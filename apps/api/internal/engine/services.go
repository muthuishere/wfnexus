package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/notify"
	"github.com/muthuishere/wfnexus/apps/api/internal/supervisor"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// SERVICE MODULES, SUPERVISED (ADR 0028, #103)
//
// `wfx module start <m> --account a [--runs-on label]` records ONE service:
// module + account. It runs on whatever serves that label — this process for
// a label it serves itself (default `local`), otherwise a joined worker — and
// nowhere else. Who runs it is a lease held by heartbeats: the first owner to
// beat keeps it while it keeps beating; a second machine with the same label
// is told no (ErrNotOwner) and stops its copy. A service whose heartbeat goes
// quiet past serviceSilentAfter is `silent` in `wfx module ls` and announced
// once on the default channel.
//
// Both records live in the existing state store under scope "service",
// scope_name "<module>/<account>": key "spec" (what to run) and "status" (the
// last heartbeat). No new table.

const serviceScope = "service"

// serviceSilentAfter is how long without a heartbeat before a service is
// silent. Three missed 15s beats plus slack.
var serviceSilentAfter = 90 * time.Second

// ErrServiceOwned is a heartbeat from a machine that does not hold the lease.
var ErrServiceOwned = supervisor.ErrNotOwner

// ServiceSpec is one wanted service.
type ServiceSpec struct {
	Module    string            `json:"module"`
	Account   string            `json:"account"`
	RunsOn    string            `json:"runsOn,omitempty"`
	Inputs    map[string]string `json:"inputs,omitempty"`
	EnabledAt time.Time         `json:"enabledAt"`
}

// Key is <module>/<account>.
func (s ServiceSpec) Key() string { return s.Module + "/" + s.Account }

// ServiceStatus is the last heartbeat, and who sent it.
type ServiceStatus struct {
	supervisor.Beat
	Owner   string    `json:"owner"`
	At      time.Time `json:"at"`
	Alerted bool      `json:"alerted,omitempty"`
}

// ServiceView is a service as `wfx module ls` shows it.
type ServiceView struct {
	ServiceSpec
	Key           string     `json:"key"`
	State         string     `json:"state"` // pending | starting | running | crashed | silent
	Owner         string     `json:"owner,omitempty"`
	Restarts      int        `json:"restarts"`
	LastError     string     `json:"lastError,omitempty"`
	LastHeartbeat *time.Time `json:"lastHeartbeat,omitempty"`
}

func (e *Engine) serviceModule(name string) (*workflow.Module, error) {
	mods, _ := e.Modules()
	for _, m := range mods {
		if m.Name == name {
			if m.Kind != "service" || m.Service == nil {
				return nil, fmt.Errorf("module %q is kind %s, not a service", name, m.Kind)
			}
			return m, nil
		}
	}
	return nil, fmt.Errorf("no module %q — wfx module ls", name)
}

// EnableService records (or replaces) one service. It renders the command
// once now so a bad input fails here, not in a crash loop on a worker.
func (e *Engine) EnableService(ctx context.Context, spec ServiceSpec) (ServiceSpec, error) {
	if e.store == nil {
		return spec, errors.New("services need the platform store")
	}
	m, err := e.serviceModule(spec.Module)
	if err != nil {
		return spec, err
	}
	if spec.Account == "" {
		spec.Account = spec.Inputs["label"]
	}
	if spec.Account == "" {
		if d, ok := m.Inputs["label"]; ok && d.Default != nil {
			spec.Account = fmt.Sprint(d.Default)
		}
	}
	if spec.Account == "" {
		spec.Account = "default"
	}
	if strings.ContainsAny(spec.Account, "/ ") {
		return spec, fmt.Errorf("account %q may not hold '/' or spaces", spec.Account)
	}
	if _, _, err := renderService(m, spec); err != nil {
		return spec, err
	}
	spec.EnabledAt = time.Now().UTC()
	b, _ := json.Marshal(spec)
	if err := e.store.PutState(ctx, serviceScope, spec.Key(), "spec", string(b)); err != nil {
		return spec, err
	}
	// A new spec starts a fresh lease and a fresh count.
	return spec, e.store.DeleteState(ctx, serviceScope, spec.Key(), "status")
}

// DisableService removes one service; its supervisor stops it on the next tick.
func (e *Engine) DisableService(ctx context.Context, module, account string) error {
	key := module + "/" + account
	if _, ok, err := e.store.GetState(ctx, serviceScope, key, "spec"); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("no service %s", key)
	}
	_ = e.store.DeleteState(ctx, serviceScope, key, "status")
	return e.store.DeleteState(ctx, serviceScope, key, "spec")
}

// renderService fills the manifest's args from the module's input defaults and
// the spec's inputs.
func renderService(m *workflow.Module, spec ServiceSpec) (string, []string, error) {
	in := map[string]any{}
	for k, d := range m.Inputs {
		if d.Default != nil {
			in[k] = fmt.Sprint(d.Default)
		}
	}
	for k, v := range spec.Inputs {
		if _, ok := m.Inputs[k]; !ok {
			return "", nil, fmt.Errorf("module %s has no input %q", m.Name, k)
		}
		in[k] = v
	}
	if _, ok := m.Inputs["label"]; ok {
		in["label"] = spec.Account // the account IS the label input
	}
	for k, d := range m.Inputs {
		if d.Required && in[k] == nil {
			return "", nil, fmt.Errorf("module %s needs input %q (--input %s=...)", m.Name, k, k)
		}
	}
	data := map[string]any{"Inputs": in}
	args := make([]string, 0, len(m.Service.Args))
	for _, a := range m.Service.Args {
		t, err := template.New("arg").Option("missingkey=error").Parse(a)
		if err != nil {
			return "", nil, fmt.Errorf("module %s: arg %q: %w", m.Name, a, err)
		}
		var b bytes.Buffer
		if err := t.Execute(&b, data); err != nil {
			return "", nil, fmt.Errorf("module %s: arg %q: %w", m.Name, a, err)
		}
		args = append(args, b.String())
	}
	cmd := m.Service.Command
	if m.Binary != nil && m.Binary.Path != "" {
		cmd = m.Binary.Path
	}
	return cmd, args, nil
}

type serviceRec struct {
	spec   ServiceSpec
	status *ServiceStatus
}

func (e *Engine) serviceRecs(ctx context.Context) ([]serviceRec, error) {
	vars, err := e.store.ListStateByPrefix(ctx, serviceScope, "")
	if err != nil {
		return nil, err
	}
	by := map[string]*serviceRec{}
	var order []string
	for _, v := range vars {
		r := by[v.ScopeName]
		if r == nil {
			r = &serviceRec{}
			by[v.ScopeName] = r
			order = append(order, v.ScopeName)
		}
		switch v.Key {
		case "spec":
			_ = json.Unmarshal([]byte(v.Value), &r.spec)
		case "status":
			var st ServiceStatus
			if json.Unmarshal([]byte(v.Value), &st) == nil {
				r.status = &st
			}
		}
	}
	sort.Strings(order)
	out := make([]serviceRec, 0, len(order))
	for _, k := range order {
		if by[k].spec.Module != "" {
			out = append(out, *by[k])
		}
	}
	return out, nil
}

func fresh(st *ServiceStatus, now time.Time) bool {
	return st != nil && now.Sub(st.At) <= serviceSilentAfter
}

// Services is every recorded service with its derived state.
func (e *Engine) Services(ctx context.Context) ([]ServiceView, error) {
	if e.store == nil {
		return nil, nil
	}
	recs, err := e.serviceRecs(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]ServiceView, 0, len(recs))
	for _, r := range recs {
		v := ServiceView{ServiceSpec: r.spec, Key: r.spec.Key(), State: "pending"}
		if r.status != nil {
			at := r.status.At
			v.LastHeartbeat, v.Owner = &at, r.status.Owner
			v.Restarts, v.LastError = r.status.Restarts, r.status.LastError
			v.State = r.status.Status
		}
		if silent(r, now) {
			v.State = "silent"
		}
		out = append(out, v)
	}
	return out, nil
}

// silent: beat once and then stopped, or never beat at all since enabled.
func silent(r serviceRec, now time.Time) bool {
	if r.status != nil {
		return !fresh(r.status, now)
	}
	return now.Sub(r.spec.EnabledAt) > serviceSilentAfter
}

// ServicesFor is what one supervisor (owner) serving labels should run: every
// service on one of its labels whose lease is free or already its own.
// local=true is this process: it takes services on the labels it serves itself.
func (e *Engine) ServicesFor(ctx context.Context, owner string, labels []string, local bool) ([]supervisor.Spec, error) {
	if e.store == nil {
		return nil, nil
	}
	recs, err := e.serviceRecs(ctx)
	if err != nil {
		return nil, err
	}
	var env map[string]string
	now := time.Now()
	var out []supervisor.Spec
	for _, r := range recs {
		if !serves(e, r.spec.RunsOn, labels, local) {
			continue
		}
		if r.status != nil && r.status.Owner != owner && fresh(r.status, now) {
			continue // someone else holds it
		}
		m, err := e.serviceModule(r.spec.Module)
		if err != nil {
			continue // uninstalled since; shown as silent in ls
		}
		cmd, args, err := renderService(m, r.spec)
		if err != nil {
			continue
		}
		if env == nil {
			// The system env (`wfx env` / `wfx module config`): the module's
			// requires and any hook token a watch names. Same as a step gets.
			env, _ = e.platformEnv(ctx, "")
			if env == nil {
				env = map[string]string{}
			}
		}
		sp := supervisor.Spec{Key: r.spec.Key(), Command: cmd, Args: args, Env: env}
		if local {
			sp.Dir = m.Dir
		}
		out = append(out, sp)
	}
	return out, nil
}

func serves(e *Engine, runsOn string, labels []string, local bool) bool {
	if local {
		return e.servesLocally(runsOn)
	}
	if e.servesLocally(runsOn) {
		return false // the server runs it itself; never also a worker
	}
	for _, l := range labels {
		if strings.EqualFold(l, runsOn) {
			return true
		}
	}
	return false
}

// ServiceBeat records one heartbeat, refusing an owner that does not hold the
// lease while the holder is still beating.
func (e *Engine) ServiceBeat(ctx context.Context, owner string, b supervisor.Beat) error {
	if _, ok, err := e.store.GetState(ctx, serviceScope, b.Key, "spec"); err != nil {
		return err
	} else if !ok {
		return ErrServiceOwned // disabled: nobody should run it
	}
	if raw, ok, err := e.store.GetState(ctx, serviceScope, b.Key, "status"); err != nil {
		return err
	} else if ok {
		var cur ServiceStatus
		if json.Unmarshal([]byte(raw), &cur) == nil && cur.Owner != owner && fresh(&cur, time.Now()) {
			return ErrServiceOwned
		}
	}
	st := ServiceStatus{Beat: b, Owner: owner, At: time.Now().UTC()}
	raw, _ := json.Marshal(st)
	return e.store.PutState(ctx, serviceScope, b.Key, "status", string(raw))
}

// StartServices runs this process's own supervisor (labels it serves itself)
// and the silence watch.
func (e *Engine) StartServices(ctx context.Context) {
	if e.store == nil {
		return
	}
	sup := &supervisor.Supervisor{
		Fetch: func(ctx context.Context) ([]supervisor.Spec, error) {
			return e.ServicesFor(ctx, "server", e.cfg.RunnerLabels, true)
		},
		Beat: func(ctx context.Context, b supervisor.Beat) error { return e.ServiceBeat(ctx, "server", b) },
	}
	go sup.Run(ctx)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				e.alertSilentServices(ctx)
			}
		}
	}()
}

// alertSilentServices announces each newly silent service once, on the default
// channel. Without a channel it is still `silent` in ls and logged here.
func (e *Engine) alertSilentServices(ctx context.Context) []string {
	recs, err := e.serviceRecs(ctx)
	if err != nil {
		return nil
	}
	now := time.Now()
	var said []string
	for _, r := range recs {
		if !silent(r, now) || (r.status != nil && r.status.Alerted) {
			continue
		}
		key := r.spec.Key()
		last := "never"
		st := ServiceStatus{Beat: supervisor.Beat{Key: key, Status: "silent"}, Owner: "", At: r.spec.EnabledAt}
		if r.status != nil {
			st = *r.status
			last = r.status.At.Format(time.RFC3339)
		}
		text := fmt.Sprintf("service %s is silent: no heartbeat since %s (runs-on %q). Check the worker: wfx module ls",
			key, last, r.spec.RunsOn)
		log.Printf("engine: %s", text)
		if _, err := e.channelEntry(""); err == nil {
			go func() {
				sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
				defer cancel()
				_, _, _ = e.SendOnChannel(sctx, "", notify.Envelope{Type: notify.TypeNotice, Text: text,
					DedupKey: "service-silent:" + key + ":" + last, At: now.UTC()})
			}()
		}
		st.Alerted = true
		raw, _ := json.Marshal(st)
		_ = e.store.PutState(ctx, serviceScope, key, "status", string(raw))
		said = append(said, key)
	}
	return said
}

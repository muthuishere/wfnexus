package engine

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
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
// Everything lives in the existing state store under scope "service",
// scope_name "<module>/<account>": "spec" (what to run), "status" (the
// supervisor's last beat), "beat" (the service's OWN last beat, from inside
// its loop), "alerted" (silence already announced) and "watch:<name>" (one
// registered watch each). No new table.
//
// The supervisor starts each service with WFX_URL, WFX_SERVICE and
// WFX_SERVICE_TOKEN (supervisor/self.go). The token is an HMAC of the key under
// the registration token: scoped to one service, nothing stored, and rotated
// with the registration token. With it the service reads its watches
// (GET /api/service-self/<key>/watches, every tick, so a new watch needs no
// restart) and beats (POST .../heartbeat). A running process whose own beat is
// older than serviceSilentAfter is hung, and shows silent like a dead one.

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

// ServiceStatus is the supervisor's last heartbeat, and who sent it.
type ServiceStatus struct {
	supervisor.Beat
	Owner string    `json:"owner"`
	At    time.Time `json:"at"`
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
	// ServiceHeartbeat is the service's own last beat; LastHeartbeat is its
	// supervisor's. Running with a stale own beat is a hung process.
	ServiceHeartbeat *time.Time        `json:"serviceHeartbeat,omitempty"`
	Watches          []json.RawMessage `json:"watches"`
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
	// A new spec starts a fresh lease and a fresh count; watches are kept.
	for _, k := range []string{"status", "beat", "alerted"} {
		if err := e.store.DeleteState(ctx, serviceScope, spec.Key(), k); err != nil {
			return spec, err
		}
	}
	return spec, nil
}

// DisableService removes one service; its supervisor stops it on the next tick.
func (e *Engine) DisableService(ctx context.Context, module, account string) error {
	key := module + "/" + account
	if _, ok, err := e.store.GetState(ctx, serviceScope, key, "spec"); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("no service %s", key)
	}
	vars, err := e.store.ListStateByPrefix(ctx, serviceScope, key)
	if err != nil {
		return err
	}
	for _, v := range vars {
		if v.ScopeName == key && v.Key != "spec" {
			_ = e.store.DeleteState(ctx, serviceScope, key, v.Key)
		}
	}
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
	spec    ServiceSpec
	status  *ServiceStatus
	beat    *time.Time // the service's own
	alerted bool
	watches []json.RawMessage
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
		case "beat":
			if t, err := time.Parse(time.RFC3339Nano, v.Value); err == nil {
				r.beat = &t
			}
		case "alerted":
			r.alerted = true
		default:
			if strings.HasPrefix(v.Key, "watch:") {
				r.watches = append(r.watches, json.RawMessage(v.Value))
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
		v := ServiceView{ServiceSpec: r.spec, Key: r.spec.Key(), State: "pending",
			ServiceHeartbeat: r.beat, Watches: r.watches}
		if v.Watches == nil {
			v.Watches = []json.RawMessage{}
		}
		if r.status != nil {
			at := r.status.At
			v.LastHeartbeat, v.Owner = &at, r.status.Owner
			v.Restarts, v.LastError = r.status.Restarts, r.status.LastError
			v.State = r.status.Status
		}
		if why := silent(r, now); why != "" {
			v.State, v.LastError = "silent", why
		}
		out = append(out, v)
	}
	return out, nil
}

// silent says why a service is silent, or "" when it is not: its supervisor
// stopped beating (or never did), or the process is running but its own loop
// has not beaten within serviceSilentAfter of starting or of its last beat.
func silent(r serviceRec, now time.Time) string {
	if r.status == nil {
		if now.Sub(r.spec.EnabledAt) > serviceSilentAfter {
			return "no supervisor has picked it up"
		}
		return ""
	}
	if !fresh(r.status, now) {
		return "its supervisor stopped beating"
	}
	if r.status.Status != "running" {
		return ""
	}
	last := r.status.StartedAt
	if r.beat != nil && r.beat.After(last) {
		last = *r.beat
	}
	if now.Sub(last) > serviceSilentAfter {
		return "running but the service itself has not beaten (hung?)"
	}
	return ""
}

// serviceToken is the credential one service holds: HMAC(key) under the
// registration token. Nothing to store, nothing to revoke but the key.
func (e *Engine) serviceToken(ctx context.Context, key string) (string, error) {
	reg, err := e.RegistrationToken(ctx)
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, []byte(reg))
	h.Write([]byte("service:" + key))
	return "wfs_" + hex.EncodeToString(h.Sum(nil)), nil
}

// AuthService checks a service's own token for its key.
func (e *Engine) AuthService(ctx context.Context, key, token string) error {
	want, err := e.serviceToken(ctx, key)
	if err != nil {
		return err
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		return ErrBadToken
	}
	return nil
}

// ServiceSelfBeat is the service's own heartbeat, from inside its loop.
func (e *Engine) ServiceSelfBeat(ctx context.Context, key string) error {
	if _, ok, err := e.store.GetState(ctx, serviceScope, key, "spec"); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("no service %s", key)
	}
	return e.store.PutState(ctx, serviceScope, key, "beat", time.Now().UTC().Format(time.RFC3339Nano))
}

// ServiceWatches is one service's registered watches, each a JSON object
// whose shape is the module's (wamonitor.Watch for whatsapp-group-monitor).
func (e *Engine) ServiceWatches(ctx context.Context, key string) ([]json.RawMessage, error) {
	vars, err := e.store.ListState(ctx, serviceScope, key)
	if err != nil {
		return nil, err
	}
	out := []json.RawMessage{}
	for _, v := range vars {
		if strings.HasPrefix(v.Key, "watch:") {
			out = append(out, json.RawMessage(v.Value))
		}
	}
	return out, nil
}

// PutServiceWatch registers (or replaces) one watch by its "name".
func (e *Engine) PutServiceWatch(ctx context.Context, key string, raw json.RawMessage) (string, error) {
	if _, ok, err := e.store.GetState(ctx, serviceScope, key, "spec"); err != nil {
		return "", err
	} else if !ok {
		return "", fmt.Errorf("no service %s — wfx module start first", key)
	}
	var w map[string]any
	if err := json.Unmarshal(raw, &w); err != nil {
		return "", fmt.Errorf("a watch is a JSON object: %w", err)
	}
	name, _ := w["name"].(string)
	if name == "" || strings.ContainsAny(name, "/ ") {
		return "", fmt.Errorf("a watch needs a \"name\" without '/' or spaces")
	}
	b, _ := json.Marshal(w)
	return name, e.store.PutState(ctx, serviceScope, key, "watch:"+name, string(b))
}

// DeleteServiceWatch removes one watch.
func (e *Engine) DeleteServiceWatch(ctx context.Context, key, name string) error {
	if _, ok, err := e.store.GetState(ctx, serviceScope, key, "watch:"+name); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("no watch %q on %s", name, key)
	}
	return e.store.DeleteState(ctx, serviceScope, key, "watch:"+name)
}

// selfURL is where a service on THIS machine reaches the API.
func (e *Engine) selfURL() string {
	if u := strings.TrimRight(e.cfg.PublicURL, "/"); u != "" {
		return u
	}
	addr := e.cfg.Addr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	return "http://" + addr
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
		tok, err := e.serviceToken(ctx, r.spec.Key())
		if err != nil {
			return nil, err
		}
		senv := make(map[string]string, len(env)+3)
		for k, v := range env {
			senv[k] = v
		}
		// WFX_URL is this server's address; a worker replaces it with the
		// URL it joined with.
		senv[supervisor.EnvURL], senv[supervisor.EnvKey], senv[supervisor.EnvToken] = e.selfURL(), r.spec.Key(), tok
		sp := supervisor.Spec{Key: r.spec.Key(), Command: cmd, Args: args, Env: senv}
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
// channel, and re-arms when it recovers. Without a channel it is still
// `silent` in ls and logged here.
func (e *Engine) alertSilentServices(ctx context.Context) []string {
	recs, err := e.serviceRecs(ctx)
	if err != nil {
		return nil
	}
	now := time.Now()
	var said []string
	for _, r := range recs {
		key := r.spec.Key()
		why := silent(r, now)
		if why == "" {
			if r.alerted {
				_ = e.store.DeleteState(ctx, serviceScope, key, "alerted")
			}
			continue
		}
		if r.alerted {
			continue
		}
		text := fmt.Sprintf("service %s is silent: %s (runs-on %q). Check it: wfx module ls", key, why, r.spec.RunsOn)
		log.Printf("engine: %s", text)
		if _, err := e.channelEntry(""); err == nil {
			go func() {
				sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
				defer cancel()
				_, _, _ = e.SendOnChannel(sctx, "", notify.Envelope{Type: notify.TypeNotice, Text: text,
					DedupKey: "service-silent:" + key + ":" + now.UTC().Format(time.RFC3339), At: now.UTC()})
			}()
		}
		_ = e.store.PutState(ctx, serviceScope, key, "alerted", now.UTC().Format(time.RFC3339))
		said = append(said, key)
	}
	return said
}

package engine

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// CALLING MODULES (#109, slim)
//
//   - A module command `<module>.<command>` is callable three ways: `uses:` in
//     a workflow, `wfx module call`, and POST /api/modules/{name}/call. The last
//     two run a generated wrapper workflow of the same name (one `uses:` step,
//     every string input mapped from the run input), so a call is an ordinary
//     run: logged, attributed, approval-gated like any other.
//   - Profiles: each module has named profiles (a handle — the account — plus
//     input defaults), one of them the default, set once globally:
//     `wfx module profile set apl business --account biz --default`. A command
//     or service names `profile:` to pick another one.
//   - Module token: a step expanded from a module's command (Step.Module), and
//     each of the module's services, get WFX_MODULE + WFX_MODULE_TOKEN (an HMAC
//     of the module under the registration token). With it the module reaches
//     its own door, /api/module-self/{module}/…: today, the OUTBOX — a send
//     that goes through the module's running service, so one process per
//     number writes to it.

const (
	profileScope  = "module-profile"
	defaultMarker = "@default"
	wrapperSource = workflow.ModuleCommandSource
)

// Profile is one named configuration of a module.
type Profile struct {
	Name    string            `json:"name"`
	Account string            `json:"account"`
	Inputs  map[string]string `json:"inputs,omitempty"`
	Default bool              `json:"default,omitempty"`
}

// SetProfile stores a profile; makeDefault (or its being the first) makes it
// the module's default.
func (e *Engine) SetProfile(ctx context.Context, module string, p Profile) (Profile, error) {
	if e.store == nil {
		return p, errors.New("profiles need the platform store")
	}
	if p.Name == "" || strings.ContainsAny(p.Name, "/ @") {
		return p, fmt.Errorf("a profile needs a name without '/', '@' or spaces")
	}
	if p.Account == "" || strings.ContainsAny(p.Account, "/ ") {
		return p, fmt.Errorf("profile %s needs an --account (the handle, e.g. the apl label) without '/' or spaces", p.Name)
	}
	if !e.moduleExists(module) {
		return p, fmt.Errorf("no module %q — wfx module ls", module)
	}
	_, hasDefault, err := e.store.GetState(ctx, profileScope, module, defaultMarker)
	if err != nil {
		return p, err
	}
	b, _ := json.Marshal(Profile{Name: p.Name, Account: p.Account, Inputs: p.Inputs})
	if err := e.store.PutState(ctx, profileScope, module, p.Name, string(b)); err != nil {
		return p, err
	}
	if p.Default || !hasDefault {
		p.Default = true
		return p, e.store.PutState(ctx, profileScope, module, defaultMarker, p.Name)
	}
	return p, nil
}

// Profiles lists a module's profiles, the default marked.
func (e *Engine) Profiles(ctx context.Context, module string) ([]Profile, error) {
	if e.store == nil {
		return nil, nil
	}
	vars, err := e.store.ListState(ctx, profileScope, module)
	if err != nil {
		return nil, err
	}
	def := ""
	out := []Profile{}
	for _, v := range vars {
		if v.Key == defaultMarker {
			def = v.Value
			continue
		}
		var p Profile
		if json.Unmarshal([]byte(v.Value), &p) == nil {
			out = append(out, p)
		}
	}
	for i := range out {
		out[i].Default = out[i].Name == def
	}
	return out, nil
}

// ResolveProfile is the named profile, or the module's default when name is "".
func (e *Engine) ResolveProfile(ctx context.Context, module, name string) (Profile, error) {
	if e.store == nil {
		return Profile{}, errors.New("profiles need the platform store")
	}
	if name == "" {
		def, ok, err := e.store.GetState(ctx, profileScope, module, defaultMarker)
		if err != nil {
			return Profile{}, err
		}
		if !ok {
			return Profile{}, fmt.Errorf("module %s has no default profile — wfx module profile set %s <name> --account <handle> --default", module, module)
		}
		name = def
	}
	raw, ok, err := e.store.GetState(ctx, profileScope, module, name)
	if err != nil {
		return Profile{}, err
	}
	if !ok {
		return Profile{}, fmt.Errorf("module %s has no profile %q — wfx module profile ls %s", module, name, module)
	}
	var p Profile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Profile{}, err
	}
	return p, nil
}

func (e *Engine) moduleExists(name string) bool {
	mods, _ := e.Modules()
	for _, m := range mods {
		if m.Name == name {
			return true
		}
	}
	return false
}

// moduleToken is the credential one module holds.
func (e *Engine) moduleToken(ctx context.Context, module string) (string, error) {
	reg, err := e.RegistrationToken(ctx)
	if err != nil {
		return "", err
	}
	h := hmac.New(sha256.New, []byte(reg))
	h.Write([]byte("module:" + module))
	return "wfm_" + hex.EncodeToString(h.Sum(nil)), nil
}

// AuthModule checks a module's token.
func (e *Engine) AuthModule(ctx context.Context, module, token string) error {
	want, err := e.moduleToken(ctx, module)
	if err != nil {
		return err
	}
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(want)) != 1 {
		return ErrBadToken
	}
	return nil
}

// moduleEnv is what a module's step or service gets to reach its door.
func (e *Engine) moduleEnv(ctx context.Context, module string) map[string]string {
	tok, err := e.moduleToken(ctx, module)
	if err != nil {
		return nil
	}
	env := map[string]string{"WFX_MODULE": module, "WFX_MODULE_TOKEN": tok}
	// The module's own files (its scripts), on this machine.
	mods, _ := e.Modules()
	for _, m := range mods {
		if m.Name == module {
			// Absolute: the step runs in its own call directory (nodes.go).
			dir := m.Dir
			if abs, err := filepath.Abs(dir); err == nil {
				dir = abs
			}
			env["WFX_MODULE_DIR"] = dir
		}
	}
	return env
}

// ---- command wrappers ----

func (e *Engine) wrapperDir() string { return filepath.Join(e.cfg.WorkDir, "module-commands") }

// IsModuleCommand reports whether a definition is a generated module-command
// wrapper. Those are callable, but listed under Modules, never as workflows.
func (e *Engine) IsModuleCommand(d *workflow.Definition) bool {
	return d != nil && d.Path != "" && e.cfg.WorkDir != "" &&
		strings.HasPrefix(filepath.Clean(d.Path), filepath.Clean(e.wrapperDir())+string(filepath.Separator))
}

// writeCommandWrappers regenerates one workflow per module command, named like
// the command, so `wfx module call` and the API start an ordinary run.
func (e *Engine) writeCommandWrappers() {
	dir := e.wrapperDir()
	if e.cfg.WorkDir == "" {
		return
	}
	_ = os.RemoveAll(dir)
	mods, _ := e.Modules()
	for _, m := range mods {
		major := strings.TrimPrefix(strings.SplitN(m.Version, ".", 2)[0], "v")
		for _, name := range m.CommandNames() {
			t, _ := m.Callable(name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return
			}
			_ = os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(wrapperYAML(name, major, t)), 0o644)
		}
	}
}

func wrapperYAML(name, major string, t *workflow.Task) string {
	props := map[string]any{}
	var required []string
	with := map[string]string{}
	keys := make([]string, 0, len(t.Inputs))
	for k := range t.Inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		spec := t.Inputs[k]
		if spec.Type != "" && spec.Type != "string" {
			continue // over a call, inputs are strings; others keep their default
		}
		p := map[string]any{"type": "string"}
		if spec.Default != nil {
			p["default"] = fmt.Sprint(spec.Default)
		}
		if spec.Description != "" {
			p["description"] = spec.Description
		}
		props[k] = p
		if spec.Required {
			required = append(required, k)
		}
		with[k] = fmt.Sprintf(`{{ or .Input.%s "" }}`, k)
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	sj, _ := json.Marshal(schema)
	wj, _ := json.Marshal(with)
	desc, _ := json.Marshal("Module command " + name + " (generated): " + t.Description)
	return fmt.Sprintf("name: %s\ndescription: %s\non: workflow_dispatch\ninput_schema: %s\nsteps:\n  - id: call\n    uses: %s@%s\n    with: %s\n",
		name, desc, sj, name, major, wj)
}

// CallModuleCommand starts `<module>.<command>` with inputs, attributed to by.
// wait > 0 waits that long for the run to end.
func (e *Engine) CallModuleCommand(ctx context.Context, name string, inputs map[string]any, by string, wait time.Duration) (*model.Run, any, error) {
	def := e.Definitions()[name]
	if def == nil || def.Source != wrapperSource {
		return nil, nil, fmt.Errorf("no module command %q — wfx module ls", name)
	}
	raw, err := e.PrepareInput(name, mustJSON(inputs))
	if err != nil {
		return nil, nil, err
	}
	run, err := e.CreateRunAdmitted(ctx, name, raw, false)
	if err != nil {
		return nil, nil, err
	}
	if err := e.store.SetTriggeredBy(ctx, run.ID, by); err != nil {
		return nil, nil, err
	}
	run.TriggeredBy = by
	e.Start(run.ID)
	if wait <= 0 {
		return run, nil, nil
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		r, err := e.store.GetRun(ctx, run.ID)
		if err != nil {
			return run, nil, err
		}
		if runStatusEnded[r.Status] || r.Status == "awaiting_approval" || r.Status == "awaiting_input" {
			return r, e.runOutput(ctx, run.ID), nil
		}
		select {
		case <-ctx.Done():
			return r, nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	r, _ := e.store.GetRun(ctx, run.ID)
	return r, nil, nil
}

// ---- the outbox: sends through the running service ----

// OutboxItem is one queued op for a service.
type OutboxItem struct {
	ID      string          `json:"id"`
	Service string          `json:"service"` // <module>.<service>/<account>
	Request json.RawMessage `json:"request"`
	Status  string          `json:"status"` // queued | sent | failed | uncertain
	Result  json.RawMessage `json:"result,omitempty"`
	At      time.Time       `json:"at"`
}

// Enqueue puts a request on the outbox of module.service for a profile's
// account. id is the idempotency key: a key seen before is never queued again,
// its item is returned as it stands.
func (e *Engine) Enqueue(ctx context.Context, module, service, profile, id string, req json.RawMessage) (OutboxItem, error) {
	p, err := e.ResolveProfile(ctx, module, profile)
	if err != nil {
		return OutboxItem{}, err
	}
	key := module + "." + service + "/" + p.Account
	if _, ok, err := e.store.GetState(ctx, serviceScope, key, "spec"); err != nil {
		return OutboxItem{}, err
	} else if !ok {
		return OutboxItem{}, fmt.Errorf("service %s is not running — wfx module start %s.%s --profile %s", key, module, service, p.Name)
	}
	if id == "" || strings.ContainsAny(id, "/ ") {
		return OutboxItem{}, fmt.Errorf("an outbox item needs an id (the idempotency key) without '/' or spaces")
	}
	if raw, ok, err := e.store.GetState(ctx, serviceScope, key, "outbox:"+id); err != nil {
		return OutboxItem{}, err
	} else if ok {
		var it OutboxItem
		_ = json.Unmarshal([]byte(raw), &it)
		return it, nil
	}
	it := OutboxItem{ID: id, Service: key, Request: req, Status: "queued", At: time.Now().UTC()}
	b, _ := json.Marshal(it)
	return it, e.store.PutState(ctx, serviceScope, key, "outbox:"+id, string(b))
}

// OutboxGet is one item.
func (e *Engine) OutboxGet(ctx context.Context, key, id string) (OutboxItem, error) {
	raw, ok, err := e.store.GetState(ctx, serviceScope, key, "outbox:"+id)
	if err != nil {
		return OutboxItem{}, err
	}
	if !ok {
		return OutboxItem{}, fmt.Errorf("no outbox item %s on %s", id, key)
	}
	var it OutboxItem
	return it, json.Unmarshal([]byte(raw), &it)
}

// Outbox is a service's queued items, oldest first.
func (e *Engine) Outbox(ctx context.Context, key string) ([]OutboxItem, error) {
	vars, err := e.store.ListState(ctx, serviceScope, key)
	if err != nil {
		return nil, err
	}
	out := []OutboxItem{}
	for _, v := range vars {
		if !strings.HasPrefix(v.Key, "outbox:") {
			continue
		}
		var it OutboxItem
		if json.Unmarshal([]byte(v.Value), &it) == nil && it.Status == "queued" {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, nil
}

// OutboxReport records what the service did with an item. Only a queued item
// moves: a send is reported once.
func (e *Engine) OutboxReport(ctx context.Context, key, id, status string, result json.RawMessage) error {
	it, err := e.OutboxGet(ctx, key, id)
	if err != nil {
		return err
	}
	if it.Status != "queued" {
		return fmt.Errorf("outbox item %s is already %s", id, it.Status)
	}
	switch status {
	case "sent", "failed", "uncertain":
	default:
		return fmt.Errorf("status is sent, failed or uncertain")
	}
	it.Status, it.Result = status, result
	b, _ := json.Marshal(it)
	return e.store.PutState(ctx, serviceScope, key, "outbox:"+id, string(b))
}

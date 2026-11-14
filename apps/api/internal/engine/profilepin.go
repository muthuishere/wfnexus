package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A module command that takes a `profile` input (gh's repo, apl's handle)
// reads its target from the module's profile. The engine resolves that
// profile — the one the call names, else the module's default — and hands it
// to the step as WFX_MODULE_PROFILE, so a step never looks the default up for
// itself. For a write call the profile is resolved at the pause, shown to the
// person asked and pinned with the rest of the call; every step of the call
// then runs with that pinned profile, after checking it still resolves the
// same.

// resolvedProfile is a profile as a call resolved it.
type resolvedProfile struct {
	Name    string            `json:"name,omitempty"`
	Account string            `json:"account,omitempty"`
	Inputs  map[string]string `json:"inputs,omitempty"`
	// Error says why nothing resolved (no default profile, no such name).
	Error string `json:"error,omitempty"`
}

// callProfile resolves the profile step's call uses; ok is false when the
// command takes no profile.
func (e *Engine) callProfile(ctx context.Context, step *workflow.Step, data workflow.TemplateData) (p resolvedProfile, ok bool) {
	if step.CallProfile == nil || step.Module == "" || e.store == nil {
		return resolvedProfile{}, false
	}
	name := *step.CallProfile
	if strings.Contains(name, "{{") {
		r, err := workflow.Render(name, data)
		if err != nil {
			return resolvedProfile{Error: "profile name: " + err.Error()}, true
		}
		name = r
	}
	name = strings.TrimSpace(name)
	got, err := e.ResolveProfile(ctx, step.Module, name)
	if err != nil {
		return resolvedProfile{Name: name, Error: err.Error()}, true
	}
	if got.Name == "" {
		got.Name = name
	}
	return resolvedProfile{Name: got.Name, Account: got.Account, Inputs: got.Inputs}, true
}

// describe is the profile as the person approving a write sees it.
func (p resolvedProfile) describe() string {
	if p.Error != "" {
		return "# profile: none resolved (" + p.Error + ")\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# profile %s: account %s", p.Name, p.Account)
	keys := make([]string, 0, len(p.Inputs))
	for k := range p.Inputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, ", %s=%s", k, p.Inputs[k])
	}
	b.WriteString("\n")
	return b.String()
}

// callHeadRow is the row id of the first step of step's call, relative to
// step's own row; step's own id when it is not part of a call.
func callHeadRow(step *workflow.Step) string {
	if step.CallHead == "" || step.CallSelf == "" || !strings.HasSuffix(step.ID, step.CallSelf) {
		return step.ID
	}
	return strings.TrimSuffix(step.ID, step.CallSelf) + step.CallHead
}

// writePauseOf is the pause record of step's write call. The call's first
// step reads it from its own row while it waits; the row's copy is cleared
// when that step starts, so it is kept here for the call's other steps.
func (e *Engine) writePauseOf(ctx context.Context, runID uuid.UUID, step *workflow.Step) (json.RawMessage, error) {
	head := callHeadRow(step)
	key := runID.String() + "|" + head
	if head == step.ID {
		if st, err := e.store.GetStep(ctx, runID, head); err == nil && st != nil && st.Status == "approved" && len(st.Pending) > 0 {
			e.writePauses.Store(key, st.Pending)
			return st.Pending, nil
		}
	}
	if v, ok := e.writePauses.Load(key); ok {
		return v.(json.RawMessage), nil
	}
	return nil, fmt.Errorf("step %s: the record of its call's approval is gone (the server restarted?); run the call again — nothing was written", step.ID)
}

// profileEnv is the WFX_MODULE_PROFILE value step runs with: for a write
// call, the profile pinned at its pause; otherwise the profile as it resolves
// now. ok is false when the command takes no profile.
func (e *Engine) profileEnv(ctx context.Context, runID uuid.UUID, step *workflow.Step, data workflow.TemplateData) (string, bool, error) {
	if step.CallProfile == nil || step.Module == "" || e.store == nil {
		return "", false, nil
	}
	if !step.WriteEffect {
		p, _ := e.callProfile(ctx, step, data)
		return string(mustJSON(p)), true, nil
	}
	pending, err := e.writePauseOf(ctx, runID, step)
	if err != nil {
		return "", false, err
	}
	var w writePause
	if json.Unmarshal(pending, &w) != nil || w.Kind != "write" || w.Profile == nil {
		return "", false, fmt.Errorf("step %s: its call was approved without a record of the profile it uses; approve it again", step.ID)
	}
	return string(mustJSON(*w.Profile)), true, nil
}

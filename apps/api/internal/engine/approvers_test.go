package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A step's `approvers:` is enforced against the AUTHENTICATED actor: a name or
// a role on the list resolves, anyone else is refused with who may answer.
func TestApproversGateWhoMayResolve(t *testing.T) {
	newRun := func(t *testing.T) (*harness, uuid.UUID) {
		def := &workflow.Definition{Name: "namedapprovers", Steps: []workflow.Step{{
			ID: "publish", Prompt: "publish", Tools: []string{"bash"}, RequiresApproval: true,
			Approvers:    []string{"ada", "role:release"},
			OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
		}}}
		normalizeForTest(def)
		h := newHarness(t, def, newFakeLLM(t, submit(map[string]any{"ok": true}), finish()), "")
		run := h.run(nil)
		if run.Status != "awaiting_approval" {
			t.Fatalf("run = %s", run.Status)
		}
		return h, run.ID
	}
	ctx := context.Background()

	t.Run("a stranger is refused, by name", func(t *testing.T) {
		h, id := newRun(t)
		err := h.eng.Approve(ctx, id, "publish", Actor{ID: "mallory", Via: "api", Authenticated: true, Role: "viewer"})
		if !errors.Is(err, ErrNotApprover) || !strings.Contains(err.Error(), "ada, role:release") {
			t.Fatalf("err = %v, want a refusal naming the approvers", err)
		}
		if st := h.steps(id)["publish"]; st.Status != "awaiting_approval" || st.ResolvedBy != "" {
			t.Fatalf("a refused approval must change nothing: %s by %q", st.Status, st.ResolvedBy)
		}
	})
	for name, by := range map[string]Actor{
		"a named user":                 {ID: "ada", Via: "api", Authenticated: true, Role: "member"},
		"a holder of the role":         {ID: "bob", Via: "api", Authenticated: true, Role: "release"},
		"the unauthenticated loopback": {ID: "anyone", Via: "cli"},
	} {
		t.Run(name, func(t *testing.T) {
			h, id := newRun(t)
			if err := h.eng.Approve(ctx, id, "publish", by); err != nil {
				t.Fatal(err)
			}
			h.wait(id)
			if st := h.steps(id)["publish"]; st.ResolvedBy != by.String() {
				t.Fatalf("resolved by %q, want %q", st.ResolvedBy, by.String())
			}
		})
	}
}

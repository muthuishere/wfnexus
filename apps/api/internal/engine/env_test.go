package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// THE GUARANTEE. A secret goes into the store and never comes back out through
// a listing — not its value, not a prefix of it. The only path out is into the
// process that runs a step.
func TestASecretIsNeverListedBack(t *testing.T) {
	h := envHarness(t)
	ctx := context.Background()
	const value = "ghp_averyrealtoken"

	if err := h.eng.SetEnvVar(ctx, store.ScopeSystem, "", "GH_TOKEN", value, true); err != nil {
		t.Fatal(err)
	}
	// A non-secret is ordinary configuration and IS shown; hiding it would make
	// the store useless for the thing it is mostly used for.
	if err := h.eng.SetEnvVar(ctx, store.ScopeSystem, "", "SERVICE_URL", "https://api.example.com", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = h.eng.DeleteEnvVar(ctx, store.ScopeSystem, "", "GH_TOKEN")
		_ = h.eng.DeleteEnvVar(ctx, store.ScopeSystem, "", "SERVICE_URL")
	})

	vars, err := h.eng.ListEnvVars(ctx, store.ScopeSystem, "")
	if err != nil {
		t.Fatal(err)
	}
	var sawSecret, sawPlain bool
	for _, v := range vars {
		switch v.Key {
		case "GH_TOKEN":
			sawSecret = true
			if v.Value != "" {
				t.Fatalf("a secret's value was listed back: %q", v.Value)
			}
			if !v.Secret {
				t.Error("the secret flag did not survive")
			}
		case "SERVICE_URL":
			sawPlain = true
			if v.Value != "https://api.example.com" {
				t.Errorf("a non-secret should be readable, got %q", v.Value)
			}
		}
	}
	if !sawSecret || !sawPlain {
		t.Fatalf("entries missing from the listing: %v", vars)
	}

	// ...and it is not sitting in the table in the clear either.
	var raw string
	if err := h.store.Pool().QueryRow(ctx,
		`SELECT encode(value_enc,'escape') FROM env_vars WHERE key='GH_TOKEN'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, value) {
		t.Fatal("the value is stored in plaintext")
	}
}

// system → project → step, each level overriding only what it names.
func TestEnvCascadesSystemThenProjectThenStep(t *testing.T) {
	h := envHarness(t)
	ctx := context.Background()
	set := func(scope, name, k, v string) {
		if err := h.eng.SetEnvVar(ctx, scope, name, k, v, false); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = h.eng.DeleteEnvVar(ctx, scope, name, k) })
	}
	set(store.ScopeSystem, "", "TIER", "system")
	set(store.ScopeSystem, "", "ONLY_SYSTEM", "yes")
	// A project of this test's own. The store is shared with whatever else
	// points at this database, and a stranger's row — sealed with a different
	// key — would otherwise fail the lookup and look like this test's bug.
	project := "envtest-" + uuid.NewString()[:8]
	set(store.ScopeProject, project, "TIER", "project")
	set(store.ScopeProject, project, "ONLY_PROJECT", "yes")

	raw, _ := json.Marshal(map[string]any{})
	r, err := h.store.CreateRun(ctx, project, h.currentWorkflow(), raw)
	if err != nil {
		t.Fatal(err)
	}
	// A run is what makes a project appear in the dashboard, and this one is
	// scaffolding. Left behind, it is a permanent row named after a test.
	t.Cleanup(func() {
		_, _ = h.store.Pool().Exec(context.Background(), `DELETE FROM workflow_runs WHERE project=$1`, project)
	})
	run := r.ID
	step := &workflow.Step{ID: "s", Env: map[string]string{"TIER": "step"}}
	got, err := h.eng.stepEnv(ctx, run, step)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"TIER": "step", "ONLY_SYSTEM": "yes", "ONLY_PROJECT": "yes",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q (full: %v)", k, got[k], want, got)
		}
	}
}

func envHarness(t *testing.T) *harness {
	t.Helper()
	t.Setenv("WFX_SECRET_KEY", "dGVzdC1rZXktMzItYnl0ZXMtZXhhY3RseS0wMDAwMDA=")
	return newHarness(t, &workflow.Definition{Name: "envwf", Steps: []workflow.Step{{
		ID: "noop", Run: "true",
	}}}, newFakeLLM(t), "")
}

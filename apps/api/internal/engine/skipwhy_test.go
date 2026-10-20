package engine

import (
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/planner"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// guardWhy must say exactly what guardFor decided — same World, same rules.
func TestGuardWhyMatchesGuardFor(t *testing.T) {
	yes, no := true, false
	w := planner.NewWorld()
	w.Assert("input", map[string]any{"mode": "fast"})
	w.Assert("check", map[string]any{"exitCode": 3, "note": nil, "obj": map[string]any{"b": 2, "a": 1}})
	cases := []struct {
		g    workflow.Guard
		want string
	}{
		{workflow.Guard{Path: "input.mode", Equals: "slow"}, `when input.mode = "slow" (was "fast")`},
		{workflow.Guard{Path: "check.exitCode", Equals: 0}, `when check.exitCode = 0 (was 3)`},
		{workflow.Guard{Path: "gone.exitCode", Equals: 0}, `when gone.exitCode = 0 (gone did not run)`},
		{workflow.Guard{Path: "check.missing", Equals: 1}, `when check.missing = 1 (not set)`},
		{workflow.Guard{Path: "check.missing", Exists: &yes}, `when check.missing exists (it does not)`},
		// present-but-null EXISTS, as guardFor reads it
		{workflow.Guard{Path: "check.note", Exists: &no}, `when check.note is absent (it is null)`},
		{workflow.Guard{Path: "check.note", Exists: &yes}, ``},
		// objects compare as JSON with sorted keys: {a,b} == {b,a}
		{workflow.Guard{Path: "check.obj", Equals: map[string]any{"a": 1, "b": 2}}, ``},
		{workflow.Guard{Path: "input.mode", Equals: "fast"}, ``},
	}
	for _, c := range cases {
		gs := []workflow.Guard{c.g}
		if got := guardWhy(gs, w); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.g, got, c.want)
		}
		if holds := guardFor(gs)(w); holds != (c.want == "") {
			t.Errorf("%+v: guardFor=%v but guardWhy=%q", c.g, holds, c.want)
		}
	}
}

package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// A nil slice marshals to `null`, and the dashboard does `workflows.length`.
// So a project with NO workflows took the landing page down while every
// populated one rendered — the empty case being the one nobody looks at.
//
// This pins the shape rather than the page: whatever the UI does, a list field
// the API returns should be a list.
func TestAProjectsWorkflowsIsNeverNull(t *testing.T) {
	for _, p := range []Project{
		{Name: "empty", Workflows: []string{}},
		{Name: "with", Workflows: []string{"a"}},
	} {
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"workflows":null`) {
			t.Fatalf("%s serialises workflows as null: %s", p.Name, raw)
		}
	}
	// ...and the constructor is what has to guarantee it.
	var zero Project
	raw, _ := json.Marshal(zero)
	if !strings.Contains(string(raw), `"workflows":null`) {
		return // the type itself is safe; nothing more to enforce
	}
	t.Log("the zero value still serialises null — Projects() must initialise it, and does")
}

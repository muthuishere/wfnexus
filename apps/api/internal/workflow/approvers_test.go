package workflow

import (
	"strings"
	"testing"
)

// `approvers:` is checked for shape at load, like every other step field: a
// typo in who may approve must fail the reload, not a 02:00 approval.
func TestApproversAreValidatedAtLoad(t *testing.T) {
	ok := strings.Replace(validWorkflow, "    tools: [bash, edit]\n",
		"    tools: [bash, edit]\n    requires_approval: true\n    approvers: [ada, \"role:release\"]\n", 1)
	dir := t.TempDir()
	writeWorkflow(t, dir, "demo.yaml", ok)
	defs, err := LoadDir(dir, catalog())
	if err != nil {
		t.Fatal(err)
	}
	if got := defs["demo"].Steps[0].Approvers; len(got) != 2 || got[1] != "role:release" {
		t.Fatalf("approvers = %v", got)
	}

	for name, list := range map[string]string{
		"empty role": `[ada, "role:"]`,
		"duplicate":  `[ada, ada]`,
		"blank":      `[""]`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeWorkflow(t, dir, "bad.yaml", strings.Replace(validWorkflow, "    tools: [bash, edit]\n",
				"    tools: [bash, edit]\n    approvers: "+list+"\n", 1))
			if _, err := LoadDir(dir, catalog()); err == nil || !strings.Contains(err.Error(), "approver") {
				t.Fatalf("err = %v, want an approvers complaint", err)
			}
		})
	}
}

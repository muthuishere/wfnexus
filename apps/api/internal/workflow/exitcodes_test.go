package workflow

import (
	"strings"
	"testing"
)

func TestExitCodesParseOnARunStep(t *testing.T) {
	dir := t.TempDir()
	writeWorkflow(t, dir, "x.yaml", "name: x\nsteps:\n"+
		"  - id: check\n    run: exit 3\n    exit-codes: { 0: ok, 3: findings }\n")
	defs, err := LoadDir(dir, catalog())
	if err != nil {
		t.Fatal(err)
	}
	got := defs["x"].Steps[0].ExitCodes
	if len(got) != 2 || got[0] != "ok" || got[3] != "findings" {
		t.Fatalf("exit-codes = %v", got)
	}
	props, _ := defs["x"].Steps[0].OutputSchema["properties"].(map[string]any)
	if _, ok := props["outcome"]; !ok {
		t.Fatal("run output schema has no outcome field")
	}
}

func TestExitCodesRejectsBadDeclarations(t *testing.T) {
	cases := map[string]string{
		"code above 255": "name: x\nsteps:\n  - id: a\n    run: exit 0\n    exit-codes: { 256: big }\n",
		"negative code":  "name: x\nsteps:\n  - id: a\n    run: exit 0\n    exit-codes: { -1: neg }\n",
		"empty name":     "name: x\nsteps:\n  - id: a\n    run: exit 0\n    exit-codes: { 3: \"\" }\n",
		"not a run step": "name: x\nsteps:\n  - id: a\n    prompt: p\n    output_schema: {type: object}\n    exit-codes: { 3: findings }\n",
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			dir := t.TempDir()
			writeWorkflow(t, dir, "x.yaml", body)
			_, err := LoadDir(dir, catalog())
			if err == nil || !strings.Contains(err.Error(), "exit-codes") {
				t.Fatalf("err = %v, want an exit-codes complaint", err)
			}
		})
	}
}

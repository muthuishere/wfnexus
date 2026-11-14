package workflow

import "testing"

// renderForTest renders a step's run text the way the engine does and returns
// the script for sh plus the environment that carries its values.
func renderForTest(t *testing.T, run string, input map[string]any, dir string) (string, []string) {
	t.Helper()
	rt, err := RenderRun(run, TemplateData{Input: input})
	if err != nil {
		t.Fatal(err)
	}
	script, env, err := rt.For("sh", dir)
	if err != nil {
		t.Fatal(err)
	}
	return script, env
}

package engine

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/testdeps"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// browser.read and an approved browser.act, run through the engine with the
// shipped module, pass shell syntax in profile, action and args to the browser
// command as literal arguments: nothing in them runs, and the command gets
// each value exactly as written.
func TestBrowserModulePassesShellSyntaxLiterally(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	if _, err := exec.LookPath("node"); err != nil {
		testdeps.Missing(t, "node", err)
	}
	box := t.TempDir()
	canary := filepath.Join(box, "CANARY")
	payloads := []string{
		"$(touch " + canary + ")",
		"$(touch CANARY)",
		"`touch " + canary + "`",
		"`touch CANARY`",
		"x;touch " + canary,
		"x; touch CANARY",
		`x"; touch ` + canary + `; echo "`,
		`x'; touch ` + canary + `; echo '`,
		`x\"$(touch ` + canary + `)\"`,
	}
	log := filepath.Join(box, "calls.log")
	fake := filepath.Join(box, "fake-browser")
	// One record per call: the profile, then each argument on its own line.
	script := "#!/bin/sh\n{ printf 'profile=%s\\n' \"$WFX_BROWSER_PROFILE\"; for a in \"$@\"; do printf 'arg=%s\\n' \"$a\"; done; echo --; } >> " + log + "\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	profiles := map[string]any{}
	for _, p := range payloads {
		profiles[p] = map[string]any{"command": []string{fake}, "folder": box, "read_actions": payloads}
	}
	cfgBody, _ := json.Marshal(map[string]any{"profiles": profiles})
	cfgPath := filepath.Join(box, "browser.json")
	if err := os.WriteFile(cfgPath, cfgBody, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WFX_BROWSER_CONFIG", cfgPath)
	mods, errs := workflow.FindModules("builtin", repoPath(t, "modules"))
	if len(errs) > 0 {
		t.Fatal(errs)
	}

	for i, p := range payloads {
		// profile and action are the payload; args are every payload as a
		// JSON array, then (second form) the payload as one plain argument.
		argsJSON, _ := json.Marshal(payloads)
		with := func(args string) map[string]string { return map[string]string{"profile": p, "action": p, "args": args} }
		wf := map[string]any{"name": "canary", "steps": []any{
			map[string]any{"id": "look", "uses": "browser.read@0", "with": with(string(argsJSON))},
			map[string]any{"id": "click", "uses": "browser.act@0", "with": with(p), "needs": []string{"look"}},
		}}
		wfBody, _ := json.Marshal(wf)
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "wf.yaml"), wfBody, 0o644); err != nil {
			t.Fatal(err)
		}
		defs, err := workflow.LoadDirWithTasks(dir, "", nil, workflow.WithModules(mods))
		if err != nil {
			t.Fatalf("payload %d: load: %v", i, err)
		}
		_ = os.Remove(log)
		h := newHarness(t, defs["canary"], newFakeLLM(t), "")
		h.eng.cfg.ModulesDir = repoPath(t, "modules")
		run := h.run(nil)
		if run.Status != "awaiting_approval" {
			t.Fatalf("payload %d: run = %s at %s (%s), want awaiting_approval", i, run.Status, run.CurrentStep, run.Error)
		}
		for _, s := range defs["canary"].Steps {
			if s.RequiresApproval {
				if err := h.eng.Approve(context.Background(), run.ID, s.ID, Actor{ID: "alice", Via: "test"}); err != nil {
					t.Fatal(err)
				}
			}
		}
		run = waitSettled(t, h, run.ID)
		if run.Status != "done" {
			t.Fatalf("payload %d: run = %s at %s (%s), want done", i, run.Status, run.CurrentStep, run.Error)
		}
		got, _ := os.ReadFile(log)
		var want strings.Builder
		want.WriteString("profile=" + p + "\narg=" + p + "\n")
		for _, a := range payloads {
			want.WriteString("arg=" + a + "\n")
		}
		want.WriteString("--\nprofile=" + p + "\narg=" + p + "\narg=" + p + "\n--\n")
		if string(got) != want.String() {
			t.Fatalf("payload %d: the browser command got\n%s\nwant\n%s", i, got, want.String())
		}
		for _, root := range []string{box, h.eng.cfg.WorkDir, dir} {
			_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err == nil && d.Name() == "CANARY" {
					t.Fatalf("payload %d: %s exists: a value ran as shell", i, path)
				}
				return nil
			})
		}
	}
}

package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// Every command of every module in modules/ is called the way `wfx module
// call` calls it (each input as `{{ .Input.<name> }}`) with values that
// hold quotes, a line equal to a heredoc delimiter, backticks, $( ), and a
// leading '-'. Each step renders and runs with fake CLIs on PATH: nothing in a
// value may run, and every in.* file a step writes must hold one of the values
// byte for byte. Modules added later (tmvp, browser, …) are covered as they
// land in modules/.
func TestModuleCommandsCarryValuesUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	modDir := filepath.Join("..", "..", "..", "..", "modules")
	mods, errs := FindModules("builtin", modDir)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if len(mods) == 0 {
		t.Fatal("no modules found")
	}
	fake := t.TempDir()
	for _, cli := range []string{"gh", "apl", "aos", "ctx-optimize", "tmvp", "node", "chrome-agent", "xbrowser"} {
		if err := os.WriteFile(filepath.Join(fake, cli), []byte("#!/bin/sh\necho '{}'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	checked := 0
	t.Cleanup(func() {
		if checked == 0 {
			t.Error("no module wrote an in.* file; nothing was compared")
		}
	})
	for _, m := range mods {
		for _, name := range m.CommandNames() {
			task, _ := m.Callable(name)
			t.Run(name, func(t *testing.T) {
				marker := filepath.Join(t.TempDir(), "ran")
				crafted := []string{
					"x\"; touch " + marker + "; echo \"",
					"x\nWFX_V\ntouch " + marker + "\ncat > /dev/null <<'WFX_V'",
					"`touch " + marker + "` $(touch " + marker + ") ${HOME} 'q' \\",
					"--help -rf /nonexistent",
				}
				keys := make([]string, 0, len(task.Inputs))
				for k := range task.Inputs {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				with := map[string]string{}
				for _, k := range keys {
					with[k] = fmt.Sprintf(`{{ .Input.%s }}`, k)
				}
				dir := t.TempDir()
				wj, _ := json.Marshal(with)
				major := strings.TrimPrefix(strings.SplitN(m.Version, ".", 2)[0], "v")
				write(t, filepath.Join(dir, "call.yaml"), fmt.Sprintf("name: call\nsteps:\n  - id: call\n    uses: %s@%s\n    with: %s\n", name, major, wj))
				// Rendering only: a write command that runs on a worker is
				// checked here too, though it may not load for a run yet.
				defs, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods), func(o *loadOptions) { o.anyRunner = true })
				if err != nil {
					t.Fatalf("load: %v", err)
				}
				// Every shape goes to every input at once, one round per shape.
				for round, shape := range crafted {
					input := map[string]any{}
					want := map[string]bool{}
					for _, k := range keys {
						v := shape + "#" + k
						input[k] = v
						want[v+"\n"] = true
					}
					work := t.TempDir()
					for _, s := range defs["call"].Steps {
						if s.Run == "" {
							continue
						}
						script, env := renderForTest(t, s.Run, input, work)
						c := exec.Command("sh", "-c", script)
						c.Dir = work
						c.Env = append(os.Environ(), env...)
						c.Env = append(c.Env, "PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"),
							"WFX_MODULE_DIR="+m.Dir, "WFX_RUNNER=test", "AOS_BIN="+filepath.Join(fake, "aos"))
						_ = c.Run() // a module may refuse fake inputs; what matters is below
						if _, err := os.Stat(marker); err == nil {
							t.Fatalf("shape %d, step %s: part of a value ran as a command", round, s.ID)
						}
					}
					files, _ := filepath.Glob(filepath.Join(work, "in.*"))
					checked += len(files)
					for _, f := range files {
						b, _ := os.ReadFile(f)
						if !want[string(b)] {
							t.Errorf("shape %d: %s holds %q, which is not one of the values", round, filepath.Base(f), b)
						}
					}
				}
			})
		}
	}
}

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every binary answers `--version`, and answering writes NOTHING.
//
// Both halves were broken and only one was obvious. `wfx-runner --version` was
// "unknown command". The server's was worse: it fell through to the config load,
// which resolves the asset roots and MATERIALISES the embedded defaults into the
// data directory — so asking a binary what version it was created files and then
// started a server. The first question anyone asks of a new binary must not have
// side effects, and it must answer on a machine with nothing configured, because
// that is the machine it is asked on.
func TestEveryBinaryAnswersVersionWithoutWritingAnything(t *testing.T) {
	if testing.Short() {
		t.Skip("builds three binaries")
	}
	for _, bin := range []struct{ name, pkg, want string }{
		{"wfx-server", ".", "wfx-server "},
		{"wfx", "./cmd/wfx", "wfx "},
		{"wfx-runner", "./cmd/wfx-runner", "wfx-runner "},
	} {
		t.Run(bin.name, func(t *testing.T) {
			dir := t.TempDir()
			exe := filepath.Join(dir, bin.name)
			if out, err := exec.Command("go", "build", "-o", exe, bin.pkg).CombinedOutput(); err != nil {
				t.Fatalf("build: %v: %s", err, out)
			}
			// A home and a data root that do not exist yet: nothing configured,
			// and anything created is visible afterwards.
			home := filepath.Join(dir, "home")
			for _, flag := range []string{"--version", "version", "-v"} {
				cmd := exec.Command(exe, flag)
				cmd.Env = append(os.Environ(), "HOME="+home, "XDG_DATA_HOME="+home, "XDG_CONFIG_HOME="+home)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%s %s: %v: %s", bin.name, flag, err, out)
				}
				if !strings.HasPrefix(string(out), bin.want) {
					t.Errorf("%s %s said %q, want it to start with %q", bin.name, flag, out, bin.want)
				}
				if strings.Count(strings.TrimSpace(string(out)), "\n") != 0 {
					t.Errorf("%s %s printed more than the version:\n%s", bin.name, flag, out)
				}
			}
			if entries, err := os.ReadDir(home); err == nil && len(entries) > 0 {
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("asking %s for its version created %v — a version must not write", bin.name, names)
			}
		})
	}
}

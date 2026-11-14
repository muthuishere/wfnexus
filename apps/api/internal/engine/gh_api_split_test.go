package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// gh.api reads only (GET); a write through the API is gh.api-write, which
// needs a method other than GET and goes through the response policy.
func TestGhApiReadsAndApiWriteWrites(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	script, _ := filepath.Abs(repoPath(t, filepath.Join("modules", "gh", "gh_cmd.py")))
	run := func(mode, cmd string, in map[string]string) (int, string) {
		t.Helper()
		dir := t.TempDir()
		for k, v := range in {
			if err := os.WriteFile(filepath.Join(dir, "in."+k), []byte(v+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		c := exec.Command("python3", script, mode, cmd)
		c.Dir = dir
		c.Env = append(os.Environ(), "GH_BIN=/bin/false")
		out, _ := c.CombinedOutput()
		return c.ProcessState.ExitCode(), string(out)
	}
	for _, method := range []string{"POST", "patch", "DELETE"} {
		if code, out := run("act", "api", map[string]string{"path": "repos/o/r/issues", "method": method}); code != 2 || !strings.Contains(out, "gh.api-write") {
			t.Errorf("gh.api with %s: exit %d %s", method, code, out)
		}
	}
	if code, out := run("act", "api-write", map[string]string{"path": "repos/o/r/issues", "method": "GET"}); code != 2 || !strings.Contains(out, "use gh.api to read") {
		t.Errorf("gh.api-write with GET: exit %d %s", code, out)
	}
	if code, out := run("plan", "api-write", map[string]string{"path": "repos/o/r/issues", "method": "POST"}); code != 11 {
		t.Errorf("gh.api-write with the default policy should draft (11): exit %d %s", code, out)
	}
}

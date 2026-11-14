package browsercfg

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The browser module's helper (modules/browser/browser.mjs), run as a step
// runs it: inputs in in.* files, config from WFX_BROWSER_CONFIG.
func runHelper(t *testing.T, cfg, profile, action, args string) (string, int) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	helper, _ := filepath.Abs("../../../../modules/browser/browser.mjs")
	work := t.TempDir()
	for k, v := range map[string]string{"profile": profile, "action": action, "args": args} {
		if err := os.WriteFile(filepath.Join(work, "in."+k), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, helper)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "WFX_BROWSER_CONFIG="+cfg, "HTTP_PROXY=", "HTTPS_PROXY=")
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

func TestBrowserHelperPicksProfileAndFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-browser")
	// Echoes its argv and the env the helper sets.
	script := "#!/bin/sh\nprintf 'argv=%s|' \"$@\"; echo \"profile=$WFX_BROWSER_PROFILE dir=$WFX_BROWSER_PROFILE_DIR proxy=$WFX_BROWSER_PROXY http=$HTTP_PROXY\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := write(t, `{"default":"us","profiles":{
		"us":{"command":["`+fake+`","--json"],"folder":"/p/us","proxy":"http://127.0.0.1:8881"},
		"de":{"command":"`+fake+`","folder":"/p/de"}}}`)

	// Named profile: its folder, no proxy; JSON args become separate argv.
	out, code := runHelper(t, cfg, "de", "open", `["https://example.com","a b"]`)
	if code != 0 || !strings.Contains(out, "argv=open|argv=https://example.com|argv=a b|") ||
		!strings.Contains(out, "profile=de dir=/p/de proxy= http=") {
		t.Fatalf("de: %d %q", code, out)
	}
	// No profile named: the default, with its proxy in both variables.
	out, code = runHelper(t, cfg, "", "read", "plain arg")
	if code != 0 || !strings.Contains(out, "argv=--json|argv=read|argv=plain arg|") ||
		!strings.Contains(out, "profile=us dir=/p/us proxy=http://127.0.0.1:8881 http=http://127.0.0.1:8881") {
		t.Fatalf("default: %d %q", code, out)
	}
	// A named profile that is not configured fails closed: non-zero, an error
	// naming the configured ones, and the command never runs (no default).
	out, code = runHelper(t, cfg, "fr", "open", "")
	if code == 0 || !strings.Contains(out, `profile "fr" is not configured; configured: de, us`) ||
		strings.Contains(out, "argv=") {
		t.Fatalf("unknown profile: %d %q", code, out)
	}
	// A missing command is refused before running.
	missing := write(t, `{"profiles":{"x":{"command":["no-such-browser-cli-xyz"],"folder":"/p"}}}`)
	if out, code = runHelper(t, missing, "x", "open", ""); code == 0 || !strings.Contains(out, "not found") {
		t.Fatalf("missing command: %d %q", code, out)
	}
	// No config at all.
	if out, code = runHelper(t, filepath.Join(dir, "none.json"), "", "open", ""); code == 0 || !strings.Contains(out, "no browser profiles configured") {
		t.Fatalf("no config: %d %q", code, out)
	}
}

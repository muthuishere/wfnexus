package browsercfg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "browser.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadMissingFileIsNotConfigured(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadBadJSON(t *testing.T) {
	if _, err := Load(write(t, "{not json")); err == nil || errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadDefaultMustBeAProfile(t *testing.T) {
	_, err := Load(write(t, `{"default":"eu","profiles":{"us":{"command":["sh"],"folder":"/x"}}}`))
	if err == nil || !strings.Contains(err.Error(), `default "eu"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckReportsCommandFolderAndUsable(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(write(t, `{"default":"ok","profiles":{
		"ok":{"command":"sh -c","folder":"`+dir+`","proxy":"http://127.0.0.1:1"},
		"nocmd":{"command":["no-such-browser-cli-xyz"],"folder":"`+dir+`"},
		"nofolder":{"command":["sh"],"folder":"`+filepath.Join(dir, "missing")+`"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Profiles["ok"].Command; len(got) != 2 || got[0] != "sh" {
		t.Fatalf("string command not split: %q", got)
	}
	by := map[string]Status{}
	for _, s := range c.Check() {
		by[s.Name] = s
	}
	if s := by["ok"]; !s.Usable || !s.Default || s.Proxy == "" {
		t.Fatalf("ok = %+v", s)
	}
	if s := by["nocmd"]; s.Usable || s.CommandFound || !strings.Contains(s.Problem, "not found") {
		t.Fatalf("nocmd = %+v", s)
	}
	if s := by["nofolder"]; s.Usable || s.FolderPresent || !strings.Contains(s.Problem, "missing") {
		t.Fatalf("nofolder = %+v", s)
	}
}

// A proxy with user or user:pass@ is a profile error, and the error does not
// repeat the credential.
func TestLoadRefusesProxyCredentials(t *testing.T) {
	for _, proxy := range []string{"http://bob:hunter2@127.0.0.1:8881", "http://bob@127.0.0.1:8881", "socks5://bob:hunter2@h:1", "bob:hunter2@127.0.0.1:8881"} {
		_, err := Load(write(t, `{"profiles":{"x":{"command":["sh"],"folder":"/p","proxy":"`+proxy+`"}}}`))
		if err == nil || !strings.Contains(err.Error(), "proxy carries credentials") || strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "bob") {
			t.Fatalf("%s: err = %v", proxy, err)
		}
	}
	if _, err := Load(write(t, `{"profiles":{"x":{"command":["sh"],"folder":"/p","proxy":"http://127.0.0.1:8881"}}}`)); err != nil {
		t.Fatalf("plain proxy refused: %v", err)
	}
}

func TestLoadReadActions(t *testing.T) {
	if _, err := Load(write(t, `{"profiles":{"x":{"command":["sh"],"folder":"/p","read_actions":["open",""]}}}`)); err == nil || !strings.Contains(err.Error(), "empty read_actions") {
		t.Fatalf("empty entry: %v", err)
	}
	if _, err := Load(write(t, `{"profiles":{"x":{"command":["sh"],"folder":"/p","read_actions":"open"}}}`)); err == nil {
		t.Fatal("a string read_actions was accepted")
	}
	c, err := Load(write(t, `{"profiles":{"x":{"command":["sh"],"folder":"/p","read_actions":["open","text"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if st := c.Check(); len(st) != 1 || strings.Join(st[0].ReadActions, ",") != "open,text" {
		t.Fatalf("status = %+v", st)
	}
}

package browsercfg

import (
	"encoding/json"
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

// Every spelling with an "@" is refused, whatever the slashes: Node and
// Chromium read a user:pass from `http:/u:p@h` and `http:///u:p@h` too.
func TestLoadRefusesAnyAtInProxy(t *testing.T) {
	for _, proxy := range []string{"http:/bob:pw1secret@h:1", "http:///bob:pw1secret@h:1", "http://u:pw1secret@h:1", "h:1/x@pw1secret", "http://h:1/?q=a@pw1secret"} {
		_, err := Load(write(t, `{"profiles":{"x":{"command":["sh"],"folder":"/p","proxy":"`+proxy+`"}}}`))
		if err == nil || !strings.Contains(err.Error(), "proxy carries credentials") || strings.Contains(err.Error(), "pw1secret") || strings.Contains(err.Error(), "bob") {
			t.Fatalf("%s: err = %v", proxy, err)
		}
	}
}

// The doctor (and so the API and the CLI) shows only scheme://host:port.
func TestDisplayProxyIsSchemeHostPortOnly(t *testing.T) {
	for in, want := range map[string]string{
		"":                                       "",
		"http://127.0.0.1:8881":                  "http://127.0.0.1:8881",
		"http://127.0.0.1:8881/":                 "http://127.0.0.1:8881",
		"http://h:1/path?token=tok1secret#frag1": "http://h:1",
		"socks5://h:1?token=tok1secret":          "socks5://h:1",
		"http://h:1#tok1secret":                  "http://h:1",
		"127.0.0.1:8881":                         "127.0.0.1:8881",
		"h:1?token=tok1secret":                   "h:1",
		"http://u:tok1secret@h:1":                "http://h:1",
		"http:/u:tok1secret@h:1":                 "(unreadable proxy address)",
		"ht tp://h:1?tok1secret":                 "(unreadable proxy address)",
		"http://?token=tok1secret":               "(unreadable proxy address)",
	} {
		if got := DisplayProxy(in); got != want || strings.Contains(got, "tok1secret") {
			t.Errorf("DisplayProxy(%q) = %q, want %q", in, got, want)
		}
	}
	c, err := Load(write(t, `{"profiles":{"x":{"command":["sh"],"folder":"/p","proxy":"http://127.0.0.1:8881/p?token=tok1secret#frag"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	st := c.Check()
	raw, _ := json.Marshal(st)
	if len(st) != 1 || st[0].Proxy != "http://127.0.0.1:8881" || strings.Contains(string(raw), "tok1secret") || strings.Contains(string(raw), "frag") {
		t.Fatalf("status = %s", raw)
	}
}

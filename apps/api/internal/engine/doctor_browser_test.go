package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The doctor's browser section never carries a proxy credential.
func TestDoctorBrowserNeverShowsProxyCredentials(t *testing.T) {
	p := filepath.Join(t.TempDir(), "browser.json")
	body := `{"profiles":{"x":{"command":["sh"],"folder":"/p","proxy":"http://bob:hunter2@127.0.0.1:8881","read_actions":["open"]}}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WFX_BROWSER_CONFIG", p)
	b := doctorBrowser()
	raw, _ := json.Marshal(b)
	if b.Error == "" || len(b.Profiles) != 0 || strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "bob") {
		t.Fatalf("doctor browser = %s", raw)
	}
}

// Every "@" spelling is a doctor error that never repeats the value, and a
// proxy with a path, query or fragment is shown as scheme://host:port only.
func TestDoctorBrowserProxyIsSchemeHostPortOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "browser.json")
	t.Setenv("WFX_BROWSER_CONFIG", p)
	for _, proxy := range []string{"http:/bob:pw1secret@h:1", "http:///bob:pw1secret@h:1", "http://u:pw1secret@h:1"} {
		body, _ := json.Marshal(map[string]any{"profiles": map[string]any{"x": map[string]any{"command": []string{"sh"}, "folder": "/p", "proxy": proxy}}})
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(doctorBrowser())
		if !strings.Contains(string(raw), "proxy carries credentials") || strings.Contains(string(raw), "pw1secret") || strings.Contains(string(raw), "bob") {
			t.Fatalf("%s: doctor browser = %s", proxy, raw)
		}
	}
	body := `{"profiles":{"x":{"command":["sh"],"folder":"/p","proxy":"http://127.0.0.1:8881/x?token=tok1secret#frag1"}}}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	b := doctorBrowser()
	raw, _ := json.Marshal(b)
	if len(b.Profiles) != 1 || b.Profiles[0].Proxy != "http://127.0.0.1:8881" || strings.Contains(string(raw), "tok1secret") || strings.Contains(string(raw), "frag1") {
		t.Fatalf("doctor browser = %s", raw)
	}
}

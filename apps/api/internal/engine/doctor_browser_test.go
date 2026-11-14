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

package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// kubectl's loop: create two, switch, current, rm. `wfx context` sees the
// same records — one store, two spellings.
func TestProfileCreateSwitchCurrentRm(t *testing.T) {
	tempContexts(t)
	if err := run([]string{"profile", "create", "dev", "--url", "http://127.0.0.1:8090"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"profile", "create", "prod", "--url", "https://wfx.example.com"}); err != nil {
		t.Fatal(err)
	}
	c, _ := loadContexts()
	if c.Current != "prod" {
		t.Fatalf("create switches to the new profile; current = %q", c.Current)
	}
	if err := run([]string{"profile", "switch", "dev"}); err != nil {
		t.Fatal(err)
	}
	if c, _ = loadContexts(); c.Current != "dev" {
		t.Fatalf("current = %q after switch dev", c.Current)
	}
	if err := run([]string{"context", "use", "prod"}); err != nil {
		t.Fatal(err)
	}
	if c, _ = loadContexts(); c.Current != "prod" {
		t.Fatal("`wfx context use` must drive the same store")
	}
	if err := run([]string{"profile", "rm", "prod"}); err != nil {
		t.Fatal(err)
	}
	if c, _ = loadContexts(); c.Contexts["prod"] != nil || c.Current != "dev" {
		t.Fatalf("rm left %+v current %q", c.Contexts["prod"], c.Current)
	}
	if err := run([]string{"profile", "switch", "nope"}); err == nil {
		t.Error("switching to an unknown profile must fail")
	}
}

// A login addressed by a profile NAME keeps that profile's URL.
func TestLoginByProfileNameKeepsItsURL(t *testing.T) {
	tempContexts(t)
	if err := run([]string{"profile", "create", "dev", "--url", "http://127.0.0.1:8090"}); err != nil {
		t.Fatal(err)
	}
	if err := putContext("dev", "tok", "muthu"); err != nil {
		t.Fatal(err)
	}
	c, _ := loadContexts()
	if c.Contexts["dev"].URL != "http://127.0.0.1:8090" || c.Contexts["dev"].Token != "tok" {
		t.Fatalf("got %+v", c.Contexts["dev"])
	}
}

// A dry-run install prints the plan and registers nothing.
func TestProfileInstallDryRunRegistersNothing(t *testing.T) {
	tempContexts(t)
	if err := run([]string{"profile", "create", "box", "--install", "ssh://ops@box", "--compose", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	c, _ := loadContexts()
	if len(c.Contexts) != 0 {
		t.Fatalf("a dry run must not register a profile: %v", c.Contexts)
	}
	o, _ := parseDeploy([]string{"server", "--to", "ssh://ops@box", "--compose"})
	if u, via := profileFromInstall(o); u != "http://127.0.0.1:8090" || via != "ssh://ops@box" {
		t.Fatalf("profile = %s via %s", u, via)
	}
}

func TestMcpFlags(t *testing.T) {
	m, err := mcpFromFlags([]string{"add", "gh", "--set", "command=npx -y gh-mcp", "--env", "GITHUB_TOKEN", "--set", "env.LOG=debug"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m["command"].([]string), " ") != "npx -y gh-mcp" || m["envFrom"].([]string)[0] != "GITHUB_TOKEN" {
		t.Fatalf("got %v", m)
	}
	if m, _ := mcpFromFlags([]string{"--set", "url=https://x/mcp"}); m["type"] != "remote" {
		t.Error("a url makes a remote server")
	}
	if _, err := mcpFromFlags([]string{"--set", "command=x", "--env", "K=ghp_abc"}); err == nil || strings.Contains(err.Error(), "ghp_abc") {
		t.Errorf("NAME=value must be refused without echoing the value, got %v", err)
	}
	if _, err := mcpFromFlags([]string{"--env", "K"}); err == nil {
		t.Error("an mcp server with neither command nor url must be refused")
	}
	if got := mcpPositional([]string{"add", "--set", "command=x y", "gh", "--env", "K"}); strings.Join(got, ",") != "add,gh" {
		t.Errorf("positional = %v", got)
	}
}

// `wfx skill add <path>` uploads the folder under its frontmatter name, and
// leaves hidden files behind.
func TestSkillAddUploadsAPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "folder-name")
	_ = os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: real-name\ndescription: d\n---\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("x"), 0o644)

	var got struct {
		Name  string            `json:"name"`
		Files map[string]string `json:"files"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/skills/installed" && r.Method == "POST" {
			_ = json.NewDecoder(r.Body).Decode(&got)
			_, _ = w.Write([]byte(`{"versioned":false,"reason":"carry off"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	tempContexts(t)
	t.Setenv("WFX_API", srv.URL)
	if err := run([]string{"skill", "add", dir}); err != nil {
		t.Fatal(err)
	}
	if got.Name != "real-name" {
		t.Fatalf("name = %q", got.Name)
	}
	if _, ok := got.Files[".git/HEAD"]; ok || len(got.Files) != 1 {
		t.Fatalf("files = %v", got.Files)
	}
	if b, _ := base64.StdEncoding.DecodeString(got.Files["SKILL.md"]); !strings.Contains(string(b), "real-name") {
		t.Fatal("SKILL.md content not sent")
	}
}

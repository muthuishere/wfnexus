package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
)

func packagesEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir()) // keep ~/.claude/skills out of the registry
	wf := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	e := &Engine{cfg: config.Config{
		WorkDir: filepath.Join(dir, "work"), SkillsDir: filepath.Join(dir, "skills"),
		McpConfig: filepath.Join(dir, "mcp.json"), RegistriesPath: filepath.Join(dir, "registries.json"),
		WorkflowsDir: wf,
	}}
	if err := e.ReloadDefinitions(); err != nil {
		t.Fatal(err)
	}
	return e
}

func skillFiles(name, desc string) map[string][]byte {
	return map[string][]byte{
		"SKILL.md":       []byte("---\nname: " + name + "\ndescription: " + desc + "\n---\nbody\n"),
		"scripts/run.sh": []byte("echo hi\n"),
	}
}

// `wfx skill add` is live without a restart: the registry has the skill as
// soon as the call returns, and rm takes it away again.
func TestInstallSkillIsLiveAndRemovable(t *testing.T) {
	e := packagesEngine(t)
	if err := e.InstallSkill("demo", skillFiles("demo", "one")); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Skills().Get("demo"); !ok {
		t.Fatal("an installed skill must be in the registry without a restart")
	}
	if got := e.InstalledSkills(); len(got) != 1 || got[0] != "demo" {
		t.Fatalf("installed = %v", got)
	}
	if err := e.RemoveInstalledSkill("demo"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Skills().Get("demo"); ok {
		t.Fatal("a removed skill must leave the registry")
	}
}

func TestInstallSkillRefusesEscapesAndBadNames(t *testing.T) {
	e := packagesEngine(t)
	if err := e.InstallSkill("../x", skillFiles("ok", "x")); err == nil {
		t.Error("a name with a path in it must be refused")
	}
	bad := skillFiles("ok", "x")
	bad["../../evil"] = []byte("x")
	if err := e.InstallSkill("ok", bad); err == nil || !strings.Contains(err.Error(), "leaves") {
		t.Errorf("a file outside the skill must be refused, got %v", err)
	}
	if err := e.InstallSkill("nomd", map[string][]byte{"a.txt": []byte("x")}); err == nil {
		t.Error("a skill without SKILL.md must be refused")
	}
}

// Snapshot → change → restore puts mcp.json and the installed skills back
// exactly: the mechanism `wfx config rollback` uses.
func TestSnapshotRestoreRoundTrip(t *testing.T) {
	e := packagesEngine(t)
	if err := e.InstallSkill("keep", skillFiles("keep", "v1")); err != nil {
		t.Fatal(err)
	}
	if err := e.SaveMcp(catalog.McpServer{Name: "gh", Command: []string{"npx", "gh-mcp"}, EnvFrom: []string{"GITHUB_TOKEN"}}); err != nil {
		t.Fatal(err)
	}
	snap, err := e.SnapshotPackages()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(snap.Mcp), "ghp_") {
		t.Fatal("the snapshot must hold names only")
	}

	if err := e.InstallSkill("extra", skillFiles("extra", "x")); err != nil {
		t.Fatal(err)
	}
	if err := e.InstallSkill("keep", skillFiles("keep", "v2")); err != nil {
		t.Fatal(err)
	}
	if err := e.DeleteMcp("gh"); err != nil {
		t.Fatal(err)
	}

	if err := e.RestorePackages(context.Background(), snap); err != nil {
		t.Fatal(err)
	}
	if got := e.InstalledSkills(); len(got) != 1 || got[0] != "keep" {
		t.Fatalf("after restore installed = %v, want [keep]", got)
	}
	b, _ := os.ReadFile(filepath.Join(e.InstalledSkillsDir(), "keep", "SKILL.md"))
	if !strings.Contains(string(b), "v1") {
		t.Fatalf("restore must bring back v1, got %q", b)
	}
	if _, ok := e.Catalog().Mcp.Get("gh"); !ok {
		t.Fatal("restore must bring the mcp server back, live")
	}
}

// A file changed by hand (or by a rollback) is picked up by the watcher with
// no API call at all.
func TestCatalogWatcherReloadsOnDiskChange(t *testing.T) {
	e := packagesEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.watchCatalog(ctx, 20*time.Millisecond)
	time.Sleep(50 * time.Millisecond)

	dir := filepath.Join(e.InstalledSkillsDir(), "byhand")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), skillFiles("byhand", "h")["SKILL.md"], 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.cfg.McpConfig, []byte(`{"mcpServers":{"hand":{"command":["x"]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, sk := e.Skills().Get("byhand")
		_, mc := e.Catalog().Mcp.Get("hand")
		if sk && mc {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the watcher did not reload the catalog after a change on disk")
}

// envFrom resolves at step build time; a name set nowhere is an error that
// names it, never a server started without its credential.
func TestResolveMcpEnv(t *testing.T) {
	e := packagesEngine(t)
	entry := map[string]any{"command": []any{"x"}, "envFrom": []any{"WFX_TEST_MCP_KEY"}}
	if _, err := e.resolveMcpEnv(context.Background(), "gh", entry); err == nil || !strings.Contains(err.Error(), "WFX_TEST_MCP_KEY") {
		t.Fatalf("an unset name must fail naming it, got %v", err)
	}
	t.Setenv("WFX_TEST_MCP_KEY", "value-from-env")
	got, err := e.resolveMcpEnv(context.Background(), "gh", entry)
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if _, still := m["envFrom"]; still {
		t.Error("envFrom is ours, not toolnexus's — it must not be passed on")
	}
	if m["environment"].(map[string]any)["WFX_TEST_MCP_KEY"] != "value-from-env" {
		t.Errorf("environment = %v", m["environment"])
	}
}

package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The precedence rule, which is the whole point of having both: the ENVIRONMENT
// wins over the file. An image may carry a config and its operator sets env
// vars; the operator must win.
func TestEnvironmentBeatsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("addr: \":9999\"\nmodel:\n  model: from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != ":9999" || cfg.Model != "from-file" {
		t.Fatalf("the file was not read: addr=%s model=%s", cfg.Addr, cfg.Model)
	}

	t.Setenv("WFX_ADDR", ":7777")
	cfg, _ = LoadWithFile(path)
	if cfg.Addr != ":7777" {
		t.Fatalf("the environment did not win: %s", cfg.Addr)
	}
	if cfg.Model != "from-file" {
		t.Fatalf("an unrelated field was lost: %s", cfg.Model)
	}
}

// `mode: local` picks the defaults a person wants on a laptop. It is a
// shorthand, not a second code path — anything stated explicitly still wins.
func TestLocalModeSelectsLaptopDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	_ = os.WriteFile(path, []byte("mode: local\n"), 0o600)
	cfg, err := LoadWithFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorageDriver != "sqlite" {
		t.Errorf("storage = %s, want sqlite", cfg.StorageDriver)
	}
	if cfg.ArtifactDriver != "folder" {
		t.Errorf("artifacts = %s, want folder", cfg.ArtifactDriver)
	}

	_ = os.WriteFile(path, []byte("mode: local\nstorage:\n  driver: postgres\n"), 0o600)
	cfg, _ = LoadWithFile(path)
	if cfg.StorageDriver != "postgres" {
		t.Errorf("an explicit driver must beat the shorthand, got %s", cfg.StorageDriver)
	}
}

// An absent file is the normal state of a fresh install, not an error.
func TestAbsentFileIsFine(t *testing.T) {
	cfg, err := LoadWithFile(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("an absent config file was an error: %v", err)
	}
	if cfg.Addr == "" {
		t.Error("defaults were not applied")
	}
}

// A misspelled key is silence otherwise, and silence here means running with a
// default the person believes they overrode.
func TestAMisspelledKeyIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	_ = os.WriteFile(path, []byte("addrr: \":9999\"\n"), 0o600)
	if _, err := LoadWithFile(path); err == nil {
		t.Fatal("a misspelled key was accepted silently")
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
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

// THE ZERO-CONFIG CASE, which is the one a stranger meets first: a binary in an
// empty directory, no file, no env. It must pick the defaults that need nothing
// running — sqlite in a file, artifacts in a folder — not a Postgres nobody
// started.
func TestNoConfigAtAllIsLocal(t *testing.T) {
	cfg, err := LoadWithFile(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("no config was an error: %v", err)
	}
	if cfg.StorageDriver != "sqlite" {
		t.Errorf("storage = %s, want sqlite", cfg.StorageDriver)
	}
	if cfg.ArtifactDriver != "folder" {
		t.Errorf("artifacts = %s, want folder", cfg.ArtifactDriver)
	}
	if strings.HasPrefix(cfg.DatabaseURL, "postgres://") {
		t.Errorf("the sqlite default got a postgres DSN: %s", cfg.DatabaseURL)
	}
}

// `mode: server` is how a deployment asks for the other set — the same
// shorthand in the other direction, and what the containers now state.
func TestServerModeSelectsDeploymentDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	_ = os.WriteFile(path, []byte("mode: server\n"), 0o600)
	cfg, err := LoadWithFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorageDriver != "postgres" {
		t.Errorf("storage = %s, want postgres", cfg.StorageDriver)
	}
	if cfg.ArtifactDriver != "s3" {
		t.Errorf("artifacts = %s, want s3", cfg.ArtifactDriver)
	}
	if !strings.HasPrefix(cfg.DatabaseURL, "postgres://") {
		t.Errorf("postgres default DSN = %s", cfg.DatabaseURL)
	}
}

// WFX_MODE is the same shorthand for something that has an environment but no
// file — a container.
func TestModeFromTheEnvironment(t *testing.T) {
	t.Setenv("WFX_MODE", "server")
	cfg, err := LoadWithFile(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorageDriver != "postgres" || cfg.ArtifactDriver != "s3" {
		t.Fatalf("WFX_MODE=server gave %s/%s", cfg.StorageDriver, cfg.ArtifactDriver)
	}
}

// An explicit value beats the shorthand in BOTH directions, because the
// shorthand is a set of defaults and a default is the thing that loses.
func TestExplicitBeatsTheShorthandBothWays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	// server, but sqlite: the DSN default must follow the DRIVER, not the
	// mode, or it hands a file-path driver a postgres URL.
	_ = os.WriteFile(path, []byte("mode: server\nstorage:\n  driver: sqlite\n"), 0o600)
	cfg, err := LoadWithFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorageDriver != "sqlite" {
		t.Errorf("explicit sqlite lost to mode: server, got %s", cfg.StorageDriver)
	}
	if strings.HasPrefix(cfg.DatabaseURL, "postgres://") {
		t.Errorf("sqlite got a postgres DSN: %s", cfg.DatabaseURL)
	}

	// local, but postgres — from the environment this time, which outranks
	// both the file and the shorthand.
	_ = os.WriteFile(path, []byte("mode: local\n"), 0o600)
	t.Setenv("WFX_STORAGE_DRIVER", "postgres")
	t.Setenv("WFX_ARTIFACT_DRIVER", "s3")
	cfg, _ = LoadWithFile(path)
	if cfg.StorageDriver != "postgres" || cfg.ArtifactDriver != "s3" {
		t.Fatalf("the environment lost to mode: local, got %s/%s", cfg.StorageDriver, cfg.ArtifactDriver)
	}
}

// A stated value that is wrong must FAIL, never fall back to the local default.
// Silently running sqlite when someone asked for "postgress" is exactly the
// failure this change could have introduced.
func TestAStatedButWrongValueFailsRatherThanFallingBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	for _, tc := range []struct{ name, yaml string }{
		{"mode", "mode: sever\n"},
		{"storage driver", "storage:\n  driver: postgress\n"},
		{"artifact driver", "artifacts:\n  driver: bucket\n"},
	} {
		_ = os.WriteFile(path, []byte(tc.yaml), 0o600)
		if _, err := LoadWithFile(path); err == nil {
			t.Errorf("a wrong %s was accepted and defaulted away", tc.name)
		}
	}

	// And the same from the environment.
	t.Setenv("WFX_STORAGE_DRIVER", "mysql")
	if _, err := LoadWithFile(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("WFX_STORAGE_DRIVER=mysql was accepted")
	}
}

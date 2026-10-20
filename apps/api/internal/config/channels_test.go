package config

import (
	"os"
	"path/filepath"
	"testing"
)

// channels.default is read from the file, and WFX_DEFAULT_CHANNEL wins — the
// same rule as every other setting.
func TestDefaultChannelFromFileAndEnv(t *testing.T) {
	t.Setenv("WFX_DEFAULT_CHANNEL", "")
	os.Unsetenv("WFX_DEFAULT_CHANNEL")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("channels:\n  default: ops\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultChannel != "ops" {
		t.Fatalf("file: %q", cfg.DefaultChannel)
	}
	t.Setenv("WFX_DEFAULT_CHANNEL", "mail")
	cfg, _ = LoadWithFile(path)
	if cfg.DefaultChannel != "mail" {
		t.Fatalf("env did not win: %q", cfg.DefaultChannel)
	}
}

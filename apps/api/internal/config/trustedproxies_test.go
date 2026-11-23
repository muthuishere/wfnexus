package config

import (
	"os"
	"path/filepath"
	"testing"
)

// #191: no trusted proxies unless stated; the file and the environment both
// state them; a wrong entry refuses to start rather than trusting nothing.
func TestTrustedProxiesDefaultToNone(t *testing.T) {
	t.Setenv("WFX_TRUSTED_PROXIES", "")
	cfg, err := LoadWithFile(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Fatalf("trusted proxies = %v, want none", cfg.TrustedProxies)
	}
}

func TestTrustedProxiesFromTheFileAndTheEnvironment(t *testing.T) {
	t.Setenv("WFX_TRUSTED_PROXIES", "")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("trustedProxies: [\"192.0.2.0/24\", \"2001:db8::1\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadWithFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[0].String() != "192.0.2.0/24" || cfg.TrustedProxies[1].String() != "2001:db8::1/128" {
		t.Fatalf("from the file: %v", cfg.TrustedProxies)
	}

	t.Setenv("WFX_TRUSTED_PROXIES", " 198.51.100.7 , ::ffff:203.0.113.0/120")
	cfg, err = LoadWithFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[0].String() != "198.51.100.7/32" || cfg.TrustedProxies[1].String() != "203.0.113.0/24" {
		t.Fatalf("from the environment: %v", cfg.TrustedProxies)
	}
}

func TestAWrongTrustedProxyIsRefused(t *testing.T) {
	t.Setenv("WFX_TRUSTED_PROXIES", "192.0.2.0/24,proxy.example")
	if _, err := LoadWithFile(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("an entry that is not an address was accepted")
	}
}

func TestACatchAllTrustedProxyIsRefused(t *testing.T) {
	for _, e := range []string{"0.0.0.0/0", "::/0", "::ffff:0.0.0.0/96", "198.51.100.0/0"} {
		if _, err := ParseTrustedProxies([]string{e}); err == nil {
			t.Errorf("%s was accepted; it trusts every address", e)
		}
	}
	t.Setenv("WFX_TRUSTED_PROXIES", "0.0.0.0/0")
	if _, err := LoadWithFile(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatal("config load accepted a catch-all trusted proxy")
	}
}

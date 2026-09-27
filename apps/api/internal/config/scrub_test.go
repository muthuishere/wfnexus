package config

import (
	"os"
	"testing"
)

// A run inherits the server's environment. The server's own configuration —
// a runner token, the database URL, the secrets key — must not be in it, and a
// variable the server never read (a step's own, an API key) must stay.
func TestTheServersOwnConfigurationIsKeptOutOfWhatRunsInherit(t *testing.T) {
	t.Setenv("WFX_RUNNER_TOKEN", "runner-secret")
	t.Setenv("DATABASE_URL", "postgres://u:pw@db/x")
	t.Setenv("WFX_DEFAULT_PROVIDER", "opencode-acp")
	t.Setenv("WFX_TEST_SECRET_KEY", "platform-key")
	t.Setenv("OPENROUTER_API_KEY", "a-model-key")
	t.Setenv("SOMETHING_ELSE", "kept")

	c, _ := LoadWithFile("")
	c.SecretKeyEnv = "WFX_TEST_SECRET_KEY"
	if c.DefaultProvider != "opencode-acp" {
		t.Fatal("the configuration was not read before scrubbing")
	}
	ScrubServerEnv(c)

	for _, k := range []string{"WFX_RUNNER_TOKEN", "DATABASE_URL", "WFX_DEFAULT_PROVIDER", "WFX_TEST_SECRET_KEY"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s is still inherited (%q)", k, v)
		}
	}
	for _, k := range []string{"OPENROUTER_API_KEY", "SOMETHING_ELSE"} {
		if os.Getenv(k) == "" {
			t.Errorf("%s was removed, but the server never read it as configuration", k)
		}
	}
}

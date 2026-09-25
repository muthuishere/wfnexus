package engine

import (
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
)

// task 8.5 — a `cli` provider's readiness is a PATH lookup, so the report must
// say the binary is PRESENT and stop there. Reporting Ready alone reads as "the
// run will work", which nothing here checked: the credential lives inside the
// CLI, owned by whoever owns the seat.
func TestACliProviderIsPresentNotAuthenticated(t *testing.T) {
	got := checkProvider(catalog.Provider{Name: "shell-agent", Kind: catalog.KindCLI, Command: []string{"sh"}})
	if !got.Ready {
		t.Fatalf("sh is on PATH and the entry was not ready: %q", got.Problem)
	}
	if !got.AuthUnknown {
		t.Fatal("a PATH hit was reported as a plain tick, which claims a login nobody checked")
	}
}

// The same rule in doctor's own report: an http provider whose key IS set is
// genuinely ready and gets no note, because a caveat on everything is a caveat
// on nothing.
func TestAKeyedHttpProviderGetsNoAuthenticationCaveat(t *testing.T) {
	t.Setenv("DOCTOR_TEST_KEY", "value-never-printed")
	got := checkProvider(catalog.Provider{
		Name: "openrouter", Kind: catalog.KindHTTP,
		BaseURL: "https://openrouter.ai/api/v1", APIKeyEnv: "DOCTOR_TEST_KEY",
	})
	if !got.Ready || got.AuthUnknown {
		t.Fatalf("a keyed http provider reported ready=%v authUnknown=%v", got.Ready, got.AuthUnknown)
	}
	if got.Login != "" {
		t.Fatalf("an http provider was given a login command: %q", got.Login)
	}
}

// A vendor we have no confident login command for gets none. A wrong command
// reads as a checked fact.
func TestAnUnknownVendorIsOfferedNoLoginCommand(t *testing.T) {
	if cmd := presetLogin(catalog.Provider{Command: []string{"sh"}}); cmd != "" {
		t.Fatalf("invented a login command for an unknown vendor: %q", cmd)
	}
	if cmd := presetLogin(catalog.Provider{Preset: "claude"}); cmd != "claude" {
		t.Fatalf("the claude preset lost its login pointer: %q", cmd)
	}
}

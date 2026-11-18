// Package testdeps decides what a test does when something it needs from the
// machine (node, the test Postgres, MinIO) is not there. By default it skips,
// so the suite runs on a laptop with nothing installed. With
// WFX_TEST_REQUIRE_DEPS=1 the same test fails instead: an authoritative run
// sets it, so a test that cannot run is seen as not having run.
package testdeps

import (
	"os"
	"testing"
)

// Env is the switch.
const Env = "WFX_TEST_REQUIRE_DEPS"

// Required is whether missing dependencies fail tests.
func Required() bool { return os.Getenv(Env) == "1" }

// Missing skips t because what is not available, or fails it under the switch.
func Missing(t testing.TB, what string, why any) {
	t.Helper()
	if Required() {
		t.Fatalf("%s is required (%s=1): %v", what, Env, why)
	}
	t.Skipf("%s not available: %v", what, why)
}

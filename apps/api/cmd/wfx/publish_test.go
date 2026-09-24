package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// task 10.7 — `wfx publish` with no configured host is an ERROR, not a
// degraded or local-only mode, and it writes nothing.
func TestPublishWithNoHostIsAnErrorAndWritesNothing(t *testing.T) {
	tempContexts(t)
	dir := t.TempDir()
	wf := filepath.Join(dir, "w.yaml")
	if err := os.WriteFile(wf, []byte("name: w\nsteps:\n  - id: one\n    prompt: hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	err = publish([]string{wf, "--version", "1.0.0"})
	if err == nil {
		t.Fatal("publishing with no host succeeded")
	}
	if !strings.Contains(err.Error(), "wfx login --url") {
		t.Fatalf("the error does not say what to do: %v", err)
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("a refused publish wrote to the working tree: %d -> %d entries", len(before), len(after))
	}
}

// A publish with no --version is refused before anything else: a published
// version is immutable, so it has to be named rather than guessed.
func TestPublishRequiresAVersion(t *testing.T) {
	tempContexts(t)
	if err := publish([]string{"w.yaml"}); err == nil || !strings.Contains(err.Error(), "--version") {
		t.Fatalf("publishing with no version: %v", err)
	}
}

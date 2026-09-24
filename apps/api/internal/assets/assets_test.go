package assets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The precedence rule is the whole point of this package, so it is the thing
// under test: a directory that exists is used as-is, and nothing embedded is
// extracted behind it.
func TestResolvePrefersDisk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	onDisk := filepath.Join(t.TempDir(), "templates")
	if err := os.MkdirAll(onDisk, 0o755); err != nil {
		t.Fatal(err)
	}

	got, origin, warn, err := Resolve(TemplatesName, onDisk, true)
	if err != nil {
		t.Fatal(err)
	}
	if origin != FromDisk || got != onDisk {
		t.Fatalf("want the on-disk dir, got %s from %s", got, origin)
	}
	if warn != "" {
		t.Fatalf("no warning expected, got %q", warn)
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "wfnexus")); err == nil {
		t.Fatal("nothing should have been extracted while a real directory exists")
	}
}

// A path someone STATED that does not exist must be reported, not swallowed.
func TestResolveWarnsOnExplicitMissingPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, warn, err := Resolve(TemplatesName, filepath.Join(t.TempDir(), "nope"), true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn, "does not exist") {
		t.Fatalf("expected a warning about the missing configured path, got %q", warn)
	}
}

// And one that was merely defaulted is a normal fresh install: no warning.
func TestResolveQuietOnDefaultedMissingPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, warn, err := Resolve(TemplatesName, filepath.Join(t.TempDir(), "nope"), false)
	if err != nil {
		t.Fatal(err)
	}
	if warn != "" {
		t.Fatalf("a defaulted path that does not exist is not a mistake, got %q", warn)
	}
}

// Extraction must never clobber a file the user already has.
func TestExtractDoesNotOverwrite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dst := filepath.Join(DefaultsDir(), TemplatesName)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(dst, "mine.yaml")
	if err := os.WriteFile(mine, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Extract(TemplatesName); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(mine)
	if err != nil || string(raw) != "mine" {
		t.Fatalf("extraction overwrote a user file: %q %v", raw, err)
	}
}

// Workflows are user data: missing is not fatal, it is a directory to create.
func TestEnsureWorkflowsDirCreates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := filepath.Join(t.TempDir(), "workflows")
	got, warn, err := EnsureWorkflowsDir(want, false)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
	if warn == "" {
		t.Fatal("creating a directory should be said out loud")
	}
	if st, err := os.Stat(got); err != nil || !st.IsDir() {
		t.Fatalf("directory not created: %v", err)
	}
}

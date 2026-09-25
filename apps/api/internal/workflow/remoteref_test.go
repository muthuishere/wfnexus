package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRefTable(t *testing.T) {
	cases := []struct {
		in     string
		remote string
		ref    string
		bundle string
	}{
		// Actions' short form: no dot in the first segment ⇒ github.com.
		{"acme/bug-fix@v1.2.0", "https://github.com/acme/bug-fix.git", "v1.2.0", ""},
		// Go's module-path form: a dot in the first segment ⇒ that host.
		{"git.acme.internal/team/wf@v1.2.0", "https://git.acme.internal/team/wf.git", "v1.2.0", ""},
		// GitHub Enterprise is case (c), not a setting.
		{"ghe.acme.com/team/wf@v1.2.0", "https://ghe.acme.com/team/wf.git", "v1.2.0", ""},
		// An explicit URL goes to git verbatim; the ref is after the LAST @.
		{"ssh://git@nas.lan/srv/wf.git@a1b2c3d", "ssh://git@nas.lan/srv/wf.git", "a1b2c3d", ""},
		{"https://git.acme.com/team/wf.git@v2", "https://git.acme.com/team/wf.git", "v2", ""},
		// git's other spelling of an SSH remote.
		{"git@nas.lan:srv/wf.git@v1", "git@nas.lan:srv/wf.git", "v1", ""},
		// A repository holding several bundles selects with a fragment.
		{"acme/workflows@v1.2.0#bug-fix", "https://github.com/acme/workflows.git", "v1.2.0", "bug-fix"},
		// A commit SHA sits in the same position as a tag.
		{"acme/wf@0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c", "https://github.com/acme/wf.git", "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c", ""},
		// A deeper path on a self-hosted host keeps every segment.
		{"git.acme.internal/group/sub/wf@main", "https://git.acme.internal/group/sub/wf.git", "main", ""},
	}
	for _, c := range cases {
		if !IsRemoteUse(c.in) {
			t.Fatalf("%q should read as a remote reference", c.in)
		}
		got, err := ParseRef(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got.Remote != c.remote || got.Ref != c.ref || got.Bundle != c.bundle {
			t.Fatalf("%q: got remote=%q ref=%q bundle=%q; want %q/%q/%q",
				c.in, got.Remote, got.Ref, got.Bundle, c.remote, c.ref, c.bundle)
		}
		if got.Raw != c.in {
			t.Fatalf("%q: the reference as authored must survive, got %q", c.in, got.Raw)
		}
	}
}

// The two that must stay LOCAL: no @, so no host rule applies at all.
func TestBareNamesStayLocal(t *testing.T) {
	for _, v := range []string{"reproduce-bug", "acme/bug-fix"} {
		if IsRemoteUse(v) {
			t.Fatalf("%q must stay a local task name", v)
		}
		if _, err := ParseRef(v); err == nil {
			t.Fatalf("%q is not a remote reference and ParseRef must say so", v)
		}
	}
}

// [SEC-TEST] task 1.3 — a reference must not name a local path or traverse out
// of a checkout. Each refusal must carry a reason.
func TestParseRefRefusesLocalPaths(t *testing.T) {
	for _, v := range []string{
		"../../etc/passwd@v1",
		"../sibling/wf@v1",
		"acme/../../../etc@v1",
		"/etc/passwd@v1",
		"/srv/wf.git@v1",
		// NOTE: file:// is NOT in this list. It is allowed deliberately — a bare
		// repo on a mounted path is how an air-gapped site works, and an
		// explicit URL is an operator naming a remote, not a path smuggled in.
		// See TestExplicitRemoteURLsAreAllowedAndBarePathsAreNot.
		"~/wf@v1",
		`C:\repos\wf@v1`,
		"./wf@v1",
	} {
		_, err := ParseRef(v)
		if err == nil {
			t.Fatalf("%q must be refused", v)
		}
		if !strings.Contains(err.Error(), "names a repository, not") {
			t.Fatalf("%q: the refusal must say why, got %v", v, err)
		}
	}
}

func TestParseRefRefusesMalformed(t *testing.T) {
	for _, v := range []string{"acme@v1", "@v1", "acme/wf@", "acme/wf@v1#"} {
		if _, err := ParseRef(v); err == nil {
			t.Fatalf("%q must be refused", v)
		}
	}
}

// [SEC-TEST] non-negotiable 1 — a local `use:` is untouched: no git process,
// no network, no cache directory. Asserted by putting a git on PATH that
// records any invocation and fails.
func TestLocalUseStartsNoGit(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	witness := filepath.Join(dir, "git-was-run")
	script := "#!/bin/sh\necho \"$@\" >> " + witness + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	tasks := filepath.Join(dir, "tasks")
	wfs := filepath.Join(dir, "workflows")
	for _, d := range []string{tasks, wfs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(tasks, "reproduce.yaml"), `
name: reproduce-bug
steps:
  - id: repro
    prompt: reproduce it
    output_schema:
      type: object
      required: [ok]
      properties:
        ok: {type: boolean}
`)
	write(t, filepath.Join(wfs, "wf.yaml"), `
name: local-only
uses:
  - use: reproduce-bug
`)
	defs, err := LoadDirWithTasks(wfs, tasks, catalog())
	if err != nil {
		t.Fatal(err)
	}
	if len(defs["local-only"].Steps) != 1 || defs["local-only"].Steps[0].ID != "reproduce-bug.repro" {
		t.Fatalf("a bare task name must expand exactly as before, got %+v", defs["local-only"].Steps)
	}
	if len(defs["local-only"].RemotePins) != 0 {
		t.Fatal("a local use pins nothing")
	}
	if _, err := os.Stat(witness); err == nil {
		raw, _ := os.ReadFile(witness)
		t.Fatalf("a local use must start no git process; git ran with: %s", raw)
	}
	// And no cache directory was created anywhere under the test root.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		switch e.Name() {
		case "bin", "tasks", "workflows":
		default:
			t.Fatalf("a local use created %q", e.Name())
		}
	}
}

// An unknown local task still fails as before, and is NOT read as a remote.
func TestUnknownLocalTaskUnchanged(t *testing.T) {
	dir := t.TempDir()
	wfs := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(wfs, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(wfs, "wf.yaml"), "name: w\nuses:\n  - use: nope\n")
	_, err := LoadDirWithTasks(wfs, "", catalog())
	if err == nil || !strings.Contains(err.Error(), `unknown task "nope"`) || !strings.Contains(err.Error(), "known:") {
		t.Fatalf("want the unchanged unknown-task error, got %v", err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// [SEC-TEST] `git clone 'ext::sh -c <command>'` RUNS that command. A `use:`
// comes from a workflow somebody else published, so this is the untrusted
// input path. The rule is an allowlist of schemes precisely so that a
// transport helper nobody here has heard of is refused by default.
func TestATransportHelperIsRefused(t *testing.T) {
	for _, raw := range []string{
		"ext::sh -c curl@v1",
		"EXT::sh -c id@v1",
		"transport::whatever@v1",
		"fd::7@v1",
	} {
		if _, err := ParseRef(raw); err == nil {
			t.Errorf("%q was accepted; a transport helper can run a command", raw)
		}
	}
}

// file:// is ALLOWED on purpose: a bare repo on a mounted path is how an
// air-gapped site works. A bare path or a traversal is still refused.
func TestExplicitRemoteURLsAreAllowedAndBarePathsAreNot(t *testing.T) {
	for _, ok := range []string{
		"file:///srv/git/bundles.git@v1.0.0",
		"ssh://git@git.internal/org/repo.git@v1.0.0",
		"https://git.example.com/org/repo.git@v1.0.0",
		"git://localhost:9418/repo.git@v1.0.0",
		"git@github.com:org/repo.git@v1.0.0",
		"FILE:///srv/git/bundles.git@v1.0.0", // scheme matching is case-insensitive
	} {
		if _, err := ParseRef(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
	for _, no := range []string{
		"/srv/git/bundles.git@v1",
		"../../etc@v1",
		"~/repo@v1",
		"C:\\repo@v1",
	} {
		if _, err := ParseRef(no); err == nil {
			t.Errorf("%q was accepted as a remote", no)
		}
	}
}

package remoteuse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/bundle"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Group 9 — the three-scale check, which is the whole point of the change and
// therefore the thing that must be proved rather than reasoned about.
//
// Every scale here runs against a LOCAL BARE REPOSITORY. No github.com, no
// network, no account. That is not a convenience for the test: it is the claim.
// A registry that only works against one vendor's host is the thing this change
// exists to avoid, so a test that reached for github.com would be testing the
// opposite of the design.

// 9.1 — ONE PERSON. A workflow whose every `use:` is a bare task name behaves
// exactly as it did before any of this existed.
//
// "Exactly as today" is asserted the only way it can be honestly asserted: git
// is REPLACED ON PATH by a stub that fails loudly if anything calls it. A test
// that merely observed the right answer would pass just as well if a git
// subprocess ran and its result were discarded.
func TestOnePersonNeverTouchesGit(t *testing.T) {
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "git"),
		[]byte("#!/bin/sh\necho 'a local-only load called git' >&2\nexit 127\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stub) // ONLY the stub: no real git is reachable at all.

	dir, tasks := t.TempDir(), t.TempDir()
	// The task declares an output_schema because every agent step must: a task's
	// steps are validated exactly like hand-written ones, which is the property
	// that makes a task worth having.
	write(t, filepath.Join(tasks, "reviewer-task.yaml"), `name: reviewer-task
steps:
  - id: review
    prompt: review it
    output_schema:
      type: object
      required: [ok]
      properties:
        ok: {type: boolean}
`)
	write(t, filepath.Join(dir, "solo.yaml"), `name: solo
uses:
  - use: reviewer-task
`)

	// No resolver at all — the one-person install has nothing configured.
	defs, err := workflow.LoadDirWithTasks(dir, tasks, nil)
	if err != nil {
		t.Fatalf("a local-only workflow failed to load with nothing configured: %v", err)
	}
	if _, ok := defs["solo"]; !ok {
		t.Fatalf("the workflow did not load; got %v", keys(defs))
	}
	if n := len(defs["solo"].Steps); n != 1 {
		t.Fatalf("the local task did not expand: %d steps", n)
	}
}

// 9.2 — A SMALL ORG. They publish to a repository they already have, with the
// access control they already run, and consume it from ANOTHER MACHINE.
//
// "Another machine" is modelled as what actually differs between two machines:
// a separate cache, a separate bundle root, and a skills directory that does NOT
// contain the carried skill. If the second machine had the skill already, the
// test would prove nothing about the dependency travelling.
func TestASmallOrgConsumesItsOwnRepoFromAnotherMachine(t *testing.T) {
	remote, tag := publishToBareRepo(t)

	// The second machine. Its own everything.
	theirCache, theirRoots := t.TempDir(), t.TempDir()
	r, _ := newResolver(t, theirCache, theirRoots)

	ref, err := workflow.ParseRef("file://" + remote + "@" + tag)
	if err != nil {
		t.Fatal(err)
	}
	task, pin, err := r.ResolveRemoteUse(ref)
	if err != nil {
		t.Fatalf("the second machine could not consume the org's own repo: %v", err)
	}
	if task == nil || len(task.Steps) == 0 {
		t.Fatal("the resolved task carries no steps")
	}
	if pin.Commit == "" {
		t.Fatal("no commit was pinned, so a rerun could not be exact")
	}

	// The carried skill exists ONLY because the bundle brought it. Prove that by
	// finding it under the bundle-scoped root and nowhere else on this machine.
	roots := r.Roots()
	if len(roots) == 0 {
		t.Fatal("no bundle-scoped skill root was materialised")
	}
	carried := filepath.Join(roots[0], "carried-skill", "SKILL.md")
	if _, err := os.Stat(carried); err != nil {
		t.Fatalf("the carried skill did not travel: %v", err)
	}
	if !strings.HasPrefix(carried, theirRoots) {
		t.Fatalf("the skill landed outside this machine's bundle root: %s", carried)
	}
}

// 9.3 — AN ENTERPRISE. A non-GitHub remote, pinned by COMMIT, resolving with
// the remote UNREACHABLE from a warm cache.
//
// The task says "no network". Removing the network from a test is unreliable and
// proves less than this does: the remote is RENAMED OUT OF EXISTENCE between the
// warm fetch and the second resolution. A resolution that still succeeds cannot
// have reached the remote, because there is no longer a remote to reach — which
// is the air-gapped site's actual situation, and stronger than an unplugged
// cable.
//
// The reference is a bare commit SHA, because that is what an enterprise pins:
// a tag is a mutable pointer somebody with push access can move. The remote is a
// `file://` URL — a bare repository on a mounted path, which is exactly the form
// the scheme allowlist admits ON PURPOSE for this case. A bare absolute path is
// still refused, and correctly so: an operator naming a remote writes a URL.
func TestAnEnterprisePinsACommitAndResolvesWithTheRemoteGone(t *testing.T) {
	remote, tag := publishToBareRepo(t)

	cache, roots := t.TempDir(), t.TempDir()
	r, _ := newResolver(t, cache, roots)

	// Warm the cache through the tag, and learn the commit it resolved to.
	byTag, err := workflow.ParseRef("file://" + remote + "@" + tag)
	if err != nil {
		t.Fatal(err)
	}
	_, pin, err := r.ResolveRemoteUse(byTag)
	if err != nil {
		t.Fatal(err)
	}
	commit := pin.Commit
	if len(commit) != 40 {
		t.Fatalf("not a full commit: %q", commit)
	}

	// A pin by COMMIT, on a second resolver with the SAME cache — a restarted
	// process on the same host, which is what an enterprise actually does.
	byCommit, err := workflow.ParseRef("file://" + remote + "@" + commit)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ := newResolver(t, cache, t.TempDir())
	if _, _, err := fresh.ResolveRemoteUse(byCommit); err != nil {
		t.Fatalf("a commit-pinned reference did not resolve: %v", err)
	}

	// Now take the remote away entirely.
	if err := os.Rename(remote, remote+".gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(remote); !os.IsNotExist(err) {
		t.Fatal("the remote is still there; this test would prove nothing")
	}
	offline, _ := newResolver(t, cache, t.TempDir())
	task, pin2, err := offline.ResolveRemoteUse(byCommit)
	if err != nil {
		t.Fatalf("a warm cache did not resolve with the remote gone: %v", err)
	}
	if task == nil || len(task.Steps) == 0 {
		t.Fatal("the offline resolution produced no steps")
	}
	if pin2.Commit != commit {
		t.Fatalf("the offline resolution pinned a different commit: %s then %s", commit, pin2.Commit)
	}

	// And the counter-proof: a reference this cache has never seen must FAIL
	// with the remote gone. Otherwise the success above might be a resolver that
	// answers anything.
	unseen, err := workflow.ParseRef("file://" + remote + "@" + strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := offline.ResolveRemoteUse(unseen); err == nil {
		t.Fatal("a cold reference resolved with no remote — the cache is answering for content it does not hold")
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func keys(m map[string]*workflow.Definition) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

var _ = bundle.VersionsDir

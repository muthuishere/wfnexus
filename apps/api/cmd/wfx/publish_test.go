package main

import (
	"os"
	"os/exec"
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

// A bare repository in a temp dir: the whole `--to` path is verified against a
// real remote, with no github.com and no network.
func bareRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "registry.git")
	git(t, "", "git", "init", "--quiet", "--bare", dir)
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v: %s", args, err, out)
	}
	return string(out)
}

// gitIdentity keeps the commit the publish makes out of the machine's own
// config, so the test says nothing about who is running it.
func gitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "wfx test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "wfx test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
}

// publishHome points config.Load at empty roots, so a publish resolves against
// the test's own skills and nothing this machine happens to hold.
func publishHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("WFX_SKILLS_DIR", filepath.Join(dir, "skills"))
	t.Setenv("WFX_REGISTRIES", filepath.Join(dir, "registries.json"))
	t.Setenv("WFX_MCP_CONFIG", filepath.Join(dir, "mcp.json"))
	return dir
}

func workflowFile(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "w.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const plainWorkflow = "name: w\nsteps:\n  - id: one\n    prompt: hi\n    output_schema:\n      type: object\n"

// task 6.1/6.3/6.5 — `--to <git remote>` writes the tree, commits, tags and
// pushes, and it needs NO login and NO token: auth is git's.
func TestPublishToAGitRemoteNeedsNoLogin(t *testing.T) {
	tempContexts(t)
	gitIdentity(t)
	home := publishHome(t)
	remote := bareRepo(t)
	wf := workflowFile(t, home, plainWorkflow)

	if err := publish([]string{wf, "--version", "1.0.0", "--to", remote}); err != nil {
		t.Fatalf("publishing to a git remote: %v", err)
	}

	// The tree is on the remote, readable, under workflows/<name>/<version>/.
	files := git(t, "", "git", "--git-dir", remote, "ls-tree", "-r", "--name-only", "HEAD")
	for _, want := range []string{
		"workflows/w/1.0.0/manifest.json",
		"workflows/w/1.0.0/workflow.yaml",
	} {
		if !strings.Contains(files, want) {
			t.Fatalf("the remote does not carry %s; it carries:\n%s", want, files)
		}
	}
	if strings.Contains(files, ".tar") || strings.Contains(files, ".gz") {
		t.Fatalf("an archive was committed instead of a tree:\n%s", files)
	}
	if tags := git(t, "", "git", "--git-dir", remote, "tag"); !strings.Contains(tags, "w/v1.0.0") {
		t.Fatalf("the version tag is absent: %q", tags)
	}
	// Provenance is the commit: git's author, not a column of ours.
	if log := git(t, "", "git", "--git-dir", remote, "log", "-1", "--format=%an %s"); !strings.Contains(log, "publish w@1.0.0") {
		t.Fatalf("the commit does not name what it published: %q", log)
	}

	// A second version coexists with the first rather than replacing it.
	if err := publish([]string{workflowFile(t, home, plainWorkflow), "--version", "1.1.0", "--to", remote}); err != nil {
		t.Fatalf("publishing a second version: %v", err)
	}
	files = git(t, "", "git", "--git-dir", remote, "ls-tree", "-r", "--name-only", "HEAD")
	if !strings.Contains(files, "workflows/w/1.0.0/manifest.json") || !strings.Contains(files, "workflows/w/1.1.0/manifest.json") {
		t.Fatalf("the two versions do not coexist:\n%s", files)
	}
}

// task 6.4 — immutability is the REMOTE's: republishing a version is a push
// that fails, reported in the same vocabulary the server path uses for a
// duplicate version rather than as raw git output.
func TestRepublishingAVersionIsRefusedByTheRemote(t *testing.T) {
	tempContexts(t)
	gitIdentity(t)
	home := publishHome(t)
	remote := bareRepo(t)
	wf := workflowFile(t, home, plainWorkflow)
	if err := publish([]string{wf, "--version", "1.0.0", "--to", remote}); err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(git(t, "", "git", "--git-dir", remote, "rev-parse", "HEAD"))

	err := publish([]string{wf, "--version", "1.0.0", "--to", remote})
	if err == nil {
		t.Fatal("republishing an existing version succeeded")
	}
	if !strings.Contains(err.Error(), "immutable") || !strings.Contains(err.Error(), "1.0.0") {
		t.Fatalf("the refusal does not read as a duplicate version: %v", err)
	}
	if now := strings.TrimSpace(git(t, "", "git", "--git-dir", remote, "rev-parse", "HEAD")); now != head {
		t.Fatalf("a refused republish moved the remote: %s -> %s", head, now)
	}
}

// The tag is the remote's to refuse: with the version directory absent but the
// tag already taken, the push is rejected and nothing lands — the branch
// included, because the push is atomic.
func TestATagTheRemoteAlreadyCarriesRefusesThePublish(t *testing.T) {
	tempContexts(t)
	gitIdentity(t)
	home := publishHome(t)
	remote := bareRepo(t)
	wf := workflowFile(t, home, plainWorkflow)
	if err := publish([]string{wf, "--version", "1.0.0", "--to", remote}); err != nil {
		t.Fatal(err)
	}
	// Take the 2.0.0 tag without publishing 2.0.0's tree.
	git(t, "", "git", "--git-dir", remote, "tag", "w/v2.0.0", "HEAD")
	head := strings.TrimSpace(git(t, "", "git", "--git-dir", remote, "rev-parse", "HEAD"))

	err := publish([]string{wf, "--version", "2.0.0", "--to", remote})
	if err == nil {
		t.Fatal("publishing over an existing tag succeeded")
	}
	if !strings.Contains(err.Error(), "w/v2.0.0") || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("the refusal does not name the tag: %v", err)
	}
	if now := strings.TrimSpace(git(t, "", "git", "--git-dir", remote, "rev-parse", "HEAD")); now != head {
		t.Fatalf("a rejected tag still moved the branch: %s -> %s", head, now)
	}
	files := git(t, "", "git", "--git-dir", remote, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(files, "2.0.0") {
		t.Fatalf("a refused publish left a version on the remote:\n%s", files)
	}
}

// task 6.2 — every existing refusal runs BEFORE a commit is made, so a refused
// publish leaves the remote with no commit, no tag and no push.
func TestARefusedPublishToAGitRemoteTouchesTheRemote(t *testing.T) {
	tempContexts(t)
	gitIdentity(t)
	home := publishHome(t)
	remote := bareRepo(t)
	wf := workflowFile(t, home, "name: w\nsteps:\n  - id: one\n    prompt: hi\n    output_schema:\n      type: object\n    skills: [absent-skill]\n")

	err := publish([]string{wf, "--version", "1.0.0", "--to", remote})
	if err == nil {
		t.Fatal("a workflow naming an unresolvable skill published")
	}
	if !strings.Contains(err.Error(), "absent-skill") || !strings.Contains(err.Error(), "roots searched") {
		t.Fatalf("the refusal does not name the reference and where it looked: %v", err)
	}
	if out := git(t, "", "git", "--git-dir", remote, "tag"); strings.TrimSpace(out) != "" {
		t.Fatalf("a refused publish left a tag: %q", out)
	}
	if err := exec.Command("git", "--git-dir", remote, "rev-parse", "--verify", "HEAD").Run(); err == nil {
		t.Fatal("a refused publish left a commit on the remote")
	}
}

// [SEC-TEST] task 6.2 — the attack is a pasted API key travelling into a public
// git history, where deleting it is a history rewrite rather than an edit. The
// literal-credential refusal runs before the clone, so the value never reaches
// a commit, a working file or the error message.
func TestAPastedCredentialNeverReachesACommit(t *testing.T) {
	tempContexts(t)
	gitIdentity(t)
	home := publishHome(t)
	remote := bareRepo(t)
	const pasted = "sk-proj-AAAABBBBCCCCDDDDEEEEFFFF/1234+5678=="
	wf := workflowFile(t, home, "name: w\nsteps:\n  - id: one\n    prompt: hi\n    output_schema:\n      type: object\n    env:\n      \""+pasted+"\": x\n")

	err := publish([]string{wf, "--version", "1.0.0", "--to", remote})
	if err == nil {
		t.Fatal("a workflow with a pasted credential published")
	}
	if strings.Contains(err.Error(), pasted) {
		t.Fatalf("the refusal echoed the value: %v", err)
	}
	if err := exec.Command("git", "--git-dir", remote, "rev-parse", "--verify", "HEAD").Run(); err == nil {
		t.Fatal("a refused publish left a commit on the remote")
	}
}

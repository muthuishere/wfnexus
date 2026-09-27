package proposal

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const wfRel = ".wfx/workflows"

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// soloRepo is a repository with one committed workflow and no remote.
func soloRepo(t *testing.T) Repo {
	t.Helper()
	root := t.TempDir()
	mustGit(t, root, "init", "-q", "-b", "main")
	mustGit(t, root, "config", "commit.gpgsign", "false")
	wf := filepath.Join(root, wfRel)
	if err := os.MkdirAll(wf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "hello.yaml"), []byte("name: hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, root, "add", "-A")
	mustGit(t, root, "commit", "-q", "-m", "init")
	r, ok := Detect(context.Background(), wf)
	if !ok {
		t.Fatal("a git repository was not detected")
	}
	return r
}

func writeHello(body string) func(string) error {
	return func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "hello.yaml"), []byte(body), 0o644)
	}
}

func TestAPlainDirectoryIsNotAGitProject(t *testing.T) {
	if _, ok := Detect(context.Background(), t.TempDir()); ok {
		t.Fatal("a plain directory was treated as a git repository")
	}
}

func TestOpeningAProposalLeavesTheCheckoutUntouchedAndCommitsOnABranch(t *testing.T) {
	r := soloRepo(t)
	ctx := context.Background()
	o, err := r.Open(ctx, Change{Workflow: "hello", Kind: KindEdit, Message: "wfx: edit workflow hello",
		WorkDir: t.TempDir(), Apply: writeHello("name: hello\ndescription: changed\n")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(o.Branch, "wfx/edit-hello-") || o.Base != "main" || o.PRURL != "" {
		t.Fatalf("opened = %+v", o)
	}
	got, _ := os.ReadFile(filepath.Join(r.Root, wfRel, "hello.yaml"))
	if string(got) != "name: hello\n" {
		t.Fatalf("the tracked checkout changed before approval: %q", got)
	}
	if author := mustGit(t, r.Root, "log", "-1", "--format=%an", o.Branch); author != AuthorName {
		t.Fatalf("the proposal commit is by %q", author)
	}
	diff, err := r.Diff(ctx, o.Base, o.Branch)
	if err != nil || !strings.Contains(diff, "+description: changed") {
		t.Fatalf("diff = %q, %v", diff, err)
	}
	if wts := mustGit(t, r.Root, "worktree", "list"); strings.Count(wts, "\n") != 0 {
		t.Fatalf("the proposal worktree was left behind:\n%s", wts)
	}
}

func TestApprovingASoloProposalMergesItIntoTheCheckout(t *testing.T) {
	r := soloRepo(t)
	ctx := context.Background()
	o, err := r.Open(ctx, Change{Workflow: "hello", Kind: KindEdit, Message: "m",
		WorkDir: t.TempDir(), Apply: writeHello("name: hello\ndescription: merged\n")})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Merge(ctx, o.Base, o.Branch, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(r.Root, wfRel, "hello.yaml"))
	if !strings.Contains(string(got), "merged") {
		t.Fatalf("the checkout did not get the change: %q", got)
	}
	if r.branchExists(ctx, o.Branch) {
		t.Fatal("the merged branch was kept")
	}
}

func TestRejectingAProposalDeletesItsBranch(t *testing.T) {
	r := soloRepo(t)
	ctx := context.Background()
	o, err := r.Open(ctx, Change{Workflow: "hello", Kind: KindDelete, Message: "m",
		WorkDir: t.TempDir(), Apply: func(dir string) error { return os.Remove(filepath.Join(dir, "hello.yaml")) }})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(ctx, o.Branch, "", "no"); err != nil {
		t.Fatal(err)
	}
	if r.branchExists(ctx, o.Branch) {
		t.Fatal("the rejected branch survived")
	}
	if _, err := os.Stat(filepath.Join(r.Root, wfRel, "hello.yaml")); err != nil {
		t.Fatal("a rejected delete removed the workflow")
	}
}

func TestAChangeThatChangesNothingIsNotAProposal(t *testing.T) {
	r := soloRepo(t)
	_, err := r.Open(context.Background(), Change{Workflow: "hello", Kind: KindEdit, Message: "m",
		WorkDir: t.TempDir(), Apply: writeHello("name: hello\n")})
	if err != ErrNoChange {
		t.Fatalf("err = %v, want ErrNoChange", err)
	}
	if out := mustGit(t, r.Root, "branch", "--list", "wfx/*"); out != "" {
		t.Fatalf("an empty proposal left a branch: %s", out)
	}
}

func TestDriftListsUntrackedAndModifiedWorkflowsAndBecomesAProposal(t *testing.T) {
	r := soloRepo(t)
	ctx := context.Background()
	wf := filepath.Join(r.Root, wfRel)
	if err := os.MkdirAll(filepath.Join(wf, "fresh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "fresh", "workflow.yaml"), []byte("name: fresh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "hello.yaml"), []byte("name: hello\nx: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ds, err := r.Drift(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 || ds[0].Workflow != "fresh" || ds[0].Kind != KindCreate || ds[1].Workflow != "hello" || ds[1].Kind != KindEdit {
		t.Fatalf("drift = %+v", ds)
	}
	o, err := r.Open(ctx, Change{Workflow: "fresh", Kind: KindCreate, Message: "m", WorkDir: t.TempDir(), Apply: r.CopyDrift(ds[0])})
	if err != nil {
		t.Fatal(err)
	}
	// The drifted files are exactly what the branch carries, so approving
	// does not trip over them.
	if err := r.Merge(ctx, o.Base, o.Branch, ""); err != nil {
		t.Fatal(err)
	}
	ds, _ = r.Drift(ctx)
	if len(ds) != 1 || ds[0].Workflow != "hello" {
		t.Fatalf("after merging the drift proposal, drift = %+v", ds)
	}
}

// With a remote the branch is pushed and a PR opened through gh; approve and
// reject go through gh too.
func TestARepositoryWithARemoteGetsAPushedBranchAndAPullRequest(t *testing.T) {
	r := soloRepo(t)
	ctx := context.Background()
	bare := t.TempDir()
	mustGit(t, bare, "init", "-q", "--bare")
	mustGit(t, r.Root, "remote", "add", "origin", bare)
	mustGit(t, r.Root, "push", "-q", "origin", "main")

	var ghCalls [][]string
	prev := Run
	t.Cleanup(func() { Run = prev })
	Run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
		if name == "gh" {
			ghCalls = append(ghCalls, args)
			if args[0] == "pr" && args[1] == "create" {
				return "https://github.com/o/r/pull/7", nil
			}
			return "", nil
		}
		return prev(ctx, dir, name, args...)
	}
	o, err := r.Open(ctx, Change{Workflow: "hello", Kind: KindEdit, Message: "wfx: edit", WorkDir: t.TempDir(),
		Apply: writeHello("name: hello\ny: 2\n")})
	if err != nil {
		t.Fatal(err)
	}
	if o.PRURL != "https://github.com/o/r/pull/7" || o.Note != "" {
		t.Fatalf("opened = %+v", o)
	}
	if out := mustGit(t, bare, "branch", "--list", o.Branch); out == "" {
		t.Fatal("the proposal branch was not pushed")
	}
	if err := r.Close(ctx, o.Branch, o.PRURL, "nope"); err != nil {
		t.Fatal(err)
	}
	last := ghCalls[len(ghCalls)-1]
	if strings.Join(last[:3], " ") != "pr close https://github.com/o/r/pull/7" {
		t.Fatalf("reject did not close the PR: %v", ghCalls)
	}
}

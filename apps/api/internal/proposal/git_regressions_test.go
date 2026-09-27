package proposal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func assertClean(t *testing.T, r Repo, workDir string) {
	t.Helper()
	if out := mustGit(t, r.Root, "branch", "--list", "wfx/*"); out != "" {
		t.Fatalf("branch left: %s", out)
	}
	if out := mustGit(t, r.Root, "worktree", "list"); strings.Count(out, "\n") != 0 {
		t.Fatalf("worktree left: %s", out)
	}
	entries, _ := os.ReadDir(workDir)
	if len(entries) != 0 {
		t.Fatalf("temp left: %v", entries)
	}
	got, _ := os.ReadFile(filepath.Join(r.Root, wfRel, "hello.yaml"))
	if string(got) != "name: hello\n" {
		t.Fatalf("checkout changed: %q", got)
	}
}

func TestOpenApplyAddAndCommitFailuresCleanAllProposalState(t *testing.T) {
	for _, tc := range []struct {
		name, fail string
		want       error
	}{
		{"apply", "", errors.New("apply failed")}, {"add", "add", errors.New("add failed")}, {"commit", "commit", errors.New("commit failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := soloRepo(t)
			workDir := t.TempDir()
			prev := Run
			t.Cleanup(func() { Run = prev })
			Run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
				if tc.fail != "" && name == "git" && containsArg(args, tc.fail) {
					return "", tc.want
				}
				return prev(ctx, dir, name, args...)
			}
			apply := func(string) error { return tc.want }
			if tc.fail != "" {
				apply = writeHello("name: changed\n")
			}
			_, err := r.Open(context.Background(), Change{Workflow: "hello", Message: "m", WorkDir: workDir, Apply: apply})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v", err)
			}
			assertClean(t, r, workDir)
		})
	}
}

func TestOpenPushFailureKeepsReviewableLocalProposalAndOriginalCheckout(t *testing.T) {
	r := soloRepo(t)
	bare := t.TempDir()
	mustGit(t, bare, "init", "-q", "--bare")
	mustGit(t, r.Root, "remote", "add", "origin", bare)
	mustGit(t, r.Root, "push", "-q", "origin", "main")
	prev := Run
	t.Cleanup(func() { Run = prev })
	Run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
		if name == "git" && len(args) > 1 && args[0] == "push" {
			return "", errors.New("network down")
		}
		return prev(ctx, dir, name, args...)
	}
	o, err := r.Open(context.Background(), Change{Workflow: "hello", Message: "m", WorkDir: t.TempDir(), Apply: writeHello("name: local\n")})
	if err != nil || o.Note == "" || !r.branchExists(context.Background(), o.Branch) {
		t.Fatalf("opened=%+v err=%v", o, err)
	}
	got, _ := os.ReadFile(filepath.Join(r.Root, wfRel, "hello.yaml"))
	if string(got) != "name: hello\n" {
		t.Fatalf("checkout changed: %q", got)
	}
}

func TestPRApprovalMergesFastForwardsCleansMatchingAndDeletesLocalBranch(t *testing.T) {
	r := soloRepo(t)
	ctx := context.Background()
	bare := t.TempDir()
	mustGit(t, bare, "init", "-q", "--bare")
	mustGit(t, r.Root, "remote", "add", "origin", bare)
	mustGit(t, r.Root, "push", "-q", "-u", "origin", "main")
	prev := Run
	t.Cleanup(func() { Run = prev })
	var merge []string
	var proposalBranch string
	Run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
		if name == "gh" && len(args) > 1 && args[0] == "pr" && args[1] == "merge" {
			merge = append([]string(nil), args...)
			if _, err := prev(ctx, dir, "git", "push", "origin", proposalBranch+":main"); err != nil {
				return "", err
			}
			return "", nil
		}
		if name == "git" && len(args) > 1 && args[0] == "push" {
			return "", nil
		}
		return prev(ctx, dir, name, args...)
	}
	o, err := r.Open(ctx, Change{Workflow: "hello", Message: "m", WorkDir: t.TempDir(), Apply: writeHello("name: approved\n")})
	if err != nil {
		t.Fatal(err)
	}
	proposalBranch = o.Branch
	if err = os.WriteFile(filepath.Join(r.Root, wfRel, "hello.yaml"), []byte("name: approved\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = r.Merge(ctx, o.Base, o.Branch, "https://example.invalid/pr/1"); err != nil {
		t.Fatal(err)
	}
	want := "pr merge https://example.invalid/pr/1 --merge --delete-branch"
	if strings.Join(merge, " ") != want {
		t.Fatalf("gh args=%v", merge)
	}
	got, _ := os.ReadFile(filepath.Join(r.Root, wfRel, "hello.yaml"))
	if string(got) != "name: approved\n" || r.branchExists(ctx, o.Branch) {
		t.Fatalf("got=%q branch=%v", got, r.branchExists(ctx, o.Branch))
	}
}

func TestMergeRejectsWrongCurrentBranchWithoutChangingCheckout(t *testing.T) {
	r := soloRepo(t)
	mustGit(t, r.Root, "checkout", "-q", "-b", "other")
	err := r.Merge(context.Background(), "main", "missing", "")
	if err == nil || !strings.Contains(err.Error(), `checkout is on "other"`) {
		t.Fatalf("err=%v", err)
	}
	if mustGit(t, r.Root, "branch", "--show-current") != "other" {
		t.Fatal("checkout moved")
	}
}

func TestMergeFailureAbortsConflictedMerge(t *testing.T) {
	r := soloRepo(t)
	mustGit(t, r.Root, "checkout", "-q", "-b", "conflicting")
	if err := os.WriteFile(filepath.Join(r.Root, wfRel, "hello.yaml"), []byte("branch\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, r.Root, "commit", "-aqm", "branch")
	mustGit(t, r.Root, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(r.Root, wfRel, "hello.yaml"), []byte("main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, r.Root, "commit", "-aqm", "main")
	if err := r.Merge(context.Background(), "main", "conflicting", ""); err == nil {
		t.Fatal("merge succeeded")
	}
	if _, err := os.Stat(filepath.Join(r.Root, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("merge not aborted: %v", err)
	}
}

func TestDriftClassifiesRenameMixedHiddenDeleteAndRoot(t *testing.T) {
	r := soloRepo(t)
	ctx := context.Background()
	wf := filepath.Join(r.Root, wfRel)
	if err := os.WriteFile(filepath.Join(wf, ".hidden.yaml"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "mixed.yaml"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, r.Root, "add", ".wfx/workflows/mixed.yaml")
	mustGit(t, r.Root, "commit", "-qm", "mixed base")
	if err := os.WriteFile(filepath.Join(wf, "mixed.yaml"), []byte("x: 2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wf, "gone.yaml"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, r.Root, "add", ".wfx/workflows/gone.yaml")
	mustGit(t, r.Root, "commit", "-qm", "gone base")
	mustGit(t, r.Root, "rm", "-q", ".wfx/workflows/gone.yaml")
	mustGit(t, r.Root, "mv", filepath.Join(wfRel, "hello.yaml"), filepath.Join(wfRel, "renamed.yaml"))
	ds, err := r.Drift(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Drift{}
	for _, d := range ds {
		got[d.Workflow] = d
	}
	if got["renamed"].Kind != KindEdit || got["mixed"].Kind != KindEdit || got["gone"].Kind != KindDelete {
		t.Fatalf("drift=%+v", ds)
	}
	if _, ok := got[".hidden"]; ok {
		t.Fatalf("hidden drift=%+v", ds)
	}
	r.Root = t.TempDir()
	mustGit(t, r.Root, "init", "-q", "-b", "main")
	r.Rel = "."
	if err := os.WriteFile(filepath.Join(r.Root, "top.yaml"), []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ds, err = r.Drift(ctx)
	if err != nil || len(ds) != 1 || ds[0].Workflow != "top" || ds[0].Kind != KindCreate {
		t.Fatalf("root drift=%+v err=%v", ds, err)
	}
}

func TestDetectRejectsUnbornAndEscapesButAllowsMissingAndRejectsDetachedHEAD(t *testing.T) {
	ctx := context.Background()
	unborn := t.TempDir()
	mustGit(t, unborn, "init", "-q", "-b", "main")
	if _, ok := Detect(ctx, filepath.Join(unborn, "workflows")); ok {
		t.Fatal("unborn detected")
	}
	r := soloRepo(t)
	if _, ok := Detect(ctx, filepath.Join(r.Root, "missing", "workflows")); !ok {
		t.Fatal("missing first directory rejected")
	}
	outside := t.TempDir()
	if _, ok := Detect(ctx, outside); ok {
		t.Fatal("outside detected")
	}
	link := filepath.Join(r.Root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	if _, ok := Detect(ctx, filepath.Join(link, "workflows")); ok {
		t.Fatal("symlink escape detected")
	}
	mustGit(t, r.Root, "checkout", "-q", "--detach", "HEAD")
	if _, err := r.CurrentBranch(ctx); err == nil || !strings.Contains(err.Error(), "detached HEAD") {
		t.Fatalf("err=%v", err)
	}
}

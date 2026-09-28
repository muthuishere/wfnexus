package engine

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/proposal"
)

// Found rehearsing the deploy skill: `wfx project new` ran `git init` and made
// no commit, and a repository with no commit is not one a proposal can branch
// from, so the FIRST workflow saved into a new project skipped review and was
// written straight to disk. A new project now has a root commit.
func TestANewProjectIsGitNativeFromItsFirstSave(t *testing.T) {
	eng, _ := copyEngine(t)
	ctx := context.Background()
	src, err := eng.CreateProject(ctx, "fresh", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := proposal.Detect(ctx, src.Dir); !ok {
		t.Fatal("a new project is not git-backed with a commit, so its first save would not be a proposal")
	}
	tgt, err := eng.TargetForSave(ctx, "fresh", "first")
	if err != nil {
		t.Fatal(err)
	}
	if !tgt.Git {
		t.Fatal("a save into the new project does not target git")
	}
}

// An existing folder that already has history keeps it: the root commit is
// only for a repository with none.
func TestAnExistingHistoryIsNotTouched(t *testing.T) {
	eng, _ := copyEngine(t)
	folder := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "theirs"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", folder}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if _, err := eng.CreateProject(context.Background(), "mine", folder); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("git", "-C", folder, "log", "--format=%s").Output()
	if got := strings.TrimSpace(string(out)); got != "theirs" {
		t.Fatalf("the existing history was changed: %q", got)
	}
}

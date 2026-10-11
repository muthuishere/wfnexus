package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func runDirEngine(t *testing.T) (*Engine, *store.Store, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wfnexus.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	work := t.TempDir()
	def := &workflow.Definition{Name: "gen", Steps: []workflow.Step{{ID: "map", Prompt: "x"}}}
	return New(config.Config{WorkDir: work}, st, nil, map[string]*workflow.Definition{"gen": def}, skills.Load(), nil), st, work
}

func runDir(t *testing.T, work string, id uuid.UUID, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(work, id.String())
	if err := os.MkdirAll(filepath.Join(dir, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-age)
	_ = os.Chtimes(dir, old, old)
	return dir
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// A run's ~60 MB checkout used to stay forever (57 GB by 2026-10-11). It goes
// when the run ends done or cancelled; a failed run keeps it for Retry.
func TestAFinishedRunLosesItsCheckout(t *testing.T) {
	ctx := context.Background()
	e, st, work := runDirEngine(t)
	for status, kept := range map[string]bool{"done": false, "cancelled": false, "failed": true, "awaiting_approval": true} {
		r, _ := st.CreateRun(ctx, "local", "gen", []byte(`{}`))
		_ = st.UpdateRun(ctx, r.ID, status, "", "")
		dir := runDir(t, work, r.ID, 0)
		e.cleanupFinished(r.ID)
		if exists(dir) != kept {
			t.Errorf("%s run: directory kept=%v, want %v", status, exists(dir), kept)
		}
	}
}

// At boot, run directories older than a day go unless the run is still live;
// newer ones, unknown runs and everything that is not a UUID-named run
// directory (sources/, projects/, sources.json, ...) stay.
func TestTheBootSweepRemovesOnlyOldDirsOfOverRuns(t *testing.T) {
	ctx := context.Background()
	e, st, work := runDirEngine(t)
	mk := func(status string, age time.Duration) string {
		r, _ := st.CreateRun(ctx, "local", "gen", []byte(`{}`))
		_ = st.UpdateRun(ctx, r.ID, status, "", "")
		return runDir(t, work, r.ID, age)
	}
	oldFailed := mk("failed", 48*time.Hour)
	oldDone := mk("done", 48*time.Hour)
	oldPaused := mk("awaiting_approval", 48*time.Hour)
	newFailed := mk("failed", time.Hour)
	unknown := runDir(t, work, uuid.New(), 48*time.Hour)
	// Everything at the top of WorkDir that is not a run: the per-project base
	// clones (sources/ — deleted by hand on 2026-10-11, which broke every
	// project run), other state dirs, and the registries. All old, all kept.
	old := time.Now().Add(-48 * time.Hour)
	keep := []string{}
	for _, d := range []string{"sources/reqsume-gh/.git", "projects/p", "proposals", "skill-sources/s"} {
		_ = os.MkdirAll(filepath.Join(work, d), 0o755)
		top := filepath.Join(work, strings.Split(d, "/")[0])
		_ = os.Chtimes(top, old, old)
		keep = append(keep, top, filepath.Join(work, d))
	}
	for _, f := range []string{"sources.json", "projects.json"} {
		p := filepath.Join(work, f)
		_ = os.WriteFile(p, []byte("[]"), 0o644)
		_ = os.Chtimes(p, old, old)
		keep = append(keep, p)
	}
	// a file named like a run is not a run directory either
	stray := filepath.Join(work, uuid.New().String())
	_ = os.WriteFile(stray, nil, 0o644)
	_ = os.Chtimes(stray, old, old)
	keep = append(keep, stray)

	if n := e.SweepRunDirs(ctx); n != 2 {
		t.Fatalf("swept %d, want 2", n)
	}
	for p, want := range map[string]bool{oldFailed: false, oldDone: false, oldPaused: true, newFailed: true, unknown: true} {
		if exists(p) != want {
			t.Errorf("%s: exists=%v, want %v", filepath.Base(p), exists(p), want)
		}
	}
	for _, p := range keep {
		if !exists(p) {
			t.Errorf("the sweep removed %s, which is not a run directory", strings.TrimPrefix(p, work+"/"))
		}
	}
}

// Removing a worktree also detaches it, so the parent repo lists no dead entry.
func TestRemovingARunWorktreeDetachesIt(t *testing.T) {
	e, _, work := runDirEngine(t)
	repo := t.TempDir()
	git := func(dir string, args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	git(repo, "init", "-q")
	git(repo, "commit", "-q", "--allow-empty", "-m", "x")
	id := uuid.New()
	wt := filepath.Join(work, id.String(), "worktree")
	git(repo, "worktree", "add", "-q", "--detach", wt, "HEAD")

	e.removeRunDir(id)
	if exists(filepath.Join(work, id.String())) {
		t.Fatal("run directory still there")
	}
	if l := git(repo, "worktree", "list"); strings.Count(l, "\n") != 1 {
		t.Fatalf("parent still lists the worktree:\n%s", l)
	}
}

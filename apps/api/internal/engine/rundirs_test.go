package engine

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
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

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}

func artifactEntries(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A run's ~60 MB checkout used to stay forever (57 GB by 2026-10-11). It goes
// when the run ends — done, cancelled or failed — leaving an artifact; a live
// run keeps it.
func TestAFinishedRunLosesItsCheckout(t *testing.T) {
	ctx := context.Background()
	e, st, work := runDirEngine(t)
	for status, kept := range map[string]bool{"done": false, "cancelled": false, "failed": false, "awaiting_approval": true, "needs_input": true} {
		r, _ := st.CreateRun(ctx, "local", "gen", []byte(`{}`))
		_ = st.UpdateRun(ctx, r.ID, status, "", "")
		dir := runDir(t, work, r.ID, 0)
		e.cleanupFinished(r.ID)
		if exists(dir) != kept {
			t.Errorf("%s run: directory kept=%v, want %v", status, exists(dir), kept)
		}
		if !kept && !exists(e.artifactPath(r.ID)) {
			t.Errorf("%s run: no artifact written", status)
		}
	}
}

// The sweep archives and removes every over run's directory (past a short
// guard), and leaves live runs, unknown runs and everything that is not a
// UUID-named run directory (sources/, projects/, artifacts/, sources.json, ...).
func TestTheSweepRemovesOnlyDirsOfOverRuns(t *testing.T) {
	ctx := context.Background()
	e, st, work := runDirEngine(t)
	mk := func(status string, age time.Duration) string {
		r, _ := st.CreateRun(ctx, "local", "gen", []byte(`{}`))
		_ = st.UpdateRun(ctx, r.ID, status, "", "")
		return runDir(t, work, r.ID, age)
	}
	oldFailed := mk("failed", 4*24*time.Hour)
	oldDone := mk("done", 48*time.Hour)
	oldPaused := mk("awaiting_approval", 48*time.Hour)
	oldRunning := mk("running", 48*time.Hour)
	justFailed := mk("failed", time.Minute)
	unknown := runDir(t, work, uuid.New(), 48*time.Hour)
	// Everything at the top of WorkDir that is not a run: the per-project base
	// clones (sources/ — deleted by hand on 2026-10-11, which broke every
	// project run), other state dirs, and the registries. All old, all kept.
	old := time.Now().Add(-48 * time.Hour)
	keep := []string{}
	for _, d := range []string{"sources/reqsume-gh/.git", "projects/p", "proposals", "skill-sources/s", "artifacts/notes"} {
		_ = os.MkdirAll(filepath.Join(work, d), 0o755)
		top := filepath.Join(work, strings.Split(d, "/")[0])
		_ = os.Chtimes(top, old, old)
		keep = append(keep, top, filepath.Join(work, d))
	}
	for _, f := range []string{"sources.json", "projects.json", "artifacts/readme.txt"} {
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
	for p, want := range map[string]bool{oldFailed: false, oldDone: false, oldPaused: true, oldRunning: true, justFailed: true, unknown: true} {
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
	gitT(t, repo, "init", "-q")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "x")
	id := uuid.New()
	wt := filepath.Join(work, id.String(), "worktree")
	gitT(t, repo, "worktree", "add", "-q", "--detach", wt, "HEAD")

	e.removeRunDir(id)
	if exists(filepath.Join(work, id.String())) {
		t.Fatal("run directory still there")
	}
	if l := gitT(t, repo, "worktree", "list"); strings.Count(l, "\n") != 1 {
		t.Fatalf("parent still lists the worktree:\n%s", l)
	}
}

// A failed run's worktree becomes an artifact (its changes, not its checkout),
// its directory goes, a nested worktree a step cut inside it is detached from
// the parent repo, and Retry rebuilds the workspace at the base with the
// patch applied and the untracked files back.
func TestAnEndedRunIsArchivedAndRetryRestoresIt(t *testing.T) {
	ctx := context.Background()
	e, st, work := runDirEngine(t)
	repo := t.TempDir()
	gitT(t, repo, "init", "-q")
	_ = os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)
	_ = os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("ignored.log\n"), 0o644)
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(gitT(t, repo, "rev-parse", "HEAD"))

	r, _ := st.CreateRun(ctx, "local", "gen", []byte(`{}`))
	input := map[string]any{"repo_path": repo}
	wt, err := e.prepareWorkspace(ctx, r.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.SetBaseRef(ctx, r.ID, base)
	// the run's work: a tracked edit (one staged, one not), untracked files,
	// an ignored file, a big file, node_modules, and a nested worktree
	_ = os.WriteFile(filepath.Join(wt, "a.txt"), []byte("one\ntwo\n"), 0o644)
	_ = os.WriteFile(filepath.Join(wt, ".gitignore"), []byte("ignored.log\n*.tmp\n"), 0o644)
	gitT(t, wt, "add", ".gitignore")
	_ = os.MkdirAll(filepath.Join(wt, "out"), 0o755)
	_ = os.WriteFile(filepath.Join(wt, "out", "signals.json"), []byte(`{"ok":true}`), 0o644)
	_ = os.WriteFile(filepath.Join(wt, "collect.sh"), []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile(filepath.Join(wt, "ignored.log"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(wt, "big.bin"), bytes.Repeat([]byte("x"), artifactMaxFile+1), 0o644)
	_ = os.MkdirAll(filepath.Join(wt, "node_modules", "m"), 0o755)
	_ = os.WriteFile(filepath.Join(wt, "node_modules", "m", "i.js"), []byte("x"), 0o644)
	nested := filepath.Join(wt, "fixes", "f1")
	gitT(t, repo, "worktree", "add", "-q", "-b", "fix-1", nested, "HEAD")

	_ = st.UpdateRun(ctx, r.ID, "failed", "", "boom")
	e.cleanupFinished(r.ID)
	if exists(filepath.Join(work, r.ID.String())) {
		t.Fatal("run directory still there")
	}
	if l := gitT(t, repo, "worktree", "list"); strings.Count(l, "\n") != 1 {
		t.Fatalf("parent still lists a worktree of the run:\n%s", l)
	}

	ents := artifactEntries(t, e.artifactPath(r.ID))
	for name := range ents {
		if strings.Contains(name, ".git/") || strings.Contains(name, "node_modules") || name == "files/ignored.log" || name == "files/big.bin" {
			t.Errorf("artifact holds %s", name)
		}
	}
	for _, want := range []string{"manifest.json", "changes.patch", "files/out/signals.json", "files/collect.sh", "files/a.txt"} {
		if _, ok := ents[want]; !ok {
			t.Errorf("artifact lacks %s (has %v)", want, keys(ents))
		}
	}
	var m artifactManifest
	if err := json.Unmarshal([]byte(ents["manifest.json"]), &m); err != nil {
		t.Fatal(err)
	}
	if m.BaseSHA != base || m.RunID != r.ID.String() || !m.Git || m.Layout != "worktree" {
		t.Errorf("manifest = %+v", m)
	}
	if len(m.Skipped) != 1 || m.Skipped[0].Path != "big.bin" {
		t.Errorf("skipped = %+v, want big.bin", m.Skipped)
	}
	if !strings.Contains(ents["changes.patch"], "+two") || !strings.Contains(ents["changes.patch"], "+*.tmp") {
		t.Errorf("patch misses the edits:\n%s", ents["changes.patch"])
	}

	// the base moves on in the parent; Retry must still restore at the old base
	_ = os.WriteFile(filepath.Join(repo, "later.txt"), []byte("later"), 0o644)
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "later")

	wt2, err := e.prepareWorkspace(ctx, r.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitT(t, wt2, "rev-parse", "HEAD")); got != base {
		t.Errorf("restored at %s, want base %s", got, base)
	}
	for f, want := range map[string]string{"a.txt": "one\ntwo\n", ".gitignore": "ignored.log\n*.tmp\n", "out/signals.json": `{"ok":true}`, "collect.sh": "#!/bin/sh\n"} {
		b, err := os.ReadFile(filepath.Join(wt2, f))
		if err != nil || string(b) != want {
			t.Errorf("%s = %q (%v), want %q", f, b, err, want)
		}
	}
	if exists(filepath.Join(wt2, "later.txt")) {
		t.Error("restore used the moved base")
	}
	// the patch alone reproduces the tracked edits at the base
	gitT(t, wt2, "reset", "-q", "--hard", base)
	cmd := exec.Command("git", "-C", wt2, "apply", "--check", "-")
	cmd.Stdin = strings.NewReader(ents["changes.patch"])
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("changes.patch does not apply at base: %v %s", err, out)
	}
}

// A retry whose artifact was pruned starts from a fresh checkout.
func TestRetryWithoutAnArtifactStartsFresh(t *testing.T) {
	ctx := context.Background()
	e, st, _ := runDirEngine(t)
	repo := t.TempDir()
	gitT(t, repo, "init", "-q")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "x")
	r, _ := st.CreateRun(ctx, "local", "gen", []byte(`{}`))
	_ = st.SetBaseRef(ctx, r.ID, strings.TrimSpace(gitT(t, repo, "rev-parse", "HEAD")))
	wt, err := e.prepareWorkspace(ctx, r.ID, map[string]any{"repo_path": repo})
	if err != nil || !exists(filepath.Join(wt, ".git")) {
		t.Fatalf("no fresh worktree: %v", err)
	}
}

// A non-git workspace (repo/) is tarred whole, minus node_modules; the run
// dir's secrets/ never goes in.
func TestANonGitWorkspaceIsTarredWhole(t *testing.T) {
	ctx := context.Background()
	e, st, work := runDirEngine(t)
	r, _ := st.CreateRun(ctx, "local", "gen", []byte(`{}`))
	dir := runDir(t, work, r.ID, 0)
	_ = os.WriteFile(filepath.Join(dir, "repo", "report.md"), []byte("hi"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "repo", "node_modules"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "repo", "node_modules", "x"), []byte("x"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "secrets"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "secrets", "s.env"), []byte("export K=v"), 0o600)
	_ = st.UpdateRun(ctx, r.ID, "done", "", "")
	e.cleanupFinished(r.ID)
	ents := artifactEntries(t, e.artifactPath(r.ID))
	if ents["files/report.md"] != "hi" || len(ents) != 2 {
		t.Errorf("artifact = %v, want manifest.json + files/report.md", keys(ents))
	}
	// and Retry puts it back
	if _, err := e.prepareWorkspace(ctx, r.ID, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "repo", "report.md")); string(b) != "hi" {
		t.Errorf("report.md not restored: %q", b)
	}
}

// Only the newest RunArtifactsKeep artifacts stay, and the sweep repeats on a
// timer, not only at boot, so a long-lived server stays bounded.
func TestTheArtifactCapAndTheSweepRepeats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, st, work := runDirEngine(t)
	e.cfg.RunArtifactsKeep = 2
	_ = os.MkdirAll(filepath.Join(work, artifactsDir), 0o755)
	var ids []uuid.UUID
	for i := 0; i < 4; i++ {
		id := uuid.New()
		ids = append(ids, id)
		p := e.artifactPath(id)
		_ = os.WriteFile(p, []byte("x"), 0o644)
		at := time.Now().Add(-time.Duration(10-i) * time.Hour)
		_ = os.Chtimes(p, at, at)
	}
	other := filepath.Join(work, artifactsDir, "not-a-run.tar.gz")
	_ = os.WriteFile(other, nil, 0o644)
	e.pruneArtifacts()
	for i, id := range ids {
		if want := i >= 2; exists(e.artifactPath(id)) != want {
			t.Errorf("artifact %d: exists=%v, want %v", i, !want, want)
		}
	}
	if !exists(other) {
		t.Error("pruned a file that is not a run artifact")
	}

	// the periodic sweeper, on a short interval
	r2, _ := st.CreateRun(ctx, "local", "gen", []byte(`{}`))
	_ = st.UpdateRun(ctx, r2.ID, "done", "", "")
	dir2 := runDir(t, work, r2.ID, 48*time.Hour)
	e.startRunDirSweeper(ctx, 10*time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for exists(dir2) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if exists(dir2) {
		t.Fatal("the periodic sweeper never removed an old done run's directory")
	}
}

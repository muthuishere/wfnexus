package engine

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// A run's checkout (WorkDir/<runID>/worktree or /repo) is ~60-70 MB of the
// project's own repo, and nearly every run only adds a handful of small files
// to it. Keeping checkouts filled the disk twice (57 GB 2026-10-11, 21 GB
// 2026-10-13). So a run directory is a throwaway workspace, GitHub-Actions
// style, and what survives the run is its ARTIFACT:
//
//	WorkDir/artifacts/<runID>.tar.gz
//	  manifest.json   {runID, repo, baseSHA, branch, layout, files, skipped, createdAt}
//	  changes.patch   `git diff --binary <baseSHA>` (tracked edits, staged or not, and commits)
//	  files/<path>    every untracked (non-ignored) and modified file
//
// .git, node_modules and files over artifactMaxFile are left out; the skipped
// ones are listed in the manifest. A non-git workspace (repo/ with no .git) is
// tarred whole under the same exclusions.
//
//   - when a run ends (done, cancelled or failed) it is archived, then its
//     directory goes;
//   - at boot and every runDirSweepEvery, any run directory whose run is
//     readable and not live is archived and removed the same way (crashes,
//     runs from before this code), and only the newest cfg.RunArtifactsKeep
//     artifacts are kept;
//   - Retry of a run whose directory is gone rebuilds the workspace at the
//     manifest's baseSHA and replays the patch and the files (restoreArtifact).

const (
	runDirSweepEvery = time.Hour
	// runDirMinAge keeps the sweep off a directory a Retry is restoring right now.
	runDirMinAge    = 10 * time.Minute
	artifactMaxFile = 5 << 20
	artifactsDir    = "artifacts"
)

// artifactsKeep is how many run artifacts survive the cap.
func (e *Engine) artifactsKeep() int {
	if n := e.cfg.RunArtifactsKeep; n > 0 {
		return n
	}
	return 20
}

func (e *Engine) artifactPath(runID uuid.UUID) string {
	return filepath.Join(e.cfg.WorkDir, artifactsDir, runID.String()+".tar.gz")
}

// StartRunDirSweeper runs SweepRunDirs every runDirSweepEvery until ctx ends.
func (e *Engine) StartRunDirSweeper(ctx context.Context) {
	e.startRunDirSweeper(ctx, runDirSweepEvery)
}

func (e *Engine) startRunDirSweeper(ctx context.Context, every time.Duration) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n := e.SweepRunDirs(ctx); n > 0 {
					log.Printf("engine: archived and removed %d run directory(ies)", n)
				}
			}
		}
	}()
}

// liveStatuses are runs that may still use their directory.
var liveStatuses = map[string]bool{"queued": true, "running": true, "awaiting_approval": true, "needs_input": true}

// endedStatuses are runs whose directory is archived and removed when they end.
var endedStatuses = map[string]bool{"done": true, "cancelled": true, "failed": true}

// removeRunDir deletes WorkDir/<runID>. Every git worktree under it — the
// run's own and any a step cut inside it (workflow-doctor's prepare.py) — is
// detached from its parent repo first and the parent pruned, so no
// `git worktree list` keeps a dead entry.
func (e *Engine) removeRunDir(runID uuid.UUID) {
	if e.cfg.WorkDir == "" {
		return
	}
	dir := filepath.Join(e.cfg.WorkDir, runID.String())
	commons := detachWorktrees(dir)
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("engine: run %s: removing %s: %v", runID, dir, err)
	}
	for _, c := range commons {
		_ = exec.Command("git", "--git-dir", c, "worktree", "prune").Run()
	}
}

// detachWorktrees finds every linked worktree under dir (a `.git` FILE saying
// "gitdir: <repo>/.git/worktrees/<name>"), removes it through git, and returns
// the parent repos' git dirs so they can be pruned once dir is gone.
func detachWorktrees(dir string) []string {
	seen := map[string]bool{}
	var commons, wts []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if d.Name() != ".git" || !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		gd := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
		if !filepath.IsAbs(gd) {
			gd = filepath.Join(filepath.Dir(p), gd)
		}
		// <common>/worktrees/<name>
		if filepath.Base(filepath.Dir(gd)) == "worktrees" {
			c := filepath.Dir(filepath.Dir(gd))
			if !seen[c] {
				seen[c] = true
				commons = append(commons, c)
			}
		}
		wts = append(wts, filepath.Dir(p))
		return nil
	})
	// deepest first, so a nested worktree goes before the one holding it
	sort.Slice(wts, func(i, j int) bool { return len(wts[i]) > len(wts[j]) })
	for _, wt := range wts {
		_ = exec.Command("git", "-C", wt, "worktree", "remove", "--force", wt).Run()
	}
	return commons
}

// endRunDir archives the run's workspace and removes its directory.
func (e *Engine) endRunDir(runID uuid.UUID) {
	if e.cfg.WorkDir == "" {
		return
	}
	dir := filepath.Join(e.cfg.WorkDir, runID.String())
	if _, err := os.Stat(dir); err != nil {
		return
	}
	if err := e.archiveRunDir(context.Background(), runID); err != nil {
		log.Printf("engine: run %s: artifact: %v", runID, err)
	}
	e.removeRunDir(runID)
}

// cleanupFinished archives and removes the run's directory if the run ended.
func (e *Engine) cleanupFinished(runID uuid.UUID) {
	if e.store == nil {
		return
	}
	run, err := e.store.GetRun(context.Background(), runID)
	if err != nil {
		return
	}
	if endedStatuses[run.Status] {
		e.endRunDir(runID)
		e.pruneArtifacts()
	}
}

// SweepRunDirs archives and removes every run directory whose run is readable
// and not live, then enforces the artifact cap. Returns how many directories
// it removed. Only UUID-named directories are touched: sources/, projects/,
// proposals/, artifacts/ and the registries are never run directories.
func (e *Engine) SweepRunDirs(ctx context.Context) int {
	if e.store == nil || e.cfg.WorkDir == "" {
		return 0
	}
	entries, err := os.ReadDir(e.cfg.WorkDir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-runDirMinAge)
	n := 0
	for _, ent := range entries {
		id, err := uuid.Parse(ent.Name())
		if err != nil || !ent.IsDir() {
			continue // not a run directory
		}
		info, err := ent.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		// A lookup error is not proof the run is gone (the store may be down),
		// so only a run we can read and see is over loses its directory.
		run, err := e.store.GetRun(ctx, id)
		if err != nil || liveStatuses[run.Status] {
			continue
		}
		e.endRunDir(id)
		n++
	}
	e.pruneArtifacts()
	return n
}

// pruneArtifacts keeps the newest artifactsKeep() <uuid>.tar.gz artifacts.
func (e *Engine) pruneArtifacts() {
	dir := filepath.Join(e.cfg.WorkDir, artifactsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type art struct {
		path string
		mod  time.Time
	}
	var arts []art
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".tar.gz") {
			continue
		}
		if _, err := uuid.Parse(strings.TrimSuffix(name, ".tar.gz")); err != nil {
			continue
		}
		info, err := ent.Info()
		if err != nil {
			continue
		}
		arts = append(arts, art{filepath.Join(dir, name), info.ModTime()})
	}
	sort.Slice(arts, func(i, j int) bool { return arts[i].mod.After(arts[j].mod) })
	for _, a := range arts[min(len(arts), e.artifactsKeep()):] {
		_ = os.Remove(a.path)
	}
}

// ---- the artifact ----

type artifactManifest struct {
	RunID     string    `json:"runID"`
	Repo      string    `json:"repo,omitempty"`
	BaseSHA   string    `json:"baseSHA,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Layout    string    `json:"layout"` // worktree | repo
	Git       bool      `json:"git"`
	Files     []string  `json:"files"`
	Skipped   []skipped `json:"skipped"`
	CreatedAt time.Time `json:"createdAt"`
}

type skipped struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// runWorkspace returns the run's workspace dir and its layout name, or "" when
// there is none.
func (e *Engine) runWorkspace(runID uuid.UUID) (string, string) {
	for _, layout := range []string{"worktree", "repo"} {
		p := filepath.Join(e.cfg.WorkDir, runID.String(), layout)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p, layout
		}
	}
	return "", ""
}

func gitOut(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// isGitTop says whether dir is itself the top of a git working tree.
func isGitTop(dir string) bool {
	top, err := gitOut(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	a, _ := filepath.EvalSymlinks(top)
	b, _ := filepath.EvalSymlinks(dir)
	return a == b
}

func excludedPath(rel string) bool {
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == ".git" || part == "node_modules" {
			return true
		}
	}
	return false
}

// archiveRunDir writes WorkDir/artifacts/<runID>.tar.gz from the run's workspace.
func (e *Engine) archiveRunDir(ctx context.Context, runID uuid.UUID) error {
	ws, layout := e.runWorkspace(runID)
	if ws == "" {
		return nil // nothing a run produced
	}
	m := artifactManifest{RunID: runID.String(), Layout: layout, Files: []string{}, Skipped: []skipped{}, CreatedAt: time.Now().UTC()}
	var patch []byte
	var candidates []string
	if isGitTop(ws) {
		m.Git = true
		if e.store != nil {
			if run, err := e.store.GetRun(ctx, runID); err == nil {
				m.BaseSHA = run.BaseRef
			}
		}
		if m.BaseSHA == "" {
			m.BaseSHA, _ = gitOut(ws, "rev-parse", "HEAD")
		}
		if c, err := gitOut(ws, "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
			m.Repo = strings.TrimSuffix(c, string(filepath.Separator)+".git")
		}
		m.Branch, _ = gitOut(ws, "rev-parse", "--abbrev-ref", "HEAD")
		if m.BaseSHA != "" {
			p, err := exec.Command("git", "-C", ws, "diff", "--binary", m.BaseSHA).Output()
			if err != nil {
				return fmt.Errorf("git diff: %w", err)
			}
			patch = p
		}
		list := func(args ...string) []string {
			out, _ := exec.Command("git", append([]string{"-C", ws}, args...)...).Output()
			var r []string
			for _, s := range strings.Split(string(out), "\x00") {
				if s != "" {
					r = append(r, s)
				}
			}
			return r
		}
		for _, rel := range list("ls-files", "-z", "--others", "--exclude-standard") {
			if strings.HasSuffix(rel, "/") { // a nested repo or worktree: walk it
				candidates = append(candidates, walkFiles(ws, strings.TrimSuffix(rel, "/"))...)
				continue
			}
			candidates = append(candidates, rel)
		}
		if m.BaseSHA != "" {
			candidates = append(candidates, list("diff", "-z", "--name-only", "--diff-filter=d", m.BaseSHA)...)
		}
	} else {
		candidates = walkFiles(ws, "")
	}
	sort.Strings(candidates)

	if err := os.MkdirAll(filepath.Join(e.cfg.WorkDir, artifactsDir), 0o755); err != nil {
		return err
	}
	final := e.artifactPath(runID)
	tmp, err := os.CreateTemp(filepath.Dir(final), ".tmp-"+runID.String()+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	gz := gzip.NewWriter(tmp)
	tw := tar.NewWriter(gz)
	seen := map[string]bool{}
	for _, rel := range candidates {
		if seen[rel] || excludedPath(rel) {
			continue
		}
		seen[rel] = true
		full := filepath.Join(ws, rel)
		fi, err := os.Lstat(full)
		if err != nil || fi.IsDir() {
			continue
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(full)
			if err != nil {
				continue
			}
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeSymlink, Name: "files/" + filepath.ToSlash(rel), Linkname: target, Mode: 0o777, ModTime: fi.ModTime()}); err != nil {
				return err
			}
		case !fi.Mode().IsRegular():
			continue
		case fi.Size() > artifactMaxFile:
			m.Skipped = append(m.Skipped, skipped{rel, fmt.Sprintf("larger than %d MB (%d bytes)", artifactMaxFile>>20, fi.Size())})
			continue
		default:
			if err := tarFile(tw, "files/"+filepath.ToSlash(rel), full, fi); err != nil {
				return err
			}
		}
		m.Files = append(m.Files, rel)
	}
	if len(patch) > 0 {
		if err := tarBytes(tw, "changes.patch", patch); err != nil {
			return err
		}
	}
	mj, _ := json.MarshalIndent(m, "", "  ")
	if err := tarBytes(tw, "manifest.json", mj); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), final)
}

// walkFiles lists every non-directory under root/sub, relative to root,
// skipping .git and node_modules. Symlinks are listed, never followed.
func walkFiles(root, sub string) []string {
	var out []string
	_ = filepath.WalkDir(filepath.Join(root, sub), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Name() == ".git" || d.Name() == "node_modules" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	return out
}

func tarBytes(tw *tar.Writer, name string, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), ModTime: time.Now()}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

func tarFile(tw *tar.Writer, name, full string, fi os.FileInfo) error {
	f, err := os.Open(full)
	if err != nil {
		return nil // vanished: not worth failing the archive
	}
	defer f.Close()
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(fi.Mode().Perm()), Size: fi.Size(), ModTime: fi.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, bufio.NewReader(f))
	return err
}

// readArtifact opens the run's artifact; ok=false when there is none.
func (e *Engine) readArtifact(runID uuid.UUID) (m artifactManifest, patch []byte, files map[string]*tar.Header, data map[string][]byte, ok bool) {
	f, err := os.Open(e.artifactPath(runID))
	if err != nil {
		return m, nil, nil, nil, false
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return m, nil, nil, nil, false
	}
	tr := tar.NewReader(gz)
	files, data = map[string]*tar.Header{}, map[string][]byte{}
	haveManifest := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, nil, nil, nil, false
		}
		b, _ := io.ReadAll(tr)
		switch {
		case h.Name == "manifest.json":
			haveManifest = json.Unmarshal(b, &m) == nil
		case h.Name == "changes.patch":
			patch = b
		case strings.HasPrefix(h.Name, "files/"):
			rel := strings.TrimPrefix(h.Name, "files/")
			files[rel], data[rel] = h, b
		}
	}
	return m, patch, files, data, haveManifest
}

// restoreArtifact replays an ended run's patch and files onto a fresh
// workspace at ws (already at the manifest's baseSHA when it is git).
func (e *Engine) restoreArtifact(ctx context.Context, runID uuid.UUID, ws string) error {
	m, patch, files, data, ok := e.readArtifact(runID)
	if !ok {
		return nil
	}
	if len(patch) > 0 && m.Git {
		cmd := exec.CommandContext(ctx, "git", "-C", ws, "apply", "--binary", "--whitespace=nowarn", "-")
		cmd.Stdin = bytes.NewReader(patch)
		if out, err := cmd.CombinedOutput(); err != nil {
			// the files below still carry every modified file's final content
			e.emit(ctx, runID, "", "log", map[string]any{"text": fmt.Sprintf("restore: changes.patch did not apply cleanly (%v): %s", err, bytes.TrimSpace(out))})
		}
	}
	root, _ := filepath.Abs(ws)
	for rel, h := range files {
		dst := filepath.Join(root, filepath.FromSlash(rel))
		if !strings.HasPrefix(dst, root+string(filepath.Separator)) {
			continue // never write outside the workspace
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		_ = os.Remove(dst)
		if h.Typeflag == tar.TypeSymlink {
			if err := os.Symlink(h.Linkname, dst); err != nil {
				return err
			}
			continue
		}
		if err := os.WriteFile(dst, data[rel], os.FileMode(h.Mode).Perm()|0o200); err != nil {
			return err
		}
	}
	e.emit(ctx, runID, "", "log", map[string]any{"text": fmt.Sprintf("restored the run's workspace from its artifact (base %s, %d file(s))", m.BaseSHA, len(files))})
	return nil
}

// artifactBase is the commit a retried run's worktree is cut at: the artifact
// manifest's baseSHA, or "" when there is no artifact.
func (e *Engine) artifactBase(runID uuid.UUID) (string, bool) {
	m, _, _, _, ok := e.readArtifact(runID)
	return m.BaseSHA, ok
}

// noteMissingArtifact logs, on the run, that a run being resumed into a fresh
// workspace had an artifact once (it has a base ref) but it was pruned.
func (e *Engine) noteMissingArtifact(ctx context.Context, runID uuid.UUID) {
	if e.store == nil {
		return
	}
	if run, err := e.store.GetRun(ctx, runID); err == nil && run.BaseRef != "" {
		msg := "the run's artifact was pruned (or never written); retrying from a fresh checkout without the earlier steps' edits"
		log.Printf("engine: run %s: %s", runID, msg)
		e.emit(ctx, runID, "", "log", map[string]any{"text": msg})
	}
}

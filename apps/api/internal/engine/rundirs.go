package engine

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// A run's checkout (WorkDir/<runID>/worktree or /repo) is ~60 MB and nothing
// reads it once the run is over, so at ~140 runs a day it filled the disk
// (57 GB, 2026-10-11). Two cleanups keep it bounded:
//
//   - when a run ends done or cancelled, its directory goes at once;
//   - at boot, any run directory untouched for runDirMaxAge whose run is not
//     still live goes too. That catches failed runs (kept so Retry still has
//     the edits of the earlier steps) and anything a crash left behind.

const runDirMaxAge = 24 * time.Hour

// liveStatuses are runs that may still use their directory.
var liveStatuses = map[string]bool{"queued": true, "running": true, "awaiting_approval": true, "needs_input": true}

// removeRunDir deletes WorkDir/<runID>. A worktree is detached from its parent
// repo first, so the parent's `git worktree list` does not keep a dead entry.
func (e *Engine) removeRunDir(runID uuid.UUID) {
	if e.cfg.WorkDir == "" {
		return
	}
	dir := filepath.Join(e.cfg.WorkDir, runID.String())
	wt := filepath.Join(dir, "worktree")
	if _, err := os.Stat(filepath.Join(wt, ".git")); err == nil {
		_ = exec.Command("git", "-C", wt, "worktree", "remove", "--force", wt).Run()
	}
	if err := os.RemoveAll(dir); err != nil {
		log.Printf("engine: run %s: removing %s: %v", runID, dir, err)
	}
}

// cleanupFinished removes the run's directory if the run ended done or cancelled.
func (e *Engine) cleanupFinished(runID uuid.UUID) {
	if e.store == nil {
		return
	}
	run, err := e.store.GetRun(context.Background(), runID)
	if err != nil {
		return
	}
	if run.Status == "done" || run.Status == "cancelled" {
		e.removeRunDir(runID)
	}
}

// SweepRunDirs removes run directories older than runDirMaxAge whose run
// is readable and not live. Returns how many it removed.
func (e *Engine) SweepRunDirs(ctx context.Context) int {
	if e.store == nil || e.cfg.WorkDir == "" {
		return 0
	}
	entries, err := os.ReadDir(e.cfg.WorkDir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-runDirMaxAge)
	n := 0
	for _, ent := range entries {
		id, err := uuid.Parse(ent.Name())
		if err != nil || !ent.IsDir() {
			continue // not a run directory (sources/, projects/, proposals/, ...)
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
		e.removeRunDir(id)
		n++
	}
	return n
}

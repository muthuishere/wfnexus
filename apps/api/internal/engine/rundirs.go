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
//   - at boot and then every runDirSweepEvery, any run directory untouched
//     for runDirMaxAge whose run is not still live goes too. A failed run is
//     kept longer (cfg.FailedRunRetentionDays, default 3) so Retry still has
//     the edits of the earlier steps. That also catches what a crash left.
//
// Only the boot sweep ran at first, so a server up for weeks grew again
// (21 GB, 2026-10-13).

const (
	runDirMaxAge     = 24 * time.Hour
	runDirSweepEvery = time.Hour
)

// failedRetention is how long a failed run keeps its directory.
func (e *Engine) failedRetention() time.Duration {
	if d := e.cfg.FailedRunRetentionDays; d > 0 {
		return time.Duration(d) * 24 * time.Hour
	}
	return 3 * 24 * time.Hour
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
					log.Printf("engine: swept %d old run directory(ies)", n)
				}
			}
		}
	}()
}

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

// SweepRunDirs removes run directories older than runDirMaxAge (a failed
// run's: older than failedRetention) whose run is readable and not live.
// Returns how many it removed.
func (e *Engine) SweepRunDirs(ctx context.Context) int {
	if e.store == nil || e.cfg.WorkDir == "" {
		return 0
	}
	entries, err := os.ReadDir(e.cfg.WorkDir)
	if err != nil {
		return 0
	}
	now := time.Now()
	cutoff, failedCutoff := now.Add(-runDirMaxAge), now.Add(-e.failedRetention())
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
		if run.Status == "failed" && info.ModTime().After(failedCutoff) {
			continue // still retryable
		}
		e.removeRunDir(id)
		n++
	}
	return n
}

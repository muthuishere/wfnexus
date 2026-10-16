package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
)

// THE RUN TELLS ITS STORY.
//
// A run page full of "0 turns · 0 tok" rows says nothing about what the run
// found. So a step can say it, the way GitHub Actions' GITHUB_STEP_SUMMARY
// does — by appending to a FILE the engine names in its environment:
//
//	WFX_STEP_SUMMARY  markdown; stored as the step output's `summary` and shown
//	                  under "What it found" on the run page.
//	WFX_OUTCOME       one line, `ok|incident|waiting|failed: <text>`; stored as
//	                  the step output's `outcome` and becomes the RUN's headline
//	                  verdict (the last step that wrote one wins).
//	WFX_STATE         a JSON object; each key is written to this workflow's
//	                  state scope (`{{ .Workflow.<key> }}` next run), the same
//	                  store a step's yaml `state:` block writes to.
//
// An agent step needs none of this: an output field named `summary` or
// `outcome` (a string) is read exactly the same way.
//
// There is deliberately NO run-level summary file. The run's headline is its
// outcome; when no step wrote one, it is derived when the run ends — a failed
// run says why it failed, a finished one leads with the first line of the last
// summary any step wrote. One rule, readable from the step rows alone, so a
// retried run re-derives instead of keeping a stale verdict.
//
// Files are local to the machine that runs the step: a step placed on a worker
// (`runs-on`) does not get them yet.

const (
	EnvStepSummary = "WFX_STEP_SUMMARY"
	EnvOutcome     = "WFX_OUTCOME"
	EnvState       = "WFX_STATE"

	maxSummaryBytes = 64 << 10
	maxOutcomeLen   = 300
)

// Verdicts are the words an outcome line may start with.
var Verdicts = []string{"ok", "incident", "waiting", "failed"}

// storyFiles is the set of files one step execution may write.
type storyFiles struct {
	dir                     string
	summary, outcome, state string
}

func newStoryFiles() (*storyFiles, error) {
	dir, err := os.MkdirTemp("", "wfx-story-")
	if err != nil {
		return nil, err
	}
	f := &storyFiles{
		dir:     dir,
		summary: filepath.Join(dir, "summary.md"),
		outcome: filepath.Join(dir, "outcome"),
		state:   filepath.Join(dir, "state.json"),
	}
	// Created empty so `>>` and `cat` both work from the first line.
	for _, p := range []string{f.summary, f.outcome, f.state} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
	}
	return f, nil
}

func (f *storyFiles) env() map[string]string {
	return map[string]string{EnvStepSummary: f.summary, EnvOutcome: f.outcome, EnvState: f.state}
}

func (f *storyFiles) close() { os.RemoveAll(f.dir) }

func readCapped(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > n {
		b = append(b[:n], []byte("\n\n…(truncated)")...)
	}
	return string(b)
}

// collect folds what the step wrote into its output map, and publishes its
// state. Returns the outcome line, normalised, or "".
func (e *Engine) collectStory(ctx context.Context, runID uuid.UUID, stepID string, f *storyFiles, out map[string]any) string {
	if s := strings.TrimSpace(readCapped(f.summary, maxSummaryBytes)); s != "" {
		out["summary"] = s
	}
	oc := NormalizeOutcome(readCapped(f.outcome, 4096))
	if oc != "" {
		out["outcome"] = oc
	}
	if raw := strings.TrimSpace(readCapped(f.state, maxSummaryBytes)); raw != "" {
		if err := e.publishState(ctx, e.workflowOfRun(ctx, runID), raw); err != nil {
			e.emit(ctx, runID, stepID, "log", map[string]any{"message": fmt.Sprintf("%s: %v", EnvState, err)})
		}
	}
	return oc
}

// workflowOfRun is the workflow name state is keyed by — the definition's own
// name, as a yaml `state:` block writes it (persistStepState).
func (e *Engine) workflowOfRun(ctx context.Context, runID uuid.UUID) string {
	if e.store == nil {
		return ""
	}
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return ""
	}
	if def := e.Definitions()[run.Workflow]; def != nil {
		return def.Name
	}
	return run.Workflow
}

// publishState writes a WFX_STATE JSON object to the workflow scope.
func (e *Engine) publishState(ctx context.Context, workflowName, raw string) error {
	if workflowName == "" {
		return fmt.Errorf("no workflow to keep state for")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return fmt.Errorf("must be a JSON object: %w", err)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var val string
		switch v := obj[k].(type) {
		case string:
			val = v
		default:
			b, _ := json.Marshal(v)
			val = string(b)
		}
		if err := e.SetState(ctx, model.StateScopeWorkflow, workflowName, k, val); err != nil {
			return fmt.Errorf("key %q: %w", k, err)
		}
	}
	return nil
}

// NormalizeOutcome makes one outcome line out of whatever a step wrote: the
// LAST non-empty line, `<verdict>: <text>`. A line without a known verdict is
// read as `ok: <line>` — a step that only says what happened still said it.
func NormalizeOutcome(s string) string {
	var line string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			line = t
		}
	}
	if line == "" {
		return ""
	}
	verdict, text, found := strings.Cut(line, ":")
	v := strings.ToLower(strings.TrimSpace(verdict))
	known := false
	for _, k := range Verdicts {
		if v == k {
			known = true
		}
	}
	if !known {
		v, text, found = "ok", line, true
	}
	text = strings.TrimSpace(text)
	if !found || text == "" {
		line = v
	} else {
		line = v + ": " + text
	}
	if len(line) > maxOutcomeLen {
		line = line[:maxOutcomeLen] + "…"
	}
	return line
}

// headlineOf is the first readable line of a markdown summary.
func headlineOf(md string) string {
	for _, l := range strings.Split(md, "\n") {
		t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#>*-• "))
		if t != "" {
			return t
		}
	}
	return ""
}

func outputString(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}

// DeriveOutcome is the run's verdict from its step rows, for a run that ended
// with `status` and `errMsg`.
func DeriveOutcome(status, errMsg string, steps []*model.StepRun) string {
	sorted := append([]*model.StepRun(nil), steps...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Position < sorted[j].Position })
	var written, summary string
	for _, s := range sorted {
		if oc := NormalizeOutcome(outputString(s.Output, "outcome")); oc != "" {
			written = oc
		}
		if sm := outputString(s.Output, "summary"); sm != "" {
			summary = sm
		}
	}
	switch status {
	case "failed":
		if strings.HasPrefix(written, "incident") || strings.HasPrefix(written, "failed") {
			return written
		}
		if errMsg = strings.TrimSpace(strings.SplitN(errMsg, "\n", 2)[0]); errMsg != "" {
			return NormalizeOutcome("failed: " + errMsg)
		}
		return "failed"
	case "done":
		if written != "" {
			return written
		}
		if h := headlineOf(summary); h != "" {
			return NormalizeOutcome("ok: " + h)
		}
		return "ok"
	}
	return written
}

// settleOutcome stores the derived verdict when a run reaches an end state.
func (e *Engine) settleOutcome(ctx context.Context, runID uuid.UUID, status, errMsg string) {
	if e.store == nil || (status != "done" && status != "failed") {
		return
	}
	ctx = context.WithoutCancel(ctx)
	steps, err := e.store.ListSteps(ctx, runID)
	if err != nil {
		log.Printf("engine: outcome: %v", err)
		return
	}
	if err := e.store.SetRunOutcome(ctx, runID, DeriveOutcome(status, errMsg, steps)); err != nil {
		log.Printf("engine: outcome: %v", err)
	}
}

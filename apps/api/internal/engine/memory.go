package engine

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// WORKFLOW MEMORY — what previous runs of a workflow learned, on by default.
//
// State (state.go) is EXPLICIT: a workflow remembers the keys its author chose
// to write. Memory is the IMPLICIT complement: every finished run leaves a
// deterministic record — what it was asked, how it ended, what each step
// concluded, what failed and why, what a human decided — and every agent step
// of the next run reads those records back in a bounded section of its prompt.
// Agents can add explicit lessons with the `remember` tool.
//
// Three properties matter more than the feature:
//
//  1. It never fails a run. Every write here logs and moves on.
//  2. It never stores a secret. Bodies are scrubbed with the run's redactions
//     plus every env-store value before they are written.
//  3. It is bounded. A per-step character budget on the way in; a raw-record
//     cap, a rolling capped digest and a lesson cap on the way out.

const (
	memoryCharsPerToken = 4
	memoryRecentShown   = 5    // raw run records shown per workflow
	memoryNotesShown    = 10   // lessons shown per workflow
	memoryIncludeRuns   = 3    // raw records shown per included source
	memoryDigestMax     = 3000 // characters in a workflow's rolling digest
	memoryNoteMax       = 500  // characters in one lesson
	memoryFieldMax      = 300  // characters of one step's output or error
	memoryInputMax      = 400  // characters of a run's input
	memoryStepsMax      = 20   // step lines in one record
	memoryHeading       = "## What previous runs of this workflow learned"
	// The injected layer's HARD cap, whatever a workflow's budget says: an
	// index a model reads every step stays small (Anthropic, "Components of
	// agent memory": ~200 lines / 25 KB); the rest is one command away.
	memoryInjectMaxLines = 200
	memoryInjectMaxBytes = 25 * 1024
)

// memoryPointer is where the part of memory that was not injected lives.
func memoryPointer(wf string) string {
	return "\n… (memory truncated to its budget; the rest: `wfx memory show --workflow " + wf + "`)"
}

// capInjected applies the line cap; the byte cap is applied while building.
func capInjected(s, wf string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= memoryInjectMaxLines {
		return s
	}
	return strings.Join(lines[:memoryInjectMaxLines-2], "\n") + memoryPointer(wf)
}

// memoryEnded are the statuses a run leaves memory at.
var memoryEnded = map[string]bool{"done": true, "failed": true, "cancelled": true}

// clip shortens s to n bytes on a rune boundary, flattening newlines.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// scrubber returns a function that removes every sensitive value this run
// knows about: the run's registered redactions plus every value of the env
// store for its project — the latter so a record written after a restart, when
// the in-memory redactions are gone, is still clean.
func (e *Engine) scrubber(ctx context.Context, runID uuid.UUID, project string) func(string) string {
	if base, err := e.platformEnv(ctx, project); err == nil && len(base) > 0 {
		sealed := map[string]bool{}
		for k := range base {
			sealed[k] = true
		}
		e.redactions.add(runID, base, sealed)
	}
	secrets := e.redactions.get(runID)
	return func(s string) string { return redactText(s, secrets) }
}

// ---- the END of a run: record it ----

// recordMemory writes the run's record. It is called from setRun for every
// terminal status, before the status lands, and never returns an error: memory
// is a side effect a run must survive without.
func (e *Engine) recordMemory(ctx context.Context, runID uuid.UUID, status, errMsg string) {
	if e.store == nil || !memoryEnded[status] {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("engine: memory for run %s: %v", runID, r)
		}
	}()
	ctx = context.WithoutCancel(ctx)
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return
	}
	def := e.defs[run.Workflow]
	plan := workflow.EffectiveMemory(def, nil)
	if !plan.On {
		return
	}
	existing, err := e.store.ListMemory(ctx, model.MemoryFilter{
		Project: run.Project, Workflow: run.Workflow, Kind: model.MemoryKindRun, RunID: runID.String(),
	})
	if err != nil {
		log.Printf("engine: memory for run %s: %v", runID, err)
		return
	}
	// A cancel that arrives after the run already ended (or after a failure)
	// does not rewrite what actually happened.
	if status == "cancelled" && len(existing) > 0 {
		return
	}
	steps, _ := e.store.ListSteps(ctx, runID)
	scrub := e.scrubber(ctx, runID, run.Project)
	body := runRecord(run, status, errMsg, steps, scrub)
	for _, old := range existing {
		_ = e.store.DeleteMemory(ctx, old.ID)
	}
	// A run from a chat remembers into ITS conversation, unless the workflow
	// said `memory: {scope: workflow}` (conversation.go).
	conv := ""
	if plan.Scope != workflow.MemoryScopeWorkflow {
		conv = conversationOfRun(run)
	}
	if _, err := e.store.AddMemory(ctx, model.MemoryEntry{
		Project: run.Project, Workflow: run.Workflow, RunID: runID.String(),
		Kind: model.MemoryKindRun, Status: status, Body: body, Conversation: conv,
	}); err != nil {
		log.Printf("engine: memory for run %s: %v", runID, err)
		return
	}
	e.consolidateMemoryIn(ctx, run.Project, run.Workflow, conv, plan.Retain)
}

// runRecord is the deterministic record of one run. Every free-text value is
// scrubbed BEFORE it is clipped, so a cut can never leave half a secret.
func runRecord(run *model.Run, status, errMsg string, steps []*model.StepRun, scrub func(string) string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "run %s · %s · %s\n", shortID(run.ID.String()), status, time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	if in := strings.TrimSpace(string(run.Input)); in != "" && in != "{}" && in != "null" {
		fmt.Fprintf(&b, "asked: %s\n", clip(scrub(in), memoryInputMax))
	}
	if o := strings.TrimSpace(run.Outcome); o != "" {
		fmt.Fprintf(&b, "outcome: %s\n", clip(scrub(o), memoryFieldMax))
	}
	if errMsg = strings.TrimSpace(errMsg); errMsg == "" && status == "failed" {
		errMsg = strings.TrimSpace(run.Error)
	}
	if errMsg != "" {
		fmt.Fprintf(&b, "error: %s\n", clip(scrub(errMsg), memoryFieldMax))
	}
	n := 0
	for _, st := range steps {
		if st.Status == "pending" || st.Status == "" {
			continue
		}
		if n == memoryStepsMax {
			b.WriteString("- … more steps not shown\n")
			break
		}
		n++
		if n == 1 {
			b.WriteString("steps:\n")
		}
		fmt.Fprintf(&b, "- %s: %s", st.StepID, st.Status)
		if st.Resolution != "" {
			fmt.Fprintf(&b, " · %s", st.Resolution)
			if st.ResolvedBy != "" {
				fmt.Fprintf(&b, " by %s", st.ResolvedBy)
			}
			if r := strings.TrimSpace(st.ResolutionReason); r != "" {
				fmt.Fprintf(&b, " — %q", clip(scrub(r), memoryFieldMax))
			}
		}
		if st.Error != "" {
			fmt.Fprintf(&b, " — error: %s", clip(scrub(st.Error), memoryFieldMax))
		} else if out := strings.TrimSpace(string(st.Output)); out != "" && out != "null" && out != "{}" {
			fmt.Fprintf(&b, " → %s", clip(scrub(out), memoryFieldMax))
		}
		if st.SkipReason != "" {
			fmt.Fprintf(&b, " (%s)", clip(st.SkipReason, 120))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// consolidateMemory keeps a workflow's memory bounded: at most `retain` raw run
// records — older ones fold into the rolling digest, which is itself capped —
// and at most 2×retain lessons.
func (e *Engine) consolidateMemory(ctx context.Context, project, wf string, retain int) {
	e.consolidateMemoryIn(ctx, project, wf, "", retain)
}

// consolidateMemoryIn is consolidateMemory for one conversation's records ("" is
// the workflow-wide ones): each conversation keeps its own records and digest.
func (e *Engine) consolidateMemoryIn(ctx context.Context, project, wf, conv string, retain int) {
	// ONE WRITER PER PAGE. Folding reads the digest, writes a new one and
	// deletes the old: two runs ending together each folded the same records
	// and one digest's lines were lost. Per (project, workflow, conversation)
	// it is serialised; a lesson (`remember`) is its own row and never folds.
	unlock := memoryPages.lock(project + "\x00" + wf + "\x00" + conv)
	defer unlock()
	if retain <= 0 {
		retain = workflow.DefaultMemoryRetain
	}
	runs, err := e.store.ListMemory(ctx, model.MemoryFilter{Project: project, Workflow: wf, Kind: model.MemoryKindRun, ByConversation: true, Conversation: conv})
	if err == nil && len(runs) > retain {
		folded := runs[retain:]
		var lines []string
		for _, r := range folded { // newest first
			lines = append(lines, "- "+digestLine(r))
		}
		digests, _ := e.store.ListMemory(ctx, model.MemoryFilter{Project: project, Workflow: wf, Kind: model.MemoryKindDigest, ByConversation: true, Conversation: conv})
		for _, d := range digests {
			if d.Body != "" {
				lines = append(lines, strings.Split(d.Body, "\n")...)
			}
		}
		// Newest lines win: drop from the old end until it fits.
		for len(lines) > 1 && len(strings.Join(lines, "\n")) > memoryDigestMax {
			lines = lines[:len(lines)-1]
		}
		if _, err := e.store.AddMemory(ctx, model.MemoryEntry{
			Project: project, Workflow: wf, Kind: model.MemoryKindDigest, Body: strings.Join(lines, "\n"), Conversation: conv,
		}); err == nil {
			for _, d := range digests {
				_ = e.store.DeleteMemory(ctx, d.ID)
			}
			for _, r := range folded {
				_ = e.store.DeleteMemory(ctx, r.ID)
			}
		}
	}
	notes, err := e.store.ListMemory(ctx, model.MemoryFilter{Project: project, Workflow: wf, Kind: model.MemoryKindNote})
	if err == nil && len(notes) > 2*retain {
		for _, n := range notes[2*retain:] {
			_ = e.store.DeleteMemory(ctx, n.ID)
		}
	}
}

// pageLocks is a mutex per memory page, created on first use.
type pageLocks struct {
	mu    sync.Mutex
	pages map[string]*sync.Mutex
}

var memoryPages = &pageLocks{pages: map[string]*sync.Mutex{}}

func (p *pageLocks) lock(key string) func() {
	p.mu.Lock()
	m := p.pages[key]
	if m == nil {
		m = &sync.Mutex{}
		p.pages[key] = m
	}
	p.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// digestLine is a folded run record's one line: its header, plus the line that
// says how it ended badly, if any.
func digestLine(r model.MemoryEntry) string {
	lines := strings.Split(r.Body, "\n")
	line := lines[0]
	for _, l := range lines[1:] {
		if strings.HasPrefix(l, "error: ") || strings.HasPrefix(l, "outcome: ") {
			line += " · " + l
			break
		}
	}
	return clip(line, 240)
}

// ---- during a run: the `remember` tool ----

// rememberTool lets an agent record an explicit lesson for the next runs of
// this workflow. The address comes from the run row, never from the model, so
// a step can only write its own workflow's memory. Nil on a worker engine and
// for a step whose memory is off.
func (e *Engine) rememberTool(runID uuid.UUID, wfName string, step *workflow.Step) []tn.Tool {
	if e.store == nil {
		return nil
	}
	if !workflow.EffectiveMemory(e.defs[wfName], step).On {
		return nil
	}
	schema := map[string]any{
		"type":     "object",
		"required": []any{"lesson"},
		"properties": map[string]any{
			"lesson": map[string]any{
				"type":        "string",
				"description": "One short, concrete note a future run needs and cannot see itself: strategy, a tested rule, a discovered fact, the plan.",
			},
		},
	}
	return []tn.Tool{tn.NativeTool("remember",
		"Write a note for FUTURE runs of this workflow: what they cannot see from their own inputs — the strategy "+
			"that worked, a rule you tested and confirmed, a fact you had to discover, the plan for what comes next. "+
			"Not a log of this run (that is recorded already) and not only failures. One short, concrete note; "+
			"never include secrets. It is shown to every agent step of the next runs.",
		schema,
		func(ctx context.Context, args map[string]any) (string, error) {
			lesson, _ := args["lesson"].(string)
			if strings.TrimSpace(lesson) == "" {
				return "", fmt.Errorf("lesson is empty")
			}
			run, err := e.store.GetRun(ctx, runID)
			if err != nil {
				return "", err
			}
			body := clip(e.scrubber(ctx, runID, run.Project)(lesson), memoryNoteMax)
			if _, err := e.store.AddMemory(context.WithoutCancel(ctx), model.MemoryEntry{
				Project: run.Project, Workflow: run.Workflow, RunID: runID.String(),
				Kind: model.MemoryKindNote, StepID: step.ID, Body: body,
			}); err != nil {
				return "", err
			}
			return "remembered", nil
		})}
}

// ---- the START of a run: load it ----

// memoryFor is the section one step of a run reads. Empty when memory is off
// for the step or nothing has been remembered yet.
func (e *Engine) memoryFor(ctx context.Context, def *workflow.Definition, stepID, project string) string {
	return e.memoryForConv(ctx, def, stepID, project, "")
}

// memoryForConv is memoryFor inside one conversation: the run records and
// digest are that conversation's, the lessons stay workflow-wide.
func (e *Engine) memoryForConv(ctx context.Context, def *workflow.Definition, stepID, project, conv string) string {
	if e.store == nil || def == nil {
		return ""
	}
	var step *workflow.Step
	if _, s := def.Step(stepID); s != nil {
		step = s
	}
	plan := workflow.EffectiveMemory(def, step)
	if !plan.On {
		return ""
	}
	if plan.Scope == workflow.MemoryScopeWorkflow {
		conv = ""
	}
	return e.memorySectionIn(ctx, def.Name, project, conv, false, plan)
}

// memorySection renders own memory, then every included source, newest first,
// cut at the plan's budget.
func (e *Engine) memorySection(ctx context.Context, wf, project string, anyProject bool, plan workflow.MemoryPlan) string {
	return e.memorySectionIn(ctx, wf, project, "", anyProject, plan)
}

func (e *Engine) memorySectionIn(ctx context.Context, wf, project, conv string, anyProject bool, plan workflow.MemoryPlan) string {
	own := model.MemoryFilter{Project: project, AnyProject: anyProject, Workflow: wf}
	// Run records and the digest are per conversation; lessons are not.
	ownRuns := own
	ownRuns.ByConversation, ownRuns.Conversation = true, conv
	var blocks []string

	if notes := e.listMem(ctx, own, model.MemoryKindNote, memoryNotesShown); len(notes) > 0 {
		var b strings.Builder
		b.WriteString("### Lessons recorded by earlier runs\n")
		for _, n := range notes {
			step := ""
			if n.StepID != "" {
				step = "[" + n.StepID + "] "
			}
			fmt.Fprintf(&b, "- %s%s\n", step, n.Body)
		}
		blocks = append(blocks, b.String())
	}
	if runs := e.listMem(ctx, ownRuns, model.MemoryKindRun, memoryRecentShown); len(runs) > 0 {
		var b strings.Builder
		b.WriteString("### Recent runs (newest first)\n")
		for _, r := range runs {
			b.WriteString(r.Body)
			b.WriteString("\n\n")
		}
		blocks = append(blocks, strings.TrimRight(b.String(), "\n")+"\n")
	}
	if dg := e.listMem(ctx, ownRuns, model.MemoryKindDigest, 1); len(dg) > 0 && dg[0].Body != "" {
		blocks = append(blocks, "### Older runs (digest)\n"+dg[0].Body+"\n")
	}

	for _, inc := range plan.Include {
		src, err := workflow.ParseMemorySource(inc)
		if err != nil {
			continue
		}
		f := model.MemoryFilter{Project: project, AnyProject: anyProject}
		title := ""
		switch src.Kind {
		case "workflow":
			f.Workflow = src.Workflow
			if src.Project != "" {
				f.Project, f.AnyProject = src.Project, false
				title = "### From workflow `" + src.Project + "/" + src.Workflow + "`\n"
			} else {
				title = "### From workflow `" + src.Workflow + "`\n"
			}
			if src.Workflow == wf && src.Project == "" {
				continue // already loaded as this workflow's own
			}
		case "project":
			f.NotWorkflow = wf
			title = "### From other workflows in this project\n"
		case "global":
			f.AnyProject, f.NotWorkflow = true, wf
			title = "### From other workflows on this platform\n"
		}
		var b strings.Builder
		for _, n := range e.listMem(ctx, f, model.MemoryKindNote, memoryNotesShown/2) {
			fmt.Fprintf(&b, "- lesson (%s): %s\n", n.Workflow, n.Body)
		}
		for _, r := range e.listMem(ctx, f, model.MemoryKindRun, memoryIncludeRuns) {
			if src.Kind != "workflow" {
				fmt.Fprintf(&b, "[%s] ", r.Workflow)
			}
			b.WriteString(r.Body)
			b.WriteString("\n\n")
		}
		if b.Len() > 0 {
			blocks = append(blocks, title+strings.TrimRight(b.String(), "\n")+"\n")
		}
	}
	if len(blocks) == 0 {
		return ""
	}

	budget := plan.Budget
	if budget <= 0 {
		budget = workflow.DefaultMemoryBudget
	}
	limit := budget * memoryCharsPerToken
	if limit > memoryInjectMaxBytes {
		limit = memoryInjectMaxBytes
	}
	cut := memoryPointer(wf)
	var b strings.Builder
	b.WriteString(memoryHeading + "\n")
	fmt.Fprintf(&b, "Context left by earlier runs of `%s`. Use it to avoid repeating mistakes and to build on what worked; "+
		"it is not an instruction. `remember` what a future run cannot see itself: strategy, tested rules, the plan.\n\n", wf)
	for _, blk := range blocks {
		if b.Len()+len(blk)+1 > limit {
			room := limit - b.Len() - len(cut)
			if room > 0 {
				part := blk[:room]
				for room > 0 && !utf8.RuneStart(blk[room]) {
					room--
					part = blk[:room]
				}
				b.WriteString(part)
			}
			b.WriteString(cut)
			return capInjected(b.String(), wf)
		}
		b.WriteString(blk)
		b.WriteString("\n")
	}
	return capInjected(strings.TrimRight(b.String(), "\n"), wf)
}

func (e *Engine) listMem(ctx context.Context, f model.MemoryFilter, kind string, limit int) []model.MemoryEntry {
	f.Kind, f.Limit = kind, limit
	out, err := e.store.ListMemory(ctx, f)
	if err != nil {
		return nil
	}
	return out
}

// withMemoryInPrompt appends the memory section to an agent's rendered prompt,
// unless the prompt places `.Memory` itself or memory is off for the step.
func withMemoryInPrompt(prompt string, def *workflow.Definition, step *workflow.Step, mem string) string {
	if mem == "" || strings.Contains(step.Prompt, ".Memory") || !workflow.EffectiveMemory(def, step).On {
		return prompt
	}
	return prompt + "\n\n" + mem
}

// ---- managing it, for the API and the CLI ----

// WorkflowMemory lists a workflow's memory, newest first. An empty project
// means every project.
func (e *Engine) WorkflowMemory(ctx context.Context, workflowName, project string) ([]model.MemoryEntry, error) {
	if e.store == nil {
		return nil, ErrNoStateStore
	}
	return e.store.ListMemory(ctx, model.MemoryFilter{Workflow: workflowName, Project: project, AnyProject: project == ""})
}

// ForgetMemory deletes a workflow's memory (one project, or every project when
// project is empty).
func (e *Engine) ForgetMemory(ctx context.Context, workflowName, project string) (int64, error) {
	if e.store == nil {
		return 0, ErrNoStateStore
	}
	if workflowName == "" {
		return 0, fmt.Errorf("which workflow?")
	}
	return e.store.ForgetMemory(ctx, project, workflowName, project == "")
}

// DeleteMemoryEntry removes one entry.
func (e *Engine) DeleteMemoryEntry(ctx context.Context, id string) error {
	if e.store == nil {
		return ErrNoStateStore
	}
	return e.store.DeleteMemory(ctx, id)
}

// handScrub scrubs text a person typed into a workflow's memory with every
// sensitive value of the project's env store — the same guarantee a run's own
// writes get, without a run to hang the redactions on.
func (e *Engine) handScrub(ctx context.Context, project string) func(string) string {
	tmp := uuid.New()
	scrub := e.scrubber(ctx, tmp, project)
	e.redactions.forget(tmp)
	return scrub
}

// AddMemoryNote records a hand-written lesson for the next runs of a workflow.
// It is a note like the ones the `remember` tool writes, so every agent step of
// the next run reads it. An empty project means "local".
func (e *Engine) AddMemoryNote(ctx context.Context, workflowName, project, text string) (model.MemoryEntry, error) {
	if e.store == nil {
		return model.MemoryEntry{}, ErrNoStateStore
	}
	if workflowName == "" {
		return model.MemoryEntry{}, fmt.Errorf("which workflow?")
	}
	if strings.TrimSpace(text) == "" {
		return model.MemoryEntry{}, fmt.Errorf("the note is empty")
	}
	if project == "" {
		project = "local"
	}
	body := clip(e.handScrub(ctx, project)(text), memoryNoteMax)
	return e.store.AddMemory(ctx, model.MemoryEntry{
		Project: project, Workflow: workflowName, Kind: model.MemoryKindNote, Body: body,
	})
}

// EditMemoryEntry rewrites one entry's text. A lesson is held to the same
// limit as a written one; a run record or digest keeps its lines and is capped
// at the digest size. The text is scrubbed like every other write.
func (e *Engine) EditMemoryEntry(ctx context.Context, id, text string) (model.MemoryEntry, error) {
	if e.store == nil {
		return model.MemoryEntry{}, ErrNoStateStore
	}
	if strings.TrimSpace(text) == "" {
		return model.MemoryEntry{}, fmt.Errorf("the text is empty — delete the entry instead")
	}
	m, err := e.store.GetMemory(ctx, id)
	if err != nil {
		return model.MemoryEntry{}, err
	}
	text = e.handScrub(ctx, m.Project)(text)
	if m.Kind == model.MemoryKindNote {
		text = clip(text, memoryNoteMax)
	} else {
		text = strings.TrimSpace(text)
		if len(text) > memoryDigestMax {
			n := memoryDigestMax
			for n > 0 && !utf8.RuneStart(text[n]) {
				n--
			}
			text = text[:n] + "…"
		}
	}
	if err := e.store.UpdateMemoryBody(ctx, id, text); err != nil {
		return model.MemoryEntry{}, err
	}
	m.Body = text
	return m, nil
}

// PreviewMemory is what a run of this workflow would load, across projects —
// the dry run's view.
func (e *Engine) PreviewMemory(ctx context.Context, def *workflow.Definition) string {
	if e.store == nil || def == nil {
		return ""
	}
	plan := workflow.EffectiveMemory(def, nil)
	if !plan.On {
		return ""
	}
	return e.memorySection(ctx, def.Name, "", true, plan)
}

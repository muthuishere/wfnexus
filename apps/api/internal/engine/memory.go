package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// WORKFLOW MEMORY — toolnexus's memory hierarchy (SPEC §7E), one home per
// (project, workflow) on the server:
//
//	<memory root>/<project>/<workflow>/
//	├── MEMORY.md        short index, loaded into every agent step (capped)
//	├── DREAM.md         optional: what this workflow's dreams should focus on
//	└── memory/
//	    ├── <slug>.md    notes, linked from the index as [[slug]], read on demand
//	    └── log.md       append-only: what each dream changed and why
//
// Agent steps get toolnexus's `memory` tool (notes on) over the home. Writes
// land on disk and load at the NEXT step/run — never mid-step. A daily dream
// pass (toolnexus's Dreamer) verifies memory against the workflow's recent
// run transcripts, demotes detail out of the index, merges notes and logs
// every change. Everything here is toolnexus code; wfnexus only picks the dir,
// scrubs secrets on the way in, and supplies the transcripts.

// memoryHeading introduces the loaded index in a step's prompt.
const memoryHeading = "## Workflow memory (MEMORY.md — the index; use the `memory` tool: read note:<slug> for detail, add what a future run needs)"

// dreamRuns is how many recent runs a dream sees as ground truth.
const dreamRuns = 30

var memSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// MemoryRoot is where every workflow's memory home lives: beside the
// artifact folder (machine data, never the checkout). WFX_MEMORY_DIR overrides.
func (e *Engine) MemoryRoot() string {
	if d := os.Getenv("WFX_MEMORY_DIR"); d != "" {
		return d
	}
	return filepath.Join(filepath.Dir(e.cfg.ArtifactDir), "memory")
}

// MemoryDir is one workflow's memory home. project "" means local.
func (e *Engine) MemoryDir(project, wf string) (string, error) {
	if project == "" {
		project = "local"
	}
	if i := strings.LastIndex(wf, "/"); i >= 0 { // project/name keys
		wf = wf[i+1:]
	}
	if !memSegment.MatchString(project) || !memSegment.MatchString(wf) || strings.Contains(project+wf, "..") {
		return "", fmt.Errorf("invalid memory address %q/%q", project, wf)
	}
	return filepath.Join(e.MemoryRoot(), project, wf), nil
}

func memoryOn(def *workflow.Definition, step *workflow.Step) bool {
	if def != nil && def.Memory != nil && def.Memory.Off {
		return false
	}
	return step == nil || step.Memory == nil || !step.Memory.Off
}

// memoryFor is the MEMORY.md section one step reads: toolnexus's ComposeSoul
// with the index cap and the notes notice. Empty when off or nothing yet.
func (e *Engine) memoryFor(def *workflow.Definition, stepID, project string) string {
	if e.store == nil || def == nil {
		return ""
	}
	_, step := def.Step(stepID)
	if !memoryOn(def, step) {
		return ""
	}
	dir, err := e.MemoryDir(project, def.Name)
	if err != nil {
		return ""
	}
	soul, found, err := agents.ComposeSoulWith(dir, agents.ComposeSoulOptions{MemoryIndexCap: &agents.IndexCap{}, Notes: true})
	if err != nil || len(found) == 0 {
		return ""
	}
	return memoryHeading + "\n\n" + soul
}

// withMemoryInPrompt appends the memory section to an agent's rendered prompt,
// unless the prompt places `.Memory` itself.
func withMemoryInPrompt(prompt string, step *workflow.Step, mem string) string {
	if mem == "" || strings.Contains(step.Prompt, ".Memory") {
		return prompt
	}
	return prompt + "\n\n" + mem
}

// memoryTool is toolnexus's `memory` builtin (notes on) over this run's
// workflow home, with every written value scrubbed of the run's secrets. The
// address comes from the run row, never the model. Nil on a worker engine and
// for a step whose memory is off.
func (e *Engine) memoryTool(ctx context.Context, runID uuid.UUID, wfName string, step *workflow.Step) []tn.Tool {
	if e.store == nil || !memoryOn(e.defs[wfName], step) {
		return nil
	}
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return nil
	}
	dir, err := e.MemoryDir(run.Project, run.Workflow)
	if err != nil {
		return nil
	}
	t := agents.MemoryToolWith(dir, agents.MemoryToolOptions{Notes: true})
	inner := t.Execute
	t.Execute = func(args map[string]any, tc *tn.ToolContext) (tn.ToolResult, error) {
		scrub := e.scrubber(context.WithoutCancel(ctx), runID, run.Project)
		clean := map[string]any{}
		for k, v := range args {
			if s, ok := v.(string); ok && (k == "text" || k == "with") {
				v = scrub(s)
			}
			clean[k] = v
		}
		return inner(clean, tc)
	}
	return []tn.Tool{t}
}

// scrubber returns a function that removes every sensitive value this run
// knows about: its registered redactions plus every env-store value of its
// project.
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

// handScrub scrubs text without a run to hang the redactions on.
func (e *Engine) handScrub(ctx context.Context, project string) func(string) string {
	tmp := uuid.New()
	scrub := e.scrubber(ctx, tmp, project)
	e.redactions.forget(tmp)
	return scrub
}

// ---- managing it, for the API, CLI and UI ----

// MemoryNote is one note file.
type MemoryNote struct {
	Slug  string    `json:"slug"`
	Bytes int64     `json:"bytes"`
	MTime time.Time `json:"mtime"`
}

// MemoryView is a workflow's memory home at a glance.
type MemoryView struct {
	Workflow string       `json:"workflow"`
	Project  string       `json:"project"`
	Dir      string       `json:"dir"`
	Enabled  bool         `json:"enabled"`
	Index    string       `json:"index"`
	Dream    string       `json:"dream"`
	Log      string       `json:"log"`
	Notes    []MemoryNote `json:"notes"`
	// Loaded is the section an agent step reads (capped), exactly.
	Loaded string `json:"loaded"`
}

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// memoryFile maps a target (index | dream | note:<slug>) to its path.
func memoryFile(dir, target string) (string, error) {
	switch {
	case target == "index" || target == "self":
		return filepath.Join(dir, "MEMORY.md"), nil
	case target == "dream":
		return filepath.Join(dir, "DREAM.md"), nil
	case strings.HasPrefix(target, "note:"):
		slug := strings.TrimPrefix(target, "note:")
		if !slugRe.MatchString(slug) || strings.Contains(slug, "..") {
			return "", fmt.Errorf("invalid note slug %q", slug)
		}
		return filepath.Join(dir, "memory", slug+".md"), nil
	}
	return "", fmt.Errorf("unknown target %q: want index, dream or note:<slug>", target)
}

func readOr(p string) string {
	b, _ := os.ReadFile(p)
	return string(b)
}

// WorkflowMemory reads a workflow's memory home.
func (e *Engine) WorkflowMemory(ctx context.Context, wf, project string) (MemoryView, error) {
	if project == "" {
		project = "local"
	}
	dir, err := e.MemoryDir(project, wf)
	if err != nil {
		return MemoryView{}, err
	}
	v := MemoryView{Workflow: wf, Project: project, Dir: dir, Enabled: memoryOn(e.Definitions()[wf], nil),
		Index: readOr(filepath.Join(dir, "MEMORY.md")), Dream: readOr(filepath.Join(dir, "DREAM.md")),
		Log: readOr(filepath.Join(dir, "memory", "log.md")), Notes: []MemoryNote{}}
	entries, _ := os.ReadDir(filepath.Join(dir, "memory"))
	for _, en := range entries {
		slug, ok := strings.CutSuffix(en.Name(), ".md")
		if !ok || !slugRe.MatchString(slug) || !en.Type().IsRegular() {
			continue
		}
		if fi, err := en.Info(); err == nil {
			v.Notes = append(v.Notes, MemoryNote{Slug: slug, Bytes: fi.Size(), MTime: fi.ModTime()})
		}
	}
	sort.Slice(v.Notes, func(i, j int) bool { return v.Notes[i].Slug < v.Notes[j].Slug })
	if def := e.Definitions()[wf]; def != nil {
		v.Loaded = e.memoryFor(def, "", project)
	}
	return v, nil
}

// ReadMemory returns one file (index, dream or note:<slug>).
func (e *Engine) ReadMemory(wf, project, target string) (string, error) {
	dir, err := e.MemoryDir(project, wf)
	if err != nil {
		return "", err
	}
	p, err := memoryFile(dir, target)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("not found: %s", target)
	}
	return string(b), err
}

// WriteMemory replaces one file's whole text (scrubbed). note:log is the
// dream's audit trail and is not hand-editable.
func (e *Engine) WriteMemory(ctx context.Context, wf, project, target, text string) error {
	if target == "note:log" {
		return fmt.Errorf("memory/log.md is append-only")
	}
	dir, err := e.MemoryDir(project, wf)
	if err != nil {
		return err
	}
	p, err := memoryFile(dir, target)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(e.handScrub(ctx, project)(text)), 0o644)
}

// DeleteMemory removes one file, or — target "" — the whole home.
func (e *Engine) DeleteMemory(wf, project, target string) error {
	dir, err := e.MemoryDir(project, wf)
	if err != nil {
		return err
	}
	if target == "" {
		return os.RemoveAll(dir)
	}
	p, err := memoryFile(dir, target)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// PreviewMemory is what a run in the local project would load.
func (e *Engine) PreviewMemory(_ context.Context, def *workflow.Definition) string {
	return e.memoryFor(def, "", "local")
}

// ---- dreaming ----

// DreamResult is one dream pass.
type DreamResult struct {
	Workflow string `json:"workflow"`
	Project  string `json:"project"`
	Changed  bool   `json:"changed"`
	Reply    string `json:"reply"`
	Runs     int    `json:"runs"`
}

// transcripts loads the workflow's recent runs into a toolnexus conversation
// store, one conversation per run: each step's prompt and answer, scrubbed.
func (e *Engine) transcripts(ctx context.Context, project, wf string) (*tn.InMemoryConversationStore, int, error) {
	st := tn.NewInMemoryConversationStore()
	runs, err := e.store.WorkflowRuns(ctx, project, wf, dreamRuns)
	if err != nil {
		return nil, 0, err
	}
	scrub := e.handScrub(ctx, project)
	for _, r := range runs {
		steps, err := e.store.ListSteps(ctx, r.ID)
		if err != nil {
			continue
		}
		var msgs []any
		if len(r.Input) > 0 {
			msgs = append(msgs, map[string]any{"role": "user", "content": "run input: " + scrub(string(r.Input))})
		}
		for _, s := range steps {
			if s.Prompt != "" {
				msgs = append(msgs, map[string]any{"role": "user", "content": "[" + s.StepID + "] " + scrub(s.Prompt)})
			}
			ans := s.RawText
			if len(s.Output) > 0 && string(s.Output) != "null" {
				ans = strings.TrimSpace(ans + "\noutput: " + string(s.Output))
			}
			if s.Error != "" {
				ans = strings.TrimSpace(ans + "\nerror: " + s.Error)
			}
			if ans != "" {
				msgs = append(msgs, map[string]any{"role": "assistant", "content": "[" + s.StepID + "] " + scrub(ans)})
			}
		}
		msgs = append(msgs, map[string]any{"role": "assistant", "content": "run ended " + r.Status + " " + scrub(r.Error)})
		_ = st.Save(r.ID.String(), msgs)
	}
	return st, len(runs), nil
}

// Dream runs one toolnexus Dreamer pass over a workflow's memory home, with
// its recent runs as transcript_search ground truth, on the default provider.
func (e *Engine) Dream(ctx context.Context, wf, project string) (DreamResult, error) {
	if project == "" {
		project = "local"
	}
	out := DreamResult{Workflow: wf, Project: project}
	if e.store == nil {
		return out, ErrNoStateStore
	}
	if i := strings.LastIndex(wf, "/"); i >= 0 {
		wf = wf[i+1:]
	}
	dir, err := e.MemoryDir(project, wf)
	if err != nil {
		return out, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return out, err
	}
	store, n, err := e.transcripts(ctx, project, wf)
	if err != nil {
		return out, err
	}
	out.Runs = n
	ag, err := agents.Dreamer(dir, agents.DreamerOptions{Store: store, Name: wf + "-dreamer"})
	if err != nil {
		return out, err
	}
	prov, err := e.resolveLLMWithEnv(&workflow.Step{ID: "dream"}, dir, nil)
	if err != nil {
		return out, fmt.Errorf("dream provider: %w", err)
	}
	defer prov.Close()
	stop := context.AfterFunc(ctx, prov.Close)
	defer stop()
	transport := e.transport
	if transport == nil {
		transport = prov.Transport
	}
	before := readOr(filepath.Join(dir, "memory", "log.md"))
	res, _ := ag.Run(agents.Options{LLM: prov.LLM, Transport: transport}, agents.HeartbeatPrompt)
	out.Reply = strings.TrimSpace(res.Text)
	out.Changed = readOr(filepath.Join(dir, "memory", "log.md")) != before
	if res.IsError || res.Status == "error" {
		return out, fmt.Errorf("dream failed: %s", out.Reply)
	}
	return out, nil
}

// dreamAll dreams every memory home on disk, one at a time. Called daily by
// the scheduler; a failure is logged and the next home goes on.
func (e *Engine) dreamAll(ctx context.Context) {
	projects, _ := os.ReadDir(e.MemoryRoot())
	for _, p := range projects {
		wfs, _ := os.ReadDir(filepath.Join(e.MemoryRoot(), p.Name()))
		for _, w := range wfs {
			if _, err := os.Stat(filepath.Join(e.MemoryRoot(), p.Name(), w.Name(), "MEMORY.md")); err != nil {
				continue
			}
			r, err := e.Dream(ctx, w.Name(), p.Name())
			b, _ := json.Marshal(r)
			log.Printf("dream: %s/%s: %s err=%v", p.Name(), w.Name(), b, err)
		}
	}
}

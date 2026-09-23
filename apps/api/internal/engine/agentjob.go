package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// AN AGENT STEP ON ANOTHER MACHINE
//
// A `run:` step on a worker is a command. An agent step is the whole harness —
// the skills, the scoped tools, the sub-agent team, the guardrails, the turn
// budget and the `submit_output` schema gate — and all of it travels. What does
// NOT travel is the CLI: `provider: devin` means the `devin` on THAT machine's
// PATH, holding that machine's credential. That is the entire reason to put a
// worker on a box in the first place.
//
// The same function runs the agent here and there (step.go's runAgent), which
// is the only way the two can be guaranteed to mean the same thing. What
// differs is only what surrounds it: here a store and a blob bucket, there a
// sink that posts events back.

// BundledFile is one file of a skill, carried in the job. Skills travel as
// their files rather than as names because the worker has no skills directory
// and must not need one — otherwise adding a machine would mean deploying the
// platform's skill tree to it and keeping the two in step.
type BundledFile struct {
	// Path is relative to the bundle root, e.g. "reproduce-bug/SKILL.md".
	Path string `json:"path"`
	Mode uint32 `json:"mode,omitempty"`
	Body []byte `json:"body"`
}

// LLMDefaults is the process-wide provider, for a step that names none. Only
// the env var NAME travels; the worker reads its own value, so a key never
// leaves the machine that holds it.
type LLMDefaults struct {
	BaseURL   string `json:"baseUrl,omitempty"`
	Style     string `json:"style,omitempty"`
	Model     string `json:"model,omitempty"`
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
}

// AgentJob is one agent step, packed to execute somewhere else.
type AgentJob struct {
	RunID    string        `json:"runId"`
	Workflow string        `json:"workflow"`
	Step     workflow.Step `json:"step"`
	// Prompt is already rendered: the worker never sees a template, the run's
	// input, or another step's output beyond what this prompt quotes.
	Prompt   string            `json:"prompt"`
	BaseRef  string            `json:"baseRef,omitempty"`
	Provider *catalog.Provider `json:"provider,omitempty"`
	Defaults LLMDefaults       `json:"defaults"`
	Skills   []BundledFile     `json:"skills,omitempty"`
	MCP      map[string]any    `json:"mcp,omitempty"`
}

// AgentOutcome is what comes back. It carries the same facts a local step
// records, so the run's history cannot tell where the step executed.
type AgentOutcome struct {
	Output      map[string]any `json:"output,omitempty"`
	Pending     *tn.Request    `json:"pending,omitempty"`
	Turns       int            `json:"turns,omitempty"`
	RawText     string         `json:"rawText,omitempty"`
	TotalTokens int            `json:"totalTokens,omitempty"`
	// Diff is the worker's workspace diff, stored as this step's artifact here.
	// The work happened on that machine, so the evidence has to be carried back
	// or the run would record a step that changed nothing.
	Diff  []byte `json:"diff,omitempty"`
	Error string `json:"error,omitempty"`
}

// ---- packing, on the platform ----

// packAgentJob assembles everything the step needs to run elsewhere.
func (e *Engine) packAgentJob(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, prompt, baseRef string) (*AgentJob, error) {
	// A step that reaches back into this platform cannot be placed: the
	// catalogue, the validator and the dry run are THIS process. Refused here
	// rather than discovered as a missing tool mid-run.
	for _, t := range step.Tools {
		if skills.IsPlatformTool(t) {
			return nil, fmt.Errorf("step %s uses the platform tool %q, which only exists in the server process — it cannot run on %q",
				step.ID, t, step.RunsOn)
		}
	}
	// The step travels with its WHOLE environment resolved down to the file's
	// last word: the worker has no access to this platform's env store, so
	// what it is not sent, it does not have.
	placed := *step
	env, err := e.stepEnv(ctx, runID, step)
	if err != nil {
		return nil, err
	}
	placed.Env = env

	job := &AgentJob{
		RunID: runID.String(), Workflow: def.Name, Step: placed, Prompt: prompt, BaseRef: baseRef,
		Defaults: LLMDefaults{
			BaseURL: e.cfg.LLMBaseURL, Style: e.cfg.LLMStyle,
			Model: e.cfg.Model, APIKeyEnv: e.cfg.LLMAPIKeyEnv,
		},
	}
	if step.Provider != "" {
		p, err := e.catalog.Providers.Require(step.Provider)
		if err != nil {
			return nil, err
		}
		job.Provider = &p
	}

	// Every skill this step or its team may load, with its files.
	names := append([]string{}, step.Skills...)
	for _, m := range step.Team {
		names = append(names, m.Skills...)
	}
	files, err := e.bundleSkills(names)
	if err != nil {
		return nil, err
	}
	job.Skills = files

	if len(step.MCP) > 0 {
		raw, err := os.ReadFile(e.cfg.McpConfig)
		if err != nil {
			return nil, fmt.Errorf("mcp config: %w", err)
		}
		var all map[string]any
		if err := json.Unmarshal(raw, &all); err != nil {
			return nil, fmt.Errorf("mcp config: %w", err)
		}
		servers, _ := all["mcpServers"].(map[string]any)
		keep := map[string]any{}
		for _, name := range step.MCP {
			s, ok := servers[name]
			if !ok {
				return nil, fmt.Errorf("step %s: mcp server %q not in %s", step.ID, name, e.cfg.McpConfig)
			}
			keep[name] = s
		}
		job.MCP = map[string]any{"mcpServers": keep}
	}
	return job, nil
}

// maxSkillBundle caps what one job carries. A skill is documentation and a few
// scripts; a directory far past this is a mistake — a checked-in node_modules,
// a model file — and shipping it to every worker on every step would be a slow
// way to discover that.
const maxSkillBundle = 8 << 20

// bundleSkills reads each named skill's directory into the job.
func (e *Engine) bundleSkills(names []string) ([]BundledFile, error) {
	seen := map[string]bool{}
	var out []BundledFile
	var total int
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		sk, ok := e.skills.Get(n)
		if !ok {
			return nil, fmt.Errorf("skill %q is not in the registry", n)
		}
		dir := filepath.Dir(sk.Location)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			total += len(body)
			if total > maxSkillBundle {
				return fmt.Errorf("the skills for this step exceed %d bytes; a skill is documentation, not a payload", maxSkillBundle)
			}
			rel, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			out = append(out, BundledFile{
				// Always slash-separated: this path is written out on a machine
				// whose separator may not be this one's.
				Path: n + "/" + filepath.ToSlash(rel),
				Mode: uint32(info.Mode().Perm()),
				Body: body,
			})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("skill %q: %w", n, err)
		}
	}
	return out, nil
}

// ---- running it, on the worker ----

// NewWorkerEngine is an engine with no database and no bucket: everything it
// would persist is handed to sink instead, and posted back to the platform.
//
// It exists so that the agent a worker runs is the SAME agent, built by the
// same code from the same step. A second implementation on the worker would
// drift from this one, and the first anyone would know of it is a workflow
// behaving differently on one machine than another.
func NewWorkerEngine(sink func(kind string, payload any)) *Engine {
	cfg := config.Config{}
	cat, _ := catalog.Load("", "")
	return &Engine{
		cfg: cfg, catalog: cat, skills: skills.Load(),
		broker: newBroker(), running: map[uuid.UUID]context.CancelFunc{},
		slots: make(chan struct{}, 1), sink: sink,
	}
}

// RunAgentJob executes one packed agent step in workdir and returns what the
// platform needs to record it.
//
// Every failure it reports names THIS machine. "devin is not on PATH" read
// from a run log is a question — which machine? — and the answer is the whole
// reason the step was placed somewhere else.
func (e *Engine) RunAgentJob(ctx context.Context, job *AgentJob, workdir string) AgentOutcome {
	host, _ := os.Hostname()
	here := func(err error) AgentOutcome {
		msg := err.Error()
		if host != "" {
			msg += " (on " + host + ")"
		}
		return AgentOutcome{Error: msg, Diff: workspaceDiff(ctx, workdir, job.BaseRef)}
	}
	root, cleanup, err := writeSkillBundle(job.Skills)
	if err != nil {
		return here(err)
	}
	defer cleanup()

	// This process's registries are the job's, and only the job's: a worker
	// serves whatever step it is sent, so there is nothing standing to load.
	e.mu.Lock()
	e.skills = skills.Load(root...)
	e.catalog, _ = catalog.Load("", "")
	if job.Provider != nil {
		e.catalog.Providers.Add(*job.Provider, "job "+job.RunID)
	}
	e.cfg.LLMBaseURL, e.cfg.LLMStyle = job.Defaults.BaseURL, job.Defaults.Style
	e.cfg.Model, e.cfg.LLMAPIKeyEnv = job.Defaults.Model, job.Defaults.APIKeyEnv
	if len(job.MCP) > 0 {
		path, err := writeTemp("mcp-*.json", job.MCP)
		if err == nil {
			e.cfg.McpConfig = path
			defer os.Remove(path)
		}
	}
	e.mu.Unlock()

	runID, _ := uuid.Parse(job.RunID)
	step := job.Step
	res, err := e.runAgent(ctx, runID, job.Workflow, &step, job.Prompt, workdir, job.BaseRef)
	if err != nil {
		return here(err)
	}
	return AgentOutcome{
		Output: res.Output, Pending: res.Pending,
		Turns: res.Turns, RawText: res.RawText, TotalTokens: res.TotalTokens,
		Diff: workspaceDiff(ctx, workdir, job.BaseRef),
	}
}

// writeSkillBundle lays the job's skills out as a skills root the registry can
// discover, which is what toolnexus wants: directories, not blobs.
func writeSkillBundle(files []BundledFile) ([]string, func(), error) {
	if len(files) == 0 {
		return nil, func() {}, nil
	}
	root, err := os.MkdirTemp("", "wfx-skills-")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	for _, f := range files {
		// A path from elsewhere is untrusted: `..` in it would write outside the
		// bundle, which is a worker executing a file the platform did not mean
		// to send.
		clean := filepath.Clean(filepath.FromSlash(f.Path))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
			cleanup()
			return nil, func() {}, fmt.Errorf("skill file %q escapes the bundle", f.Path)
		}
		dest := filepath.Join(root, clean)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		mode := fs.FileMode(f.Mode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(dest, f.Body, mode); err != nil {
			cleanup()
			return nil, func() {}, err
		}
	}
	return []string{root}, cleanup, nil
}

func writeTemp(pattern string, v any) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(v); err != nil {
		return "", err
	}
	return f.Name(), nil
}

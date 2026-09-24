package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// WHERE A STEP RUNS
//
// A workflow says `runs-on: windows`. It does not say which machine, and it
// never holds an address. A machine JOINS by running one command with a token —
// the same shape as a GitHub self-hosted runner or a Jenkins node — and from
// then on it polls for work carrying its labels.
//
// The consequence is the point: the platform can be a container in Kubernetes
// and still run a step on a Windows box under somebody's desk, with the Devin
// or Claude CLI that is installed THERE, because nothing has to reach in. The
// worker reaches out.
//
// A label this process serves itself (cfg.RunnerLabels, default `local`) runs
// in process, exactly as it did before workers existed. A single-machine
// install therefore needs no worker, and adding one changes nothing that was
// already working.

// runnerTokenKey is where the registration token lives once generated.
const runnerTokenKey = "runner.token"

// how long a queued step waits for a worker holding its label to appear before
// the step fails. Long enough for a laptop to be opened; short enough that a
// typo in a label is a failure rather than a hang.
var workerWait = 30 * time.Minute

// servesLocally reports whether this process runs the label itself.
func (e *Engine) servesLocally(label string) bool {
	if label == "" {
		return true
	}
	for _, l := range e.cfg.RunnerLabels {
		if strings.EqualFold(l, label) {
			return true
		}
	}
	return false
}

// LocalLabels is what this process answers for, for the dashboard and doctor.
func (e *Engine) LocalLabels() []string { return e.cfg.RunnerLabels }

// RegistrationToken is the token a machine presents to join. It is configured
// (WFX_RUNNER_TOKEN) or generated once and kept, so the join command in the
// dashboard is the same one tomorrow.
func (e *Engine) RegistrationToken(ctx context.Context) (string, error) {
	if e.cfg.RunnerToken != "" {
		return e.cfg.RunnerToken, nil
	}
	return e.store.SettingOnce(ctx, runnerTokenKey, newToken)
}

// RotateRegistrationToken invalidates the old join command. Workers already
// registered keep working — they hold their own token, not this one.
func (e *Engine) RotateRegistrationToken(ctx context.Context) (string, error) {
	if e.cfg.RunnerToken != "" {
		return "", fmt.Errorf("the token is pinned by WFX_RUNNER_TOKEN; change it there")
	}
	t := newToken()
	return t, e.store.SetSetting(ctx, runnerTokenKey, t)
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "wfx_" + hex.EncodeToString(b)
}

// HashToken is how a token is stored and looked up. The value itself is never
// written to the database, a log or an event.
func HashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// JoinRequest is what a machine sends when it runs the join command.
type JoinRequest struct {
	Token   string   `json:"token"`
	Name    string   `json:"name"`
	Labels  []string `json:"labels"`
	OS      string   `json:"os"`
	Arch    string   `json:"arch"`
	Version string   `json:"version"`
}

// JoinResult is what it gets back: who it is, and the token it polls with.
type JoinResult struct {
	Worker *model.Worker `json:"worker"`
	Token  string        `json:"token"`
	// Shadowed are labels this worker offers that the platform already serves
	// in-process, so nothing carrying them will ever be queued to it.
	Shadowed []string `json:"shadowed,omitempty"`
}

// Join registers a machine into the pool.
func (e *Engine) Join(ctx context.Context, req JoinRequest) (*JoinResult, error) {
	want, err := e.RegistrationToken(ctx)
	if err != nil {
		return nil, err
	}
	if req.Token == "" || req.Token != want {
		return nil, ErrBadToken
	}
	if req.Name == "" {
		return nil, fmt.Errorf("a worker needs a name")
	}
	labels := req.Labels
	if len(labels) == 0 {
		// A worker with no labels serves nothing and would poll forever, which
		// looks like a broken platform rather than a missing flag.
		labels = []string{"self-hosted"}
	}
	// A label the platform serves ITSELF can never reach this machine: the step
	// runs in the server process before it is ever queued. Said at join time,
	// because the symptom otherwise is a worker that sits there, online and
	// idle, while the work happens somewhere else.
	var shadowed []string
	for _, l := range labels {
		if e.servesLocally(l) {
			shadowed = append(shadowed, l)
		}
	}
	w := &model.Worker{Name: req.Name, Labels: labels, OS: req.OS, Arch: req.Arch, Version: req.Version}
	tok := newToken()
	if err := e.store.RegisterWorker(ctx, w, HashToken(tok)); err != nil {
		return nil, err
	}
	return &JoinResult{Worker: w, Token: tok, Shadowed: shadowed}, nil
}

// ErrBadToken is a refused join or poll.
var ErrBadToken = fmt.Errorf("invalid token")

// AuthWorker resolves a polling worker from its own token.
func (e *Engine) AuthWorker(ctx context.Context, token string) (*model.Worker, error) {
	if token == "" {
		return nil, ErrBadToken
	}
	w, err := e.store.WorkerByToken(ctx, HashToken(token))
	if err != nil {
		return nil, ErrBadToken
	}
	_ = e.store.TouchWorker(ctx, w.ID)
	return w, nil
}

// Claim long-polls for work. Holding the request open is what keeps a worker's
// latency low without it hammering the server: one connection, no queue
// middleware, nothing to install on the worker's side.
func (e *Engine) Claim(ctx context.Context, w *model.Worker, wait time.Duration) (*model.Job, error) {
	deadline := time.Now().Add(wait)
	for {
		job, err := e.store.ClaimJob(ctx, w)
		if err != nil || job != nil {
			return job, err
		}
		if time.Now().After(deadline) {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// JobPayload is everything a worker needs to do one step's work on a machine
// that has never seen this repository. It is deliberately a command and a
// place to run it: a worker is a shell with a workspace, not a second engine.
type JobPayload struct {
	// Kind is "run" (a command) or "agent" (a whole agent step: skills, tools,
	// guardrails, budget and the schema gate, all carried in Agent).
	Kind    string `json:"kind,omitempty"`
	RunID   string `json:"runId"`
	StepID  string `json:"stepId"`
	Project string `json:"project"`
	// Command is already rendered — the worker never sees a template, an input
	// or a secret it was not sent.
	Command string `json:"command"`
	// Env is the step's env block AS WRITTEN — references and all. It is
	// resolved on the worker, against that machine's environment, so a
	// credential belonging to a build box is used there without the platform
	// ever holding it or putting it on the wire.
	//
	// Shell names the interpreter, or "" for the best one the worker has. This
	// is how the same workflow runs on Windows and Linux: the step says bash or
	// powershell or nothing, and the worker resolves it locally.
	Shell string `json:"shell,omitempty"`
	// RepoURL and Ref, when set, are the checkout the worker makes before
	// running. A remote machine has no workspace otherwise.
	RepoURL    string            `json:"repoUrl,omitempty"`
	Ref        string            `json:"ref,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	TimeoutSec int               `json:"timeoutSec,omitempty"`
	// Agent is set when Kind is "agent".
	Agent *AgentJob `json:"agent,omitempty"`
}

// IsAgent reports whether this job is a whole agent step rather than a command.
func (p JobPayload) IsAgent() bool { return p.Kind == "agent" && p.Agent != nil }

// JobResult is the worker's report. Its shape is exactly what a local `run:`
// step produces, so a gate reading `steps.build.ok` cannot tell — and must not
// care — whether the command ran here or on a Windows box in another building.
type JobResult struct {
	OK       bool   `json:"ok"`
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	// Error is set when the worker could not run the command at all, which is a
	// platform fault and not the command's verdict.
	Error string `json:"error,omitempty"`
	// Agent is the outcome of an agent job.
	Agent *AgentOutcome `json:"agent,omitempty"`
}

// runRemote queues a command for a worker holding `label` and waits for it.
func (e *Engine) runRemote(ctx context.Context, runID uuid.UUID, step *workflow.Step, cmd, label string, data workflow.TemplateData) (map[string]any, error) {
	env, err := e.stepEnv(ctx, runID, step)
	if err != nil {
		return nil, err
	}
	payload := JobPayload{
		RunID: runID.String(), StepID: step.ID, Command: cmd,
		Shell: step.Shell, TimeoutSec: step.TimeoutSec, Env: env,
	}
	if run, err := e.store.GetRun(ctx, runID); err == nil {
		payload.Project = run.Project
		payload.Ref = run.BaseRef
		if p, err := e.Project(ctx, run.Project); err == nil && p.URL != "" {
			payload.RepoURL = p.URL
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	job, err := e.store.EnqueueJob(ctx, runID, step.ID, label, raw)
	if err != nil {
		return nil, err
	}

	e.setStep(ctx, runID, step.ID, model.StepPatch{
		Status: str("running"), Prompt: str(cmd), StartedAt: now(), Error: str(""), ClearPending: true,
	})
	e.emit(ctx, runID, step.ID, "tool_call", map[string]any{
		"name": "run", "args": map[string]any{"command": cmd, "runsOn": label, "jobId": job.ID.String()},
	})

	res, err := e.awaitJob(ctx, job.ID, workerWait)
	if err != nil {
		_ = e.store.CancelJob(context.WithoutCancel(ctx), job.ID)
		return nil, err
	}
	if res.Error != "" {
		return nil, fmt.Errorf("worker could not run %q: %s", cmd, res.Error)
	}
	out := map[string]any{
		"ok": res.ExitCode == 0, "exitCode": res.ExitCode,
		"stdout": trim(res.Stdout, maxEventOutput), "stderr": trim(res.Stderr, maxEventOutput),
		"runsOn": label,
	}
	e.emit(ctx, runID, step.ID, "tool_result", map[string]any{
		"name": "run", "isError": res.ExitCode != 0,
		"output": trim(res.Stdout+res.Stderr, maxEventOutput),
	})
	return out, nil
}

// awaitJob waits for a worker to report. It polls the row rather than holding a
// channel so the wait survives the server restarting under it.
func (e *Engine) awaitJob(ctx context.Context, jobID uuid.UUID, wait time.Duration) (*JobResult, error) {
	deadline := time.Now().Add(wait)
	warned := false
	for {
		job, err := e.store.GetJob(ctx, jobID)
		if err != nil {
			return nil, err
		}
		if job.Status == "done" && len(job.Result) > 0 {
			var res JobResult
			if err := json.Unmarshal(job.Result, &res); err != nil {
				return nil, fmt.Errorf("unreadable worker result: %w", err)
			}
			return &res, nil
		}
		if !warned && job.Status == "queued" && time.Since(job.CreatedAt) > time.Minute {
			warned = true
			online, _ := e.store.OnlineLabels(ctx)
			if online[job.Label] == 0 {
				e.emit(ctx, job.RunID, job.StepID, "log", map[string]any{
					"message": fmt.Sprintf("waiting for a worker with the label %q — none is online. "+
						"Join one from the Workers page.", job.Label),
				})
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("no worker with the label %q took this step within %s", job.Label, wait)
		}
		// Anything leased by a worker that stopped polling goes back in the
		// queue, so an unplugged machine costs a delay rather than a run.
		_, _ = e.store.RequeueLostJobs(ctx)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// PublicURL is the address workers are told to reach this server on.
func (e *Engine) PublicURL() string { return e.cfg.PublicURL }

// runAgentRemotely executes a whole agent step on the machine holding its
// label, and records the result here exactly as a local step would be.
//
// The step's harness travels: its skills as files, its tool and MCP
// allowlists, its team, its guardrails, its budget and its output schema. The
// CLI does not — `provider: devin` resolves against THAT machine's PATH, with
// the credential that machine already holds. That asymmetry is the feature.
func (e *Engine) runAgentRemotely(ctx context.Context, runID uuid.UUID, def *workflow.Definition, step *workflow.Step, prompt string, data workflow.TemplateData) (stepResult, error) {
	job, err := e.packAgentJob(ctx, runID, def, step, prompt, data.BaseRef)
	if err != nil {
		return stepResult{}, err
	}
	payload := JobPayload{
		Kind: "agent", RunID: runID.String(), StepID: step.ID,
		Agent: job, TimeoutSec: step.TimeoutSec,
	}
	if run, err := e.store.GetRun(ctx, runID); err == nil {
		payload.Project = run.Project
		payload.Ref = run.BaseRef
		if p, err := e.Project(ctx, run.Project); err == nil && p.URL != "" {
			payload.RepoURL = p.URL
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return stepResult{}, err
	}
	queued, err := e.store.EnqueueJob(ctx, runID, step.ID, step.RunsOn, raw)
	if err != nil {
		return stepResult{}, err
	}

	e.setStep(ctx, runID, step.ID, model.StepPatch{
		Status: str("running"), Prompt: str(prompt), StartedAt: now(), Error: str(""), ClearPending: true,
	})
	e.emit(ctx, runID, step.ID, "log", map[string]any{
		"text": fmt.Sprintf("agent %s placed on %q — skills and tools travel with it; the CLI is that machine's own",
			step.ID, step.RunsOn),
	})

	res, err := e.awaitJob(ctx, queued.ID, workerWait)
	if err != nil {
		_ = e.store.CancelJob(context.WithoutCancel(ctx), queued.ID)
		return stepResult{}, err
	}
	if res.Agent == nil {
		if res.Error != "" {
			return stepResult{}, fmt.Errorf("worker: %s", res.Error)
		}
		return stepResult{}, fmt.Errorf("the worker returned no agent result for step %s", step.ID)
	}
	ag := res.Agent
	if ag.Error != "" {
		return stepResult{}, fmt.Errorf("%s", ag.Error)
	}

	// What the step cost is recorded here, because the step row is the
	// platform's. The worker only reports.
	e.setStep(ctx, runID, step.ID, model.StepPatch{
		Turns: intp(ag.Turns), RawText: str(ag.RawText),
		Usage: mustJSON(map[string]any{"totalTokens": ag.TotalTokens, "runsOn": step.RunsOn}),
	})
	e.saveRemoteArtifacts(ctx, runID, step.ID, ag)
	return stepResult{
		Output: ag.Output, Pending: ag.Pending,
		Turns: ag.Turns, RawText: ag.RawText, TotalTokens: ag.TotalTokens,
	}, nil
}

// saveRemoteArtifacts stores what the worker changed. The work happened on
// another disk, so without this the run would record a step that did nothing.
func (e *Engine) saveRemoteArtifacts(ctx context.Context, runID uuid.UUID, stepID string, ag *AgentOutcome) {
	if e.blob == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	put := func(name, ctype string, body []byte) {
		if len(body) == 0 {
			return
		}
		key := fmt.Sprintf("runs/%s/%s/%s", runID, stepID, name)
		if err := e.blob.Put(ctx, key, bytes.NewReader(body), int64(len(body)), ctype); err != nil {
			return
		}
		a := &model.Artifact{RunID: runID, StepID: stepID, Name: name, ObjectKey: key, ContentType: ctype, SizeBytes: int64(len(body))}
		if err := e.store.CreateArtifact(ctx, a); err == nil {
			e.emit(ctx, runID, stepID, "artifact", a)
		}
	}
	put("final.txt", "text/plain", []byte(ag.RawText))
	put("workspace.diff", "text/x-diff", ag.Diff)
}

// WorkerEvent is one line of a worker's activity, posted back while the step is
// still running so the live view is the same whichever machine it is on.
type WorkerEvent struct {
	StepID  string `json:"stepId"`
	Kind    string `json:"kind"`
	Payload any    `json:"payload"`
}

// IngestWorkerEvents appends a worker's activity to the run's log.
func (e *Engine) IngestWorkerEvents(ctx context.Context, runID uuid.UUID, evs []WorkerEvent) {
	for _, ev := range evs {
		kind := ev.Kind
		if kind == "" {
			kind = "log"
		}
		e.emit(ctx, runID, ev.StepID, kind, ev.Payload)
	}
}

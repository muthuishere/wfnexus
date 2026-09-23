package engine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
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
	Worker *store.Worker `json:"worker"`
	Token  string        `json:"token"`
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
	w := &store.Worker{Name: req.Name, Labels: labels, OS: req.OS, Arch: req.Arch, Version: req.Version}
	tok := newToken()
	if err := e.store.RegisterWorker(ctx, w, HashToken(tok)); err != nil {
		return nil, err
	}
	return &JoinResult{Worker: w, Token: tok}, nil
}

// ErrBadToken is a refused join or poll.
var ErrBadToken = fmt.Errorf("invalid token")

// AuthWorker resolves a polling worker from its own token.
func (e *Engine) AuthWorker(ctx context.Context, token string) (*store.Worker, error) {
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
func (e *Engine) Claim(ctx context.Context, w *store.Worker, wait time.Duration) (*store.Job, error) {
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
	RunID   string `json:"runId"`
	StepID  string `json:"stepId"`
	Project string `json:"project"`
	// Command is already rendered — the worker never sees a template, an input
	// or a secret it was not sent.
	Command string `json:"command"`
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
}

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
}

// runRemote queues a command for a worker holding `label` and waits for it.
func (e *Engine) runRemote(ctx context.Context, runID uuid.UUID, step *workflow.Step, cmd, label string, data workflow.TemplateData) (map[string]any, error) {
	payload := JobPayload{
		RunID: runID.String(), StepID: step.ID, Command: cmd,
		Shell: step.Shell, TimeoutSec: step.TimeoutSec,
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

	e.setStep(ctx, runID, step.ID, store.StepPatch{
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

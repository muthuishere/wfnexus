// Package model holds the platform's persisted records as plain data.
//
// WHY THESE ARE NOT IN internal/store
//
// The engine runs a step. It does not care whether the row it patches lands in
// Postgres, in SQLite, or nowhere at all — and on a worker it lands nowhere at
// all, because a worker has no database. But a record has to be NAMED for the
// engine to talk about it, and while the names lived in internal/store, naming
// one dragged in database/sql, the pgx and lib/pq drivers, golang-migrate and
// its two database backends.
//
// That is how wfx-runner — a process that opens no database, by design —
// came to ship a Postgres driver and a migration engine it can never run.
//
// So the records live here: uuid, time and encoding/json, nothing else, ever.
// internal/store aliases every one of them, so the SQL layer and the API read
// exactly as they did; the engine names them through this package and is free
// of the drivers.
package model

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ---- runs and steps ----

type Run struct {
	ID uuid.UUID `json:"id"`
	// Project is the repository this run belongs to — "local" for the
	// platform's own workflows directory.
	Project     string          `json:"project"`
	Workflow    string          `json:"workflow"`
	Status      string          `json:"status"`
	Input       json.RawMessage `json:"input"`
	CurrentStep string          `json:"currentStep"`
	BaseRef     string          `json:"baseRef"`
	Error       string          `json:"error"`
	// StartedAt is when the run actually LEFT the queue — nil while it is still
	// queued. CreatedAt is only when the row was written, so a consumer that
	// wants elapsed time needs this one (migration 000011).
	StartedAt *time.Time `json:"startedAt"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt time.Time  `json:"updatedAt"`
}

type StepRun struct {
	ID       uuid.UUID       `json:"id"`
	RunID    uuid.UUID       `json:"runId"`
	StepID   string          `json:"stepId"`
	Position int             `json:"position"`
	Status   string          `json:"status"`
	Attempts int             `json:"attempts"`
	Turns    int             `json:"turns"`
	Prompt   string          `json:"prompt"`
	Output   json.RawMessage `json:"output"`
	RawText  string          `json:"rawText"`
	Error    string          `json:"error"`
	Usage    json.RawMessage `json:"usage"`
	// Pending is the toolnexus suspension Request this step parked on, if any.
	Pending json.RawMessage `json:"pending"`
	// Decision is the classifier answer set for this step, if it declared one.
	Decision   json.RawMessage `json:"decision"`
	StartedAt  *time.Time      `json:"startedAt"`
	FinishedAt *time.Time      `json:"finishedAt"`
	// ResolvedBy / ResolvedAt / Resolution / ResolutionReason are the audit
	// fact for a pause somebody answered (ADR 0021): who, when, and what they
	// decided. Resolution is approved|rejected|answered|declined|cancelled|
	// expired — the last three being toolnexus's Answer.Reason vocabulary, so a
	// decline and a timeout are different events rather than the same string.
	ResolvedBy       string     `json:"resolvedBy"`
	ResolvedAt       *time.Time `json:"resolvedAt"`
	Resolution       string     `json:"resolution"`
	ResolutionReason string     `json:"resolutionReason"`
}

// StepPatch is a partial update to one step row: a nil field is left alone.
type StepPatch struct {
	Status   *string
	Attempts *int
	Turns    *int
	Prompt   *string
	Output   json.RawMessage
	RawText  *string
	Error    *string
	Usage    json.RawMessage
	Pending  json.RawMessage
	Decision json.RawMessage
	// ClearPending wipes a stored suspension (set when the step re-runs).
	ClearPending bool
	StartedAt    *time.Time
	FinishedAt   *time.Time

	ResolvedBy       *string
	ResolvedAt       *time.Time
	Resolution       *string
	ResolutionReason *string
}

type Event struct {
	ID        int64           `json:"id"`
	RunID     uuid.UUID       `json:"runId"`
	StepID    string          `json:"stepId"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

type Artifact struct {
	ID          uuid.UUID `json:"id"`
	RunID       uuid.UUID `json:"runId"`
	StepID      string    `json:"stepId"`
	Name        string    `json:"name"`
	ObjectKey   string    `json:"objectKey"`
	ContentType string    `json:"contentType"`
	SizeBytes   int64     `json:"sizeBytes"`
	CreatedAt   time.Time `json:"createdAt"`
}

// ProjectActivity summarises one project's run history for the dashboard.
type ProjectActivity struct {
	Runs        int
	LastStatus  string
	LastCreated time.Time
}

// ---- the environment store ----

const (
	ScopeSystem  = "system"
	ScopeProject = "project"
)

// EnvVar is one entry as the API returns it: never with its value, when secret.
type EnvVar struct {
	Scope     string `json:"scope"`
	ScopeName string `json:"scopeName,omitempty"`
	Key       string `json:"key"`
	Secret    bool   `json:"secret"`
	// Value is present ONLY for a non-secret. A secret's value leaves the
	// database exactly once, into the process that runs the step.
	Value     string    `json:"value,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ---- the state store ----

// State is what a workflow REMEMBERS between runs: four separate namespaces of
// plain string key/value, narrowest first.
//
//	step      this step of this workflow, across runs
//	workflow  this workflow, across runs
//	project   every workflow in one repository
//	global    everything on this platform
//
// It is NOT a secret store. Values are stored in PLAINTEXT — unlike env_vars,
// which is sealed. A token belongs in `wfx env`, never here.
const (
	StateScopeStep     = "step"
	StateScopeWorkflow = "workflow"
	StateScopeProject  = "project"
	StateScopeGlobal   = "global"
)

// StateScopes is the whole vocabulary, narrowest to widest.
var StateScopes = []string{StateScopeStep, StateScopeWorkflow, StateScopeProject, StateScopeGlobal}

// StateVar is one entry as the API returns it. The value is always present:
// state is plaintext by design.
type StateVar struct {
	Scope     string    `json:"scope"`
	ScopeName string    `json:"scopeName,omitempty"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Sealer is the encryption the store writes through. It is an interface so the
// store does not import a key: whoever holds the key passes it in.
type Sealer interface {
	Seal(plain string) ([]byte, error)
	Open(sealed []byte) (string, error)
}

// ---- workers and their jobs ----

// A worker is a machine that joined the pool. It is identified by its LABELS,
// never by its address: the platform never connects to a worker, the worker
// connects to the platform and asks for work. That direction is what lets a
// laptop behind NAT, a Windows VM on someone's desk and a Kubernetes pod all be
// the same kind of thing, and it is why joining is one command with a token.
type Worker struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Labels   []string  `json:"labels"`
	OS       string    `json:"os"`
	Arch     string    `json:"arch"`
	Version  string    `json:"version"`
	LastSeen time.Time `json:"lastSeen"`
	Created  time.Time `json:"createdAt"`
}

// Online reports whether this worker has been heard from recently enough to
// send work to. A worker does not log out; it stops polling.
func (w *Worker) Online() bool { return time.Since(w.LastSeen) < 90*time.Second }

// Status is what a listing shows.
func (w *Worker) Status() string {
	if w.Online() {
		return "online"
	}
	return "offline"
}

// A Job is one step's work waiting for whoever holds its label.
type Job struct {
	ID         uuid.UUID       `json:"id"`
	RunID      uuid.UUID       `json:"runId"`
	StepID     string          `json:"stepId"`
	Label      string          `json:"label"`
	WorkerID   *uuid.UUID      `json:"workerId,omitempty"`
	Status     string          `json:"status"`
	Payload    json.RawMessage `json:"payload"`
	Result     json.RawMessage `json:"result,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
	LeasedAt   *time.Time      `json:"leasedAt,omitempty"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
}

package engine

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
)

// WHAT THE ENGINE NEEDS OF THE WORLD, SAID BY THE ENGINE
//
// The engine used to hold a *store.Store and a blob.Store. Both are real
// things a deployment has, and neither is something a WORKER has: wfx-runner
// opens no database and writes to no bucket — it runs one packed agent step
// and posts the result back over HTTP.
//
// While the engine named the concrete types, the worker's binary contained
// them anyway. Importing internal/store pulled in database/sql, the pgx and
// lib/pq drivers, golang-migrate and its Postgres and SQLite backends;
// importing internal/blob pulled in minio-go. wfx-runner shipped a Postgres
// driver, a migration engine and an S3 client, and opened none of them.
//
// So the boundary is declared HERE, at the consumer, as the narrowest set of
// calls the engine actually makes. *store.Store satisfies Store and
// blob.Store satisfies Artifacts without either package knowing this file
// exists, wiring happens in the server's main, and the worker passes nothing.
//
// The point of doing it this way rather than by copying the step loop into the
// worker is that there IS only one step loop. NewWorkerEngine builds the same
// *Engine, runs the same runAgent, from the same step. Two implementations
// would drift, and the first anyone would hear of it is a workflow behaving
// differently on one machine than another.

// Store is every persistence call the engine makes. Implemented by
// *store.Store; nil on a worker engine, where the guarded paths that would
// reach it are the ones a worker never takes.
type Store interface {
	// runs
	CreateRun(ctx context.Context, project, workflow string, input json.RawMessage) (*model.Run, error)
	GetRun(ctx context.Context, id uuid.UUID) (*model.Run, error)
	UpdateRun(ctx context.Context, id uuid.UUID, status, currentStep, errMsg string) error
	UpdateRunInput(ctx context.Context, id uuid.UUID, input json.RawMessage) error
	SetBaseRef(ctx context.Context, id uuid.UUID, ref string) error
	ProjectRunActivity(ctx context.Context) (map[string]model.ProjectActivity, error)

	// steps
	EnsureStep(ctx context.Context, runID uuid.UUID, stepID string, position int) error
	GetStep(ctx context.Context, runID uuid.UUID, stepID string) (*model.StepRun, error)
	ListSteps(ctx context.Context, runID uuid.UUID) ([]*model.StepRun, error)
	PatchStep(ctx context.Context, runID uuid.UUID, stepID string, p model.StepPatch) error
	ResetStepsFrom(ctx context.Context, runID uuid.UUID, position int) error

	// events and artifacts
	AppendEvent(ctx context.Context, runID uuid.UUID, stepID, kind string, payload any) (*model.Event, error)
	CreateArtifact(ctx context.Context, a *model.Artifact) error

	// the environment store
	PutEnvVar(ctx context.Context, box model.Sealer, scope, scopeName, key, value string, secret bool) error
	DeleteEnvVar(ctx context.Context, scope, scopeName, key string) error
	ListEnvVars(ctx context.Context, box model.Sealer, scope, scopeName string) ([]model.EnvVar, error)
	EnvFor(ctx context.Context, box model.Sealer, scope, scopeName string) (map[string]string, error)

	// settings
	SetSetting(ctx context.Context, key, value string) error
	SettingOnce(ctx context.Context, key string, gen func() string) (string, error)

	// workers and their job queue
	RegisterWorker(ctx context.Context, w *model.Worker, tokenHash string) error
	WorkerByToken(ctx context.Context, tokenHash string) (*model.Worker, error)
	TouchWorker(ctx context.Context, id uuid.UUID) error
	OnlineLabels(ctx context.Context) (map[string]int, error)
	EnqueueJob(ctx context.Context, runID uuid.UUID, stepID, label string, payload json.RawMessage) (*model.Job, error)
	ClaimJob(ctx context.Context, w *model.Worker) (*model.Job, error)
	GetJob(ctx context.Context, id uuid.UUID) (*model.Job, error)
	CancelJob(ctx context.Context, id uuid.UUID) error
	RequeueLostJobs(ctx context.Context) (int64, error)
}

// Artifacts is the one call the engine makes of artifact storage: it writes a
// step's transcript and its workspace diff. Reading them back is the API's
// job, and the API holds the concrete store.
//
// Both blob drivers — the bucket and the plain folder — satisfy this, and the
// engine no longer needs to know that minio exists.
type Artifacts interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
}

// PresignedArtifacts is the optional extra a bucket has and a folder does not.
// Nothing in the engine needs it; it is named here only so a caller holding an
// Artifacts can ask.
type PresignedArtifacts interface {
	Artifacts
	PresignedGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

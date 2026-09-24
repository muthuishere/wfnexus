// Package store is the persistence layer. It speaks one set of Postgres-dialect
// queries to either Postgres (pgx through database/sql) or SQLite (the cgo-free
// modernc driver), chosen by the configured storage driver; db.go holds the
// small dialect that rewrites the difference.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/muthuishere/wfnexus/apps/api/migrations"
)

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
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
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

type Store struct {
	db *sql.DB
	d  dialect
}

// rowScanner is what *sql.Row and *sql.Rows have in common, so one scan
// function serves both the single-row and the listing path.
type rowScanner interface{ Scan(dest ...any) error }

// Open connects with the named driver. "sqlite" takes a FILE PATH as its dsn,
// not a URL — that is what a local config writes and what a user can delete.
func Open(ctx context.Context, driver, dsn string) (*Store, error) {
	d := dialect{name: normalizeDriver(driver)}
	name, conn := d.name, dsn
	if d.isSQLite() {
		name = "sqlite"
		if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
			return nil, fmt.Errorf("sqlite dir: %w", err)
		}
		// Busy timeout and WAL: the engine writes from several goroutines at
		// once, and without them a concurrent run fails with SQLITE_BUSY
		// instead of waiting a few milliseconds.
		conn = dsn + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	} else {
		name = "pgx"
	}
	db, err := sql.Open(name, conn)
	if err != nil {
		return nil, err
	}
	if d.isSQLite() {
		// One writer. SQLite serialises writes anyway; a pool of them only
		// converts the serialisation into lock errors.
		db.SetMaxOpenConns(1)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s ping: %w", d.name, err)
	}
	return &Store{db: db, d: d}, nil
}

func (s *Store) Close() { s.db.Close() }

// DB exposes the handle for tests that need to assert on what is actually
// stored — that a secret is ciphertext in the table, for instance.
func (s *Store) DB() *sql.DB { return s.db }

func (s *Store) qrow(ctx context.Context, q string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.d.q(q), args...)
}

func (s *Store) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.d.q(q), args...)
}

func (s *Store) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.d.q(q), args...)
}

func normalizeDriver(driver string) string {
	if strings.EqualFold(driver, driverSQLite) || strings.EqualFold(driver, "sqlite3") {
		return driverSQLite
	}
	return driverPostgres
}

// Migrate applies the embedded SQL tree for this driver. Idempotent.
//
// The two dialects have SEPARATE migration sets, because the applied Postgres
// files use uuid, jsonb, text[] and bigserial, none of which SQLite has. The
// Postgres set stays exactly where it was and is never edited; the SQLite set
// lives under migrations/sqlite/ with its own version line and its own
// schema_migrations table inside the SQLite file.
func Migrate(driver, dsn string) error {
	dir, url := ".", dsn
	if normalizeDriver(driver) == driverSQLite {
		dir = "sqlite"
		if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
			return fmt.Errorf("sqlite dir: %w", err)
		}
		url = "sqlite://" + dsn + "?_pragma=busy_timeout(5000)"
	}
	src, err := iofs.New(migrations.FS, dir)
	if err != nil {
		return err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, url)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// ---- runs ----

// CreateRun records a run against its PROJECT. project → workflow → runs is
// the same hierarchy GitHub Actions has, and the project is not optional: a run
// that belongs to nothing cannot be found again once there is more than one
// repository.
func (s *Store) CreateRun(ctx context.Context, project, workflow string, input json.RawMessage) (*Run, error) {
	if project == "" {
		project = "local"
	}
	r := &Run{ID: uuid.New(), Project: project, Workflow: workflow, Status: "queued", Input: input}
	err := s.qrow(ctx,
		`INSERT INTO workflow_runs (id, project, workflow, status, input) VALUES ($1,$2,$3,$4,$5) RETURNING created_at, updated_at`,
		r.ID, r.Project, r.Workflow, r.Status, jsonArg(r.Input)).Scan(&r.CreatedAt, &r.UpdatedAt)
	return r, err
}

const runCols = `id, project, workflow, status, input, current_step, base_ref, error, created_at, updated_at`

func scanRun(row rowScanner) (*Run, error) {
	r := &Run{}
	err := row.Scan(&r.ID, &r.Project, &r.Workflow, &r.Status, rawJSON{&r.Input}, &r.CurrentStep, &r.BaseRef, &r.Error, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

func (s *Store) GetRun(ctx context.Context, id uuid.UUID) (*Run, error) {
	return scanRun(s.qrow(ctx, `SELECT `+runCols+` FROM workflow_runs WHERE id=$1`, id))
}

// ProjectActivity is the per-project run summary a dashboard needs: how many,
// and how the newest one went.
//
// Computed in SQL rather than by listing runs and counting them — the listing
// is capped, so counting it reported the cap as the number of runs and a
// project with 900 runs and one with 500 looked identical.
type ProjectActivity struct {
	Runs        int
	LastStatus  string
	LastCreated time.Time
}

// ProjectRunActivity returns one entry per project that has ever run anything.
func (s *Store) ProjectRunActivity(ctx context.Context) (map[string]ProjectActivity, error) {
	q := `
		SELECT r.project, r.n, l.status, l.created_at
		FROM (SELECT project, count(*) AS n FROM workflow_runs GROUP BY project) r
		JOIN LATERAL (
			SELECT status, created_at FROM workflow_runs
			WHERE project = r.project ORDER BY created_at DESC LIMIT 1
		) l ON true`
	if s.d.isSQLite() {
		// No LATERAL. Correlated scalar subqueries say the same thing, and the
		// counting still happens in SQL — which is the point of this query.
		q = `
		SELECT project, count(*),
		       (SELECT status FROM workflow_runs i
		        WHERE i.project = o.project ORDER BY created_at DESC LIMIT 1),
		       (SELECT created_at FROM workflow_runs i
		        WHERE i.project = o.project ORDER BY created_at DESC LIMIT 1)
		FROM workflow_runs o GROUP BY project`
	}
	rows, err := s.query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ProjectActivity{}
	for rows.Next() {
		var name string
		var a ProjectActivity
		if err := rows.Scan(&name, &a.Runs, &a.LastStatus, &a.LastCreated); err != nil {
			return nil, err
		}
		out[name] = a
	}
	return out, rows.Err()
}

// RunFilter narrows a listing to a project, a workflow, or both — the two axes
// the hierarchy actually has.
type RunFilter struct {
	Project  string
	Workflow string
	Limit    int
}

func (s *Store) ListRuns(ctx context.Context, limit int) ([]*Run, error) {
	return s.FindRuns(ctx, RunFilter{Limit: limit})
}

// FindRuns lists runs newest first, optionally within one project or workflow.
func (s *Store) FindRuns(ctx context.Context, f RunFilter) ([]*Run, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	q := `SELECT ` + runCols + ` FROM workflow_runs`
	var where []string
	var args []any
	if f.Project != "" {
		args = append(args, f.Project)
		where = append(where, fmt.Sprintf("project=$%d", len(args)))
	}
	if f.Workflow != "" {
		args = append(args, f.Workflow)
		where = append(where, fmt.Sprintf("workflow=$%d", len(args)))
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, f.Limit)
	q += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d", len(args))

	rows, err := s.query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpdateRun(ctx context.Context, id uuid.UUID, status, currentStep, errMsg string) error {
	_, err := s.exec(ctx,
		`UPDATE workflow_runs SET status=$2, current_step=$3, error=$4, updated_at=now() WHERE id=$1`,
		id, status, currentStep, errMsg)
	return err
}

func (s *Store) UpdateRunInput(ctx context.Context, id uuid.UUID, input json.RawMessage) error {
	_, err := s.exec(ctx, `UPDATE workflow_runs SET input=$2, updated_at=now() WHERE id=$1`, id, jsonArg(input))
	return err
}

// SetBaseRef records the commit a run started from, once. Later resumes read it
// back so every step's diff is measured from the same point.
func (s *Store) SetBaseRef(ctx context.Context, id uuid.UUID, ref string) error {
	_, err := s.exec(ctx, `UPDATE workflow_runs SET base_ref=$2 WHERE id=$1 AND base_ref=''`, id, ref)
	return err
}

// ---- step runs ----

const stepCols = `id, run_id, step_id, position, status, attempts, turns, prompt, output, raw_text, error, usage, pending, decision, started_at, finished_at`

func scanStep(row rowScanner) (*StepRun, error) {
	st := &StepRun{}
	err := row.Scan(&st.ID, &st.RunID, &st.StepID, &st.Position, &st.Status, &st.Attempts, &st.Turns,
		&st.Prompt, rawJSON{&st.Output}, &st.RawText, &st.Error, rawJSON{&st.Usage},
		rawJSON{&st.Pending}, rawJSON{&st.Decision}, &st.StartedAt, &st.FinishedAt)
	return st, err
}

// EnsureStep creates the pending row for (run, step) if it does not exist yet.
func (s *Store) EnsureStep(ctx context.Context, runID uuid.UUID, stepID string, position int) error {
	_, err := s.exec(ctx,
		`INSERT INTO step_runs (id, run_id, step_id, position, status) VALUES ($1,$2,$3,$4,'pending')
		 ON CONFLICT (run_id, step_id) DO NOTHING`, uuid.New(), runID, stepID, position)
	return err
}

func (s *Store) GetStep(ctx context.Context, runID uuid.UUID, stepID string) (*StepRun, error) {
	return scanStep(s.qrow(ctx, `SELECT `+stepCols+` FROM step_runs WHERE run_id=$1 AND step_id=$2`, runID, stepID))
}

func (s *Store) ListSteps(ctx context.Context, runID uuid.UUID) ([]*StepRun, error) {
	rows, err := s.query(ctx, `SELECT `+stepCols+` FROM step_runs WHERE run_id=$1 ORDER BY position`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*StepRun
	for rows.Next() {
		st, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

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
}

func (s *Store) PatchStep(ctx context.Context, runID uuid.UUID, stepID string, p StepPatch) error {
	_, err := s.exec(ctx, `UPDATE step_runs SET
		status      = COALESCE($3, status),
		attempts    = COALESCE($4, attempts),
		turns       = COALESCE($5, turns),
		prompt      = COALESCE($6, prompt),
		output      = COALESCE($7, output),
		raw_text    = COALESCE($8, raw_text),
		error       = COALESCE($9, error),
		usage       = COALESCE($10, usage),
		pending     = CASE WHEN $11::text = 'clear' THEN NULL ELSE COALESCE($12, pending) END,
		decision    = COALESCE($13, decision),
		started_at  = COALESCE($14, started_at),
		finished_at = COALESCE($15, finished_at)
		WHERE run_id=$1 AND step_id=$2`,
		runID, stepID, p.Status, p.Attempts, p.Turns, p.Prompt, nullableJSON(p.Output), p.RawText, p.Error,
		nullableJSON(p.Usage), pendingOp(p), nullableJSON(p.Pending), nullableJSON(p.Decision), p.StartedAt, p.FinishedAt)
	return err
}

// ResetStepsFrom marks the given step and everything after it pending again (used by retry / re-run).
func (s *Store) ResetStepsFrom(ctx context.Context, runID uuid.UUID, position int) error {
	_, err := s.exec(ctx, `UPDATE step_runs SET status='pending', output=NULL, raw_text='', error='',
		pending=NULL, decision=NULL, started_at=NULL, finished_at=NULL WHERE run_id=$1 AND position>=$2`, runID, position)
	return err
}

// pendingOp distinguishes "leave pending alone" from "clear it": a step that
// resumes must drop the question it was parked on.
func pendingOp(p StepPatch) string {
	if p.ClearPending {
		return "clear"
	}
	return ""
}

func nullableJSON(j json.RawMessage) any { return jsonArg(j) }

// ---- events ----

func (s *Store) AppendEvent(ctx context.Context, runID uuid.UUID, stepID, kind string, payload any) (*Event, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	ev := &Event{RunID: runID, StepID: stepID, Kind: kind, Payload: raw}
	err = s.qrow(ctx,
		`INSERT INTO run_events (run_id, step_id, kind, payload) VALUES ($1,$2,$3,$4) RETURNING id, created_at`,
		runID, stepID, kind, jsonArg(raw)).Scan(&ev.ID, &ev.CreatedAt)
	return ev, err
}

func (s *Store) ListEvents(ctx context.Context, runID uuid.UUID, afterID int64, limit int) ([]*Event, error) {
	rows, err := s.query(ctx,
		`SELECT id, run_id, step_id, kind, payload, created_at FROM run_events WHERE run_id=$1 AND id>$2 ORDER BY id LIMIT $3`,
		runID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		ev := &Event{}
		if err := rows.Scan(&ev.ID, &ev.RunID, &ev.StepID, &ev.Kind, rawJSON{&ev.Payload}, &ev.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ---- artifacts ----

func (s *Store) CreateArtifact(ctx context.Context, a *Artifact) error {
	a.ID = uuid.New()
	return s.qrow(ctx,
		`INSERT INTO artifacts (id, run_id, step_id, name, object_key, content_type, size_bytes) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING created_at`,
		a.ID, a.RunID, a.StepID, a.Name, a.ObjectKey, a.ContentType, a.SizeBytes).Scan(&a.CreatedAt)
}

func (s *Store) ListArtifacts(ctx context.Context, runID uuid.UUID) ([]*Artifact, error) {
	rows, err := s.query(ctx,
		`SELECT id, run_id, step_id, name, object_key, content_type, size_bytes, created_at FROM artifacts WHERE run_id=$1 ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Artifact
	for rows.Next() {
		a := &Artifact{}
		if err := rows.Scan(&a.ID, &a.RunID, &a.StepID, &a.Name, &a.ObjectKey, &a.ContentType, &a.SizeBytes, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

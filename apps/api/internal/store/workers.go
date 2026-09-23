package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

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

// ---- settings ----

// Setting reads a stored value, "" when absent.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting stores a value, replacing any previous one.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO settings (key,value) VALUES ($1,$2) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`,
		key, value)
	return err
}

// SettingOnce returns the stored value, generating and storing gen() the first
// time. Two processes racing to boot cannot end up with two tokens: the insert
// is conditional and the loser reads the winner's value back.
func (s *Store) SettingOnce(ctx context.Context, key string, gen func() string) (string, error) {
	if v, err := s.Setting(ctx, key); err != nil || v != "" {
		return v, err
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO settings (key,value) VALUES ($1,$2) ON CONFLICT (key) DO NOTHING`, key, gen()); err != nil {
		return "", err
	}
	return s.Setting(ctx, key)
}

// ---- workers ----

const workerCols = `id, name, labels, os, arch, version, last_seen, created_at`

func scanWorker(row pgx.Row) (*Worker, error) {
	w := &Worker{}
	err := row.Scan(&w.ID, &w.Name, &w.Labels, &w.OS, &w.Arch, &w.Version, &w.LastSeen, &w.Created)
	return w, err
}

// RegisterWorker records a machine that has joined, or re-registers one under
// the same NAME — re-running the join command on a machine that already joined
// updates it in place rather than leaving a ghost in the list.
func (s *Store) RegisterWorker(ctx context.Context, w *Worker, tokenHash string) error {
	if w.ID == uuid.Nil {
		w.ID = uuid.New()
	}
	if w.Labels == nil {
		w.Labels = []string{}
	}
	return s.pool.QueryRow(ctx, `
		INSERT INTO workers (id, name, labels, os, arch, version, token_hash, last_seen)
		VALUES ($1,$2,$3,$4,$5,$6,$7, now())
		RETURNING `+workerCols,
		w.ID, w.Name, w.Labels, w.OS, w.Arch, w.Version, tokenHash,
	).Scan(&w.ID, &w.Name, &w.Labels, &w.OS, &w.Arch, &w.Version, &w.LastSeen, &w.Created)
}

// WorkerByToken authenticates a polling worker by its own token.
func (s *Store) WorkerByToken(ctx context.Context, tokenHash string) (*Worker, error) {
	return scanWorker(s.pool.QueryRow(ctx, `SELECT `+workerCols+` FROM workers WHERE token_hash=$1`, tokenHash))
}

func (s *Store) TouchWorker(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE workers SET last_seen=now() WHERE id=$1`, id)
	return err
}

func (s *Store) ListWorkers(ctx context.Context) ([]*Worker, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+workerCols+` FROM workers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Worker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) DeleteWorker(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM workers WHERE id=$1`, id)
	return err
}

// OnlineLabels is every label currently served by a worker that is polling.
// The planner uses it to say, before a run costs anything, that a `runs-on:`
// nobody serves will wait forever.
func (s *Store) OnlineLabels(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT unnest(labels), count(*) FROM workers WHERE last_seen > now() - interval '90 seconds' GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var l string
		var n int
		if err := rows.Scan(&l, &n); err != nil {
			return nil, err
		}
		out[l] = n
	}
	return out, rows.Err()
}

// ---- the job queue ----

const jobCols = `id, run_id, step_id, label, worker_id, status, payload, result, created_at, leased_at, finished_at`

func scanJob(row pgx.Row) (*Job, error) {
	j := &Job{}
	err := row.Scan(&j.ID, &j.RunID, &j.StepID, &j.Label, &j.WorkerID, &j.Status, &j.Payload, &j.Result,
		&j.CreatedAt, &j.LeasedAt, &j.FinishedAt)
	return j, err
}

func (s *Store) EnqueueJob(ctx context.Context, runID uuid.UUID, stepID, label string, payload json.RawMessage) (*Job, error) {
	return scanJob(s.pool.QueryRow(ctx, `
		INSERT INTO worker_jobs (id, run_id, step_id, label, payload)
		VALUES ($1,$2,$3,$4,$5) RETURNING `+jobCols,
		uuid.New(), runID, stepID, label, payload))
}

// ClaimJob hands the oldest queued job for one of this worker's labels to it.
// FOR UPDATE SKIP LOCKED is what makes two workers holding the same label safe
// without a lease table: the second one skips the row the first is taking.
func (s *Store) ClaimJob(ctx context.Context, w *Worker) (*Job, error) {
	j, err := scanJob(s.pool.QueryRow(ctx, `
		UPDATE worker_jobs SET status='leased', worker_id=$1, leased_at=now()
		WHERE id = (
			SELECT id FROM worker_jobs
			WHERE status='queued' AND label = ANY($2)
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING `+jobCols, w.ID, w.Labels))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return j, err
}

// FinishJob records a result. It is the worker's own report, and a job already
// finished is not overwritten — a retrying worker cannot rewrite history.
func (s *Store) FinishJob(ctx context.Context, id uuid.UUID, workerID uuid.UUID, result json.RawMessage) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE worker_jobs SET status='done', result=$3, finished_at=now()
		 WHERE id=$1 AND worker_id=$2 AND status='leased'`, id, workerID, result)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("no such leased job for this worker")
	}
	return nil
}

func (s *Store) GetJob(ctx context.Context, id uuid.UUID) (*Job, error) {
	return scanJob(s.pool.QueryRow(ctx, `SELECT `+jobCols+` FROM worker_jobs WHERE id=$1`, id))
}

// CancelJob drops a job that is no longer wanted — the run was cancelled, or
// the step timed out waiting for a worker that never came.
func (s *Store) CancelJob(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE worker_jobs SET status='done', finished_at=now(),
		 result=COALESCE(result,'{"ok":false,"error":"cancelled"}'::jsonb)
		 WHERE id=$1 AND status <> 'done'`, id)
	return err
}

// RequeueLostJobs puts back anything leased by a worker that has stopped
// polling. A machine that is unplugged mid-step must not strand the run.
func (s *Store) RequeueLostJobs(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE worker_jobs SET status='queued', worker_id=NULL, leased_at=NULL
		WHERE status='leased' AND worker_id IN (
			SELECT id FROM workers WHERE last_seen < now() - interval '120 seconds'
		)`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

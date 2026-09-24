package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ---- settings ----

// Setting reads a stored value, "" when absent.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.qrow(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting stores a value, replacing any previous one.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.exec(ctx,
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
	if _, err := s.exec(ctx,
		`INSERT INTO settings (key,value) VALUES ($1,$2) ON CONFLICT (key) DO NOTHING`, key, gen()); err != nil {
		return "", err
	}
	return s.Setting(ctx, key)
}

// ---- workers ----

const workerCols = `id, name, labels, os, arch, version, last_seen, created_at`

func (s *Store) scanWorker(row rowScanner) (*Worker, error) {
	w := &Worker{}
	err := row.Scan(&w.ID, &w.Name, labelsScan{s.d, &w.Labels}, &w.OS, &w.Arch, &w.Version, &w.LastSeen, &w.Created)
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
	// ON CONFLICT (name): a machine is its name. The previous token stops
	// working, which is correct — whoever just ran the join command holds the
	// new one, and a machine cannot be in the pool twice.
	return s.qrow(ctx, `
		INSERT INTO workers (id, name, labels, os, arch, version, token_hash, last_seen)
		VALUES ($1,$2,$3,$4,$5,$6,$7, now())
		ON CONFLICT (name) DO UPDATE SET
			labels=EXCLUDED.labels, os=EXCLUDED.os, arch=EXCLUDED.arch,
			version=EXCLUDED.version, token_hash=EXCLUDED.token_hash, last_seen=now()
		RETURNING `+workerCols,
		w.ID, w.Name, s.d.labelsArg(w.Labels), w.OS, w.Arch, w.Version, tokenHash,
	).Scan(&w.ID, &w.Name, labelsScan{s.d, &w.Labels}, &w.OS, &w.Arch, &w.Version, &w.LastSeen, &w.Created)
}

// WorkerByToken authenticates a polling worker by its own token.
func (s *Store) WorkerByToken(ctx context.Context, tokenHash string) (*Worker, error) {
	return s.scanWorker(s.qrow(ctx, `SELECT `+workerCols+` FROM workers WHERE token_hash=$1`, tokenHash))
}

func (s *Store) TouchWorker(ctx context.Context, id uuid.UUID) error {
	_, err := s.exec(ctx, `UPDATE workers SET last_seen=now() WHERE id=$1`, id)
	return err
}

func (s *Store) ListWorkers(ctx context.Context) ([]*Worker, error) {
	rows, err := s.query(ctx, `SELECT `+workerCols+` FROM workers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Worker
	for rows.Next() {
		w, err := s.scanWorker(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) DeleteWorker(ctx context.Context, id uuid.UUID) error {
	_, err := s.exec(ctx, `DELETE FROM workers WHERE id=$1`, id)
	return err
}

// OnlineLabels is every label currently served by a worker that is polling.
// The planner uses it to say, before a run costs anything, that a `runs-on:`
// nobody serves will wait forever.
func (s *Store) OnlineLabels(ctx context.Context) (map[string]int, error) {
	q := `SELECT unnest(labels), count(*) FROM workers WHERE last_seen > $1 GROUP BY 1`
	if s.d.isSQLite() {
		// SQLite's answer to unnest is the json1 table-valued function.
		q = `SELECT l.value, count(*) FROM workers w, json_each(w.labels) l
		     WHERE w.last_seen > $1 GROUP BY 1`
	}
	rows, err := s.query(ctx, q, time.Now().UTC().Add(-90*time.Second))
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

func (s *Store) scanJob(row rowScanner) (*Job, error) {
	j := &Job{}
	var worker uuid.NullUUID
	err := row.Scan(&j.ID, &j.RunID, &j.StepID, &j.Label, &worker, &j.Status,
		rawJSON{&j.Payload}, rawJSON{&j.Result}, &j.CreatedAt, &j.LeasedAt, &j.FinishedAt)
	if worker.Valid {
		id := worker.UUID
		j.WorkerID = &id
	}
	return j, err
}

func (s *Store) EnqueueJob(ctx context.Context, runID uuid.UUID, stepID, label string, payload json.RawMessage) (*Job, error) {
	return s.scanJob(s.qrow(ctx, `
		INSERT INTO worker_jobs (id, run_id, step_id, label, payload)
		VALUES ($1,$2,$3,$4,$5) RETURNING `+jobCols,
		uuid.New(), runID, stepID, label, jsonArg(payload)))
}

// ClaimJob hands the oldest queued job for one of this worker's labels to it.
// FOR UPDATE SKIP LOCKED is what makes two workers holding the same label safe
// without a lease table: the second one skips the row the first is taking.
func (s *Store) ClaimJob(ctx context.Context, w *Worker) (*Job, error) {
	q := `
		UPDATE worker_jobs SET status='leased', worker_id=$1, leased_at=now()
		WHERE id = (
			SELECT id FROM worker_jobs
			WHERE status='queued' AND label = ANY($2)
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING ` + jobCols
	if s.d.isSQLite() {
		// No SKIP LOCKED, and none needed: SQLite takes one write lock for the
		// whole database, so two workers claiming at once are serialised by the
		// engine rather than by the row.
		q = `
		UPDATE worker_jobs SET status='leased', worker_id=$1, leased_at=now()
		WHERE id = (
			SELECT id FROM worker_jobs
			WHERE status='queued' AND label IN (SELECT value FROM json_each($2))
			ORDER BY created_at
			LIMIT 1
		)
		RETURNING ` + jobCols
	}
	j, err := s.scanJob(s.qrow(ctx, q, w.ID, s.d.labelsArg(w.Labels)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return j, err
}

// FinishJob records a result. It is the worker's own report, and a job already
// finished is not overwritten — a retrying worker cannot rewrite history.
func (s *Store) FinishJob(ctx context.Context, id uuid.UUID, workerID uuid.UUID, result json.RawMessage) error {
	tag, err := s.exec(ctx,
		`UPDATE worker_jobs SET status='done', result=$3, finished_at=now()
		 WHERE id=$1 AND worker_id=$2 AND status='leased'`, id, workerID, jsonArg(result))
	if err != nil {
		return err
	}
	if n, _ := tag.RowsAffected(); n == 0 {
		return errors.New("no such leased job for this worker")
	}
	return nil
}

func (s *Store) GetJob(ctx context.Context, id uuid.UUID) (*Job, error) {
	return s.scanJob(s.qrow(ctx, `SELECT `+jobCols+` FROM worker_jobs WHERE id=$1`, id))
}

// CancelJob drops a job that is no longer wanted — the run was cancelled, or
// the step timed out waiting for a worker that never came.
func (s *Store) CancelJob(ctx context.Context, id uuid.UUID) error {
	_, err := s.exec(ctx,
		`UPDATE worker_jobs SET status='done', finished_at=now(),
		 result=COALESCE(result,'{"ok":false,"error":"cancelled"}'::jsonb)
		 WHERE id=$1 AND status <> 'done'`, id)
	return err
}

// RequeueLostJobs puts back anything leased by a worker that has stopped
// polling. A machine that is unplugged mid-step must not strand the run.
func (s *Store) RequeueLostJobs(ctx context.Context) (int64, error) {
	tag, err := s.exec(ctx, `
		UPDATE worker_jobs SET status='queued', worker_id=NULL, leased_at=NULL
		WHERE status='leased' AND worker_id IN (
			SELECT id FROM workers WHERE last_seen < $1
		)`, time.Now().UTC().Add(-120*time.Second))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected()
}

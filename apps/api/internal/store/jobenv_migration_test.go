package store

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/store/pgtest"
	"github.com/muthuishere/wfnexus/apps/api/migrations"
)

func mustDSN(t *testing.T) string {
	t.Helper()
	dsn, err := pgtest.DSN()
	if err != nil {
		t.Skip("no postgres")
	}
	return dsn
}

const oldJobPayload = `{"runId":"r","stepId":"s","command":"echo","env":{"API_TOKEN":"canary-old-format-value","PLAIN":"x"},` +
	`"agent":{"step":{"id":"s","env":{"GH_TOKEN":"canary-old-agent-value"}}}}`

// checkBlanked: the row's values are gone, its names kept. A job that was
// still queued is failed with a reason to re-run it (an old row cannot say
// which values were the workflow's own); a leased one keeps running.
func checkBlanked(t *testing.T, st *Store, id, wantStatus string) {
	t.Helper()
	var raw, status, result string
	if err := st.db.QueryRow(`SELECT CAST(payload AS TEXT), status, COALESCE(CAST(result AS TEXT), '') FROM worker_jobs WHERE CAST(id AS TEXT) = $1`, id).Scan(&raw, &status, &result); err != nil {
		t.Fatal(err)
	}
	if status != wantStatus {
		t.Fatalf("status = %s, want %s", status, wantStatus)
	}
	if wantStatus == "done" && !strings.Contains(result, "re-run the step") {
		t.Fatalf("a queued pre-upgrade job was not failed with a reason: %q", result)
	}
	if wantStatus == "leased" && result != "" {
		t.Fatalf("a leased job was given a result: %q", result)
	}
	if strings.Contains(raw, "canary-old") {
		t.Fatalf("a value survived: %s", raw)
	}
	var p struct {
		Env   map[string]string `json:"env"`
		Agent struct {
			Step struct {
				Env map[string]string `json:"env"`
			} `json:"step"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if v, ok := p.Env["API_TOKEN"]; !ok || v != "" || len(p.Env) != 2 {
		t.Fatalf("env = %v", p.Env)
	}
	if v, ok := p.Agent.Step.Env["GH_TOKEN"]; !ok || v != "" {
		t.Fatalf("agent env = %v", p.Agent.Step.Env)
	}
}

// SQLite: a job row written in the old format (values in env) is blanked by
// the migration, every name kept, and the migration logs a count, no value.
func TestMigrationBlanksStoredJobEnvSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	src, err := iofs.New(migrations.FS, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, sqliteMigrateURL(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Migrate(22); err != nil {
		t.Fatal(err)
	}
	m.Close()
	st, err := Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	run, err := st.CreateRun(context.Background(), "local", "wf", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.EnqueueJob(context.Background(), run.ID, "s", "box", json.RawMessage(oldJobPayload))
	if err != nil {
		t.Fatal(err)
	}
	leased := enqueueLeased(t, st, run.ID)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	if err := Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	checkBlanked(t, st, job.ID.String(), "done")
	checkBlanked(t, st, leased, "leased")
	if !strings.Contains(logs.String(), "blanked stored env values in 2 job rows") || strings.Contains(logs.String(), "canary") {
		t.Fatalf("log = %q", logs.String())
	}
}

// Postgres: the migration's SQL blanks an old-format row the same way.
func TestMigrationBlanksStoredJobEnvPostgres(t *testing.T) {
	st := openPostgres(t)
	if st == nil {
		t.Skip("no postgres")
	}
	run, err := st.CreateRun(context.Background(), "local", "wf", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.EnqueueJob(context.Background(), run.ID, "s", "box", json.RawMessage(oldJobPayload))
	if err != nil {
		t.Fatal(err)
	}
	leased := enqueueLeased(t, st, run.ID)
	if n := countStoredJobEnv(driverPostgres, mustDSN(t)); n < 1 {
		t.Fatalf("count before = %d", n)
	}
	sqlText, err := migrations.FS.ReadFile("000029_blank_job_env.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(string(sqlText)); err != nil {
		t.Fatal(err)
	}
	checkBlanked(t, st, job.ID.String(), "done")
	checkBlanked(t, st, leased, "leased")
	if n := countStoredJobEnv(driverPostgres, mustDSN(t)); n != 0 {
		t.Fatalf("count after = %d", n)
	}
}

// enqueueLeased writes an old-format job that a worker holds right now.
func enqueueLeased(t *testing.T, st *Store, runID uuid.UUID) string {
	t.Helper()
	job, err := st.EnqueueJob(context.Background(), runID, "s2", "box", json.RawMessage(oldJobPayload))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE worker_jobs SET status='leased' WHERE CAST(id AS TEXT) = $1`, job.ID.String()); err != nil {
		t.Fatal(err)
	}
	return job.ID.String()
}

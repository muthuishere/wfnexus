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

func checkBlanked(t *testing.T, st *Store, id string) {
	t.Helper()
	var raw string
	if err := st.db.QueryRow(`SELECT CAST(payload AS TEXT) FROM worker_jobs WHERE CAST(id AS TEXT) = $1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
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
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	if err := Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	checkBlanked(t, st, job.ID.String())
	if !strings.Contains(logs.String(), "blanked stored env values in 1 job rows") || strings.Contains(logs.String(), "canary") {
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
	checkBlanked(t, st, job.ID.String())
	if n := countStoredJobEnv(driverPostgres, mustDSN(t)); n != 0 {
		t.Fatalf("count after = %d", n)
	}
}

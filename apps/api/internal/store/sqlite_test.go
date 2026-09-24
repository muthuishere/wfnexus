package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The SQLite store is what `mode: local` runs on, and it has no infrastructure
// to stand up — so this walks the whole surface the engine uses (runs, steps,
// events, artifacts, workers, jobs, settings, env) against a real file, and
// runs in CI with nothing installed.

func openSQLite(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wfnexus.db")
	if err := Migrate(driverSQLite, path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Twice: boot happens more than once on the same file.
	if err := Migrate(driverSQLite, path); err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	st, err := Open(context.Background(), driverSQLite, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

func TestSQLiteRunsStepsEventsArtifacts(t *testing.T) {
	st := openSQLite(t)
	ctx := context.Background()

	run, err := st.CreateRun(ctx, "demo", "hello", json.RawMessage(`{"issue":42}`))
	if err != nil {
		t.Fatal(err)
	}
	if run.CreatedAt.IsZero() {
		t.Fatal("created_at did not come back as a time")
	}

	got, err := st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project != "demo" || got.Workflow != "hello" || got.Status != "queued" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if string(got.Input) != `{"issue":42}` {
		t.Fatalf("input json not preserved: %s", got.Input)
	}

	if err := st.UpdateRun(ctx, run.ID, "running", "build", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateRunInput(ctx, run.ID, json.RawMessage(`{"issue":43}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetBaseRef(ctx, run.ID, "abc123"); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetRun(ctx, run.ID)
	if got.Status != "running" || got.CurrentStep != "build" || got.BaseRef != "abc123" ||
		string(got.Input) != `{"issue":43}` {
		t.Fatalf("update did not stick: %+v", got)
	}

	runs, err := st.FindRuns(ctx, RunFilter{Project: "demo", Workflow: "hello"})
	if err != nil || len(runs) != 1 {
		t.Fatalf("FindRuns: %v %d", err, len(runs))
	}
	if all, err := st.ListRuns(ctx, 10); err != nil || len(all) != 1 {
		t.Fatalf("ListRuns: %v %d", err, len(all))
	}

	act, err := st.ProjectRunActivity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a := act["demo"]; a.Runs != 1 || a.LastStatus != "running" {
		t.Fatalf("activity: %+v", act)
	}

	// ---- steps ----
	if err := st.EnsureStep(ctx, run.ID, "build", 0); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureStep(ctx, run.ID, "build", 0); err != nil {
		t.Fatal("EnsureStep must be idempotent:", err)
	}
	if err := st.EnsureStep(ctx, run.ID, "ship", 1); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	status, attempts := "done", 2
	if err := st.PatchStep(ctx, run.ID, "build", StepPatch{
		Status:    &status,
		Attempts:  &attempts,
		Output:    json.RawMessage(`{"ok":true}`),
		Usage:     json.RawMessage(`{"tokens":10}`),
		Pending:   json.RawMessage(`{"ask":"approve?"}`),
		Decision:  json.RawMessage(`{"verdict":"yes"}`),
		StartedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	step, err := st.GetStep(ctx, run.ID, "build")
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != "done" || step.Attempts != 2 || string(step.Output) != `{"ok":true}` ||
		string(step.Pending) != `{"ask":"approve?"}` || string(step.Decision) != `{"verdict":"yes"}` {
		t.Fatalf("step patch: %+v", step)
	}
	if step.StartedAt == nil || step.FinishedAt != nil {
		t.Fatalf("nullable timestamps wrong: %+v %+v", step.StartedAt, step.FinishedAt)
	}

	// A patch that names nothing must leave everything alone — the COALESCE
	// path, which is where a placeholder rewrite would go wrong.
	if err := st.PatchStep(ctx, run.ID, "build", StepPatch{ClearPending: true}); err != nil {
		t.Fatal(err)
	}
	step, _ = st.GetStep(ctx, run.ID, "build")
	if step.Pending != nil {
		t.Fatalf("ClearPending did not clear: %s", step.Pending)
	}
	if step.Status != "done" || step.Attempts != 2 || string(step.Output) != `{"ok":true}` {
		t.Fatalf("an empty patch overwrote columns: %+v", step)
	}

	steps, err := st.ListSteps(ctx, run.ID)
	if err != nil || len(steps) != 2 || steps[0].StepID != "build" {
		t.Fatalf("ListSteps: %v %+v", err, steps)
	}

	if err := st.ResetStepsFrom(ctx, run.ID, 0); err != nil {
		t.Fatal(err)
	}
	step, _ = st.GetStep(ctx, run.ID, "build")
	if step.Status != "pending" || step.Output != nil {
		t.Fatalf("reset: %+v", step)
	}

	// ---- events ----
	ev, err := st.AppendEvent(ctx, run.ID, "build", "log", map[string]any{"msg": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if ev.ID == 0 {
		t.Fatal("event id did not autoincrement")
	}
	if _, err := st.AppendEvent(ctx, run.ID, "build", "log", map[string]any{"msg": "again"}); err != nil {
		t.Fatal(err)
	}
	evs, err := st.ListEvents(ctx, run.ID, 0, 10)
	if err != nil || len(evs) != 2 {
		t.Fatalf("ListEvents: %v %d", err, len(evs))
	}
	if string(evs[0].Payload) != `{"msg":"hi"}` {
		t.Fatalf("payload: %s", evs[0].Payload)
	}
	if after, err := st.ListEvents(ctx, run.ID, ev.ID, 10); err != nil || len(after) != 1 {
		t.Fatalf("ListEvents after: %v %d", err, len(after))
	}

	// ---- artifacts ----
	a := &Artifact{RunID: run.ID, StepID: "build", Name: "diff.patch",
		ObjectKey: "runs/x/diff.patch", ContentType: "text/plain", SizeBytes: 12}
	if err := st.CreateArtifact(ctx, a); err != nil {
		t.Fatal(err)
	}
	arts, err := st.ListArtifacts(ctx, run.ID)
	if err != nil || len(arts) != 1 || arts[0].Name != "diff.patch" || arts[0].SizeBytes != 12 {
		t.Fatalf("artifacts: %v %+v", err, arts)
	}
}

func TestSQLiteWorkersAndJobs(t *testing.T) {
	st := openSQLite(t)
	ctx := context.Background()

	// settings, including the generate-once path the runner token uses
	if err := st.SetSetting(ctx, "k", "v"); err != nil {
		t.Fatal(err)
	}
	if v, err := st.Setting(ctx, "k"); err != nil || v != "v" {
		t.Fatalf("setting: %v %q", err, v)
	}
	if v, err := st.Setting(ctx, "absent"); err != nil || v != "" {
		t.Fatalf("a missing setting must be empty, not an error: %v %q", err, v)
	}
	tok, err := st.SettingOnce(ctx, "token", func() string { return "first" })
	if err != nil || tok != "first" {
		t.Fatalf("SettingOnce: %v %q", err, tok)
	}
	if again, _ := st.SettingOnce(ctx, "token", func() string { return "second" }); again != "first" {
		t.Fatalf("SettingOnce regenerated: %q", again)
	}

	w := &Worker{Name: "laptop", Labels: []string{"local", "macos"}, OS: "darwin", Arch: "arm64", Version: "1"}
	if err := st.RegisterWorker(ctx, w, "hash1"); err != nil {
		t.Fatal(err)
	}
	if len(w.Labels) != 2 || w.Labels[0] != "local" {
		t.Fatalf("labels did not round trip: %v", w.Labels)
	}
	// Re-joining the same machine updates it in place.
	w2 := &Worker{Name: "laptop", Labels: []string{"local"}, OS: "darwin", Arch: "arm64", Version: "2"}
	if err := st.RegisterWorker(ctx, w2, "hash2"); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListWorkers(ctx)
	if err != nil || len(list) != 1 || list[0].Version != "2" {
		t.Fatalf("re-register left a ghost: %v %+v", err, list)
	}
	if !list[0].Online() || list[0].Status() != "online" {
		t.Fatalf("a worker that just registered should be online: %v", list[0].LastSeen)
	}

	found, err := st.WorkerByToken(ctx, "hash2")
	if err != nil || found.Name != "laptop" {
		t.Fatalf("WorkerByToken: %v %+v", err, found)
	}
	if err := st.TouchWorker(ctx, found.ID); err != nil {
		t.Fatal(err)
	}
	labels, err := st.OnlineLabels(ctx)
	if err != nil || labels["local"] != 1 {
		t.Fatalf("OnlineLabels: %v %v", err, labels)
	}

	run, err := st.CreateRun(ctx, "demo", "hello", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	job, err := st.EnqueueJob(ctx, run.ID, "build", "local", json.RawMessage(`{"cmd":"go test"}`))
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "queued" || string(job.Payload) != `{"cmd":"go test"}` || job.WorkerID != nil {
		t.Fatalf("enqueue: %+v", job)
	}

	claimed, err := st.ClaimJob(ctx, found)
	if err != nil || claimed == nil {
		t.Fatalf("ClaimJob: %v %+v", err, claimed)
	}
	if claimed.Status != "leased" || claimed.WorkerID == nil || *claimed.WorkerID != found.ID {
		t.Fatalf("lease: %+v", claimed)
	}
	if empty, err := st.ClaimJob(ctx, found); err != nil || empty != nil {
		t.Fatalf("an empty queue must be (nil, nil): %v %+v", err, empty)
	}
	// A label nobody holds hands out nothing.
	stranger := &Worker{ID: uuid.New(), Labels: []string{"windows"}}
	if j, err := st.ClaimJob(ctx, stranger); err != nil || j != nil {
		t.Fatalf("claimed someone else's label: %v %+v", err, j)
	}

	if err := st.FinishJob(ctx, claimed.ID, found.ID, json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishJob(ctx, claimed.ID, found.ID, json.RawMessage(`{"ok":false}`)); err == nil {
		t.Fatal("a finished job must not be rewritable")
	}
	done, err := st.GetJob(ctx, claimed.ID)
	if err != nil || done.Status != "done" || string(done.Result) != `{"ok":true}` || done.FinishedAt == nil {
		t.Fatalf("GetJob: %v %+v", err, done)
	}

	other, err := st.EnqueueJob(ctx, run.ID, "ship", "local", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CancelJob(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.GetJob(ctx, other.ID); c.Status != "done" {
		t.Fatalf("cancel: %+v", c)
	}
	if n, err := st.RequeueLostJobs(ctx); err != nil || n != 0 {
		t.Fatalf("nothing is lost yet: %v %d", err, n)
	}

	if err := st.DeleteWorker(ctx, found.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListWorkers(ctx); len(list) != 0 {
		t.Fatalf("DeleteWorker: %+v", list)
	}
}

// plainSealer stands in for the real encryption: the store only needs something
// that seals and opens, and the key belongs to whoever passes it in.
type plainSealer struct{}

func (plainSealer) Seal(p string) ([]byte, error) { return []byte("enc:" + p), nil }
func (plainSealer) Open(b []byte) (string, error) { return string(b[len("enc:"):]), nil }

func TestSQLiteEnvVars(t *testing.T) {
	st := openSQLite(t)
	ctx := context.Background()
	box := plainSealer{}

	if err := st.PutEnvVar(ctx, box, ScopeSystem, "", "GH_TOKEN", "ghp_x", true); err != nil {
		t.Fatal(err)
	}
	if err := st.PutEnvVar(ctx, box, ScopeSystem, "", "SERVICE_URL", "https://api", false); err != nil {
		t.Fatal(err)
	}
	// Writing the same name again replaces it rather than failing the key.
	if err := st.PutEnvVar(ctx, box, ScopeSystem, "", "SERVICE_URL", "https://api2", false); err != nil {
		t.Fatal(err)
	}
	if err := st.PutEnvVar(ctx, box, ScopeProject, "demo", "TIER", "project", false); err != nil {
		t.Fatal(err)
	}

	vars, err := st.ListEnvVars(ctx, box, ScopeSystem, "")
	if err != nil || len(vars) != 2 {
		t.Fatalf("ListEnvVars: %v %+v", err, vars)
	}
	for _, v := range vars {
		switch v.Key {
		case "GH_TOKEN":
			if !v.Secret || v.Value != "" {
				t.Fatalf("a secret's value must not be listed: %+v", v)
			}
		case "SERVICE_URL":
			if v.Value != "https://api2" {
				t.Fatalf("non-secret: %+v", v)
			}
		}
	}

	// The stored bytes are the sealed ones, not the plaintext.
	var enc []byte
	if err := st.DB().QueryRowContext(ctx,
		`SELECT value_enc FROM env_vars WHERE key='GH_TOKEN'`).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if string(enc) != "enc:ghp_x" {
		t.Fatalf("blob round trip: %q", enc)
	}

	env, err := st.EnvFor(ctx, box, ScopeSystem, "")
	if err != nil || env["GH_TOKEN"] != "ghp_x" {
		t.Fatalf("EnvFor: %v %v", err, env)
	}
	names, err := st.EnvKeyNames(ctx, ScopeProject, "demo")
	if err != nil || len(names) != 1 || names[0] != "TIER" {
		t.Fatalf("EnvKeyNames: %v %v", err, names)
	}

	if err := st.DeleteEnvVar(ctx, ScopeSystem, "", "GH_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if vars, _ := st.ListEnvVars(ctx, box, ScopeSystem, ""); len(vars) != 1 {
		t.Fatalf("delete: %+v", vars)
	}
}

// The rewrite is the whole dialect, so it is worth asserting directly — in
// particular that $11 stays parameter 11 and does not become the first ?.
func TestSQLitePlaceholderRewriteKeepsIndexes(t *testing.T) {
	d := dialect{name: driverSQLite}
	got := d.q(`UPDATE t SET a=COALESCE($3,a), b=$11::text, c=now() WHERE id=$1 AND k=$2`)
	want := `UPDATE t SET a=COALESCE(?3,a), b=?11, c=CURRENT_TIMESTAMP WHERE id=?1 AND k=?2`
	if got != want {
		t.Fatalf("rewrite\n got %s\nwant %s", got, want)
	}
	if p := (dialect{name: driverPostgres}); p.q(want) != want {
		t.Fatal("postgres must pass through untouched")
	}
}

// A run's start time is its own fact, stamped once when it leaves the queue —
// not the first step's started_at, which the UI had been reading as a proxy.
func TestRunStartedAtIsStampedOnceWhenItLeavesTheQueue(t *testing.T) {
	st := openSQLite(t)
	ctx := context.Background()

	run, err := st.CreateRun(ctx, "demo", "hello", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if run.StartedAt != nil {
		t.Fatalf("a queued run has not started: %v", run.StartedAt)
	}
	got, err := st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.StartedAt != nil {
		t.Fatalf("queued run came back with a start time: %v", got.StartedAt)
	}

	if err := st.UpdateRun(ctx, run.ID, "running", "build", ""); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.StartedAt == nil {
		t.Fatal("running run has no start time")
	}
	first := *got.StartedAt

	// A later status change must NOT restamp it.
	time.Sleep(10 * time.Millisecond)
	if err := st.UpdateRun(ctx, run.ID, "done", "", ""); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(first) {
		t.Fatalf("start time moved: %v -> %v", first, got.StartedAt)
	}
}

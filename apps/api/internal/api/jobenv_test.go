package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

const jobCanary = "canary-job-env-4c8e1f93a7d2"

// dumpDB is every row of every table as text: where a stored value would be.
func dumpDB(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	var b strings.Builder
	for _, tb := range tables {
		r, err := db.Query(`SELECT * FROM "` + tb + `"`)
		if err != nil {
			continue
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = r.Scan(ptrs...)
			fmt.Fprintf(&b, "%s: %v\n", tb, vals)
			for _, v := range vals {
				if bs, ok := v.([]byte); ok {
					b.Write(bs)
				}
			}
		}
		r.Close()
	}
	return b.String()
}

// The env-store value a remote step needs is delivered when the worker claims
// the job and is stored nowhere: not in any table, not in a run or job API
// response, not in the log — after the claim, while the step runs, and after
// the end. A job reclaimed after a worker restart gets the value again.
func TestJobEnvIsDeliveredAtClaimAndNeverStored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	label := "envbox-" + uuid.NewString()[:8]
	got := filepath.Join(t.TempDir(), "got")
	dbPath := filepath.Join(t.TempDir(), "w.db")
	if err := store.Migrate("sqlite", dbPath); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	repo := t.TempDir()
	wf := filepath.Join(repo, ".wfx", "workflows")
	_ = os.MkdirAll(wf, 0o755)
	run := `printf '%s' "$CANARY_KEY" > ` + got
	b, _ := json.Marshal(run)
	_ = os.WriteFile(filepath.Join(wf, "remote.yaml"), []byte("name: remote\nsteps:\n  - id: use\n    shell: sh\n    runs-on: "+label+"\n    run: "+string(b)+"\n"), 0o644)
	cfg := config.Config{Addr: "127.0.0.1:0", WorkDir: t.TempDir(), WorkflowsDir: t.TempDir(),
		SecretKeyEnv: "WFX_JOBENV_TEST_NO_KEY", SecretKeyPath: filepath.Join(t.TempDir(), "k")}
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	if _, err := eng.ImportRepo(context.Background(), "demo", repo, ""); err != nil {
		t.Fatal(err)
	}
	f := exposeFixture{h: New(eng, st, nil, cfg.Addr, "", nil), st: st, eng: eng}
	ctx := context.Background()
	if err := eng.SetEnvVar(ctx, store.ScopeSystem, "", "CANARY_KEY", jobCanary, true); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	tok, _ := eng.RegistrationToken(ctx)
	jb, _ := json.Marshal(map[string]any{"token": tok, "name": "env-worker", "labels": []string{label}})
	code, joined, _ := f.call(t, "POST", "/api/workers/join", jb, nil)
	if code != 200 && code != 201 {
		t.Fatalf("join = %d %v", code, joined)
	}
	wtok := joined["token"].(string)
	workerID := joined["worker"].(map[string]any)["id"].(string)
	auth := map[string]string{"Authorization": "Bearer " + wtok}

	code, out, _ := f.call(t, "POST", "/api/workflows/demo%2Fremote/runs", []byte(`{}`), nil)
	if code != 201 {
		t.Fatalf("start = %d %v", code, out)
	}
	runID := out["run"].(map[string]any)["id"].(string)

	claim := func() (string, engine.JobPayload) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			req := httptest.NewRequest("POST", "/api/workers/claim?wait=1s", nil)
			req.Header.Set("Authorization", "Bearer "+wtok)
			rec := httptest.NewRecorder()
			f.h.ServeHTTP(rec, req)
			if rec.Code == 200 {
				var job struct {
					ID      string            `json:"id"`
					Payload engine.JobPayload `json:"payload"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &job)
				return job.ID, job.Payload
			}
		}
		t.Fatal("nothing to claim")
		return "", engine.JobPayload{}
	}
	nowhere := func(when string) {
		t.Helper()
		places := map[string]string{"database": dumpDB(t, dbPath), "server log": logs.String()}
		req := httptest.NewRequest("GET", "/api/runs/"+runID, nil)
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		places["GET /api/runs/{id}"] = rec.Body.String()
		req = httptest.NewRequest("GET", "/api/workers", nil)
		rec = httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		places["GET /api/workers"] = rec.Body.String()
		forms := map[string]string{
			"raw": jobCanary, "base64": base64.StdEncoding.EncodeToString([]byte(jobCanary)),
			"hex": hex.EncodeToString([]byte(jobCanary)),
		}
		for where, text := range places {
			for form, v := range forms {
				if strings.Contains(text, v) {
					t.Errorf("%s: the %s form of the value is in the %s", when, form, where)
				}
			}
		}
	}

	// First claim: the value is delivered. The worker then dies.
	_, p1 := claim()
	if p1.Env["CANARY_KEY"] != jobCanary {
		t.Fatalf("the first claim did not deliver the value: %v", len(p1.Env))
	}
	nowhere("after the first claim")

	// The worker restarts: its lease is given up and the job is claimed again.
	db, _ := sql.Open("sqlite", dbPath)
	_, err = db.Exec(`UPDATE workers SET last_seen = '2000-01-01 00:00:00' WHERE id = $1`, workerID)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if n, err := st.RequeueLostJobs(ctx); err != nil || n != 1 {
		t.Fatalf("requeue = %d %v", n, err)
	}
	jobID, p2 := claim()
	if p2.Env["CANARY_KEY"] != jobCanary {
		t.Fatalf("the reclaim did not deliver the value again: %v", len(p2.Env))
	}
	nowhere("after the reclaim, while the step runs")

	// The worker runs the step with what it was given.
	c := exec.Command("sh", "-c", p2.Command)
	c.Env = os.Environ()
	for k, v := range p2.Env {
		c.Env = append(c.Env, k+"="+v)
	}
	if err := c.Run(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(got); string(b) != jobCanary {
		t.Fatalf("the step did not receive the value: %q", b)
	}
	// The worker's report quotes the value in every form; it is stored in none.
	b64, hx := base64.StdEncoding.EncodeToString([]byte(jobCanary)), hex.EncodeToString([]byte(jobCanary))
	res, _ := json.Marshal(engine.JobResult{OK: true, Stdout: jobCanary + " " + b64 + " " + hx})
	if code, out, _ := f.call(t, "POST", "/api/workers/jobs/"+jobID+"/result", res, auth); code != 200 {
		t.Fatalf("result = %d %v", code, out)
	}
	for i := 0; i < 200; i++ {
		r, _ := st.GetRun(ctx, uuid.MustParse(runID))
		if r != nil && (r.Status == "done" || r.Status == "failed") {
			if r.Status != "done" {
				t.Fatalf("run = %s %s", r.Status, r.Error)
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	nowhere("after the end")
}

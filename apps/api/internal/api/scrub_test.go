package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http/httptest"
	"net/url"
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

const scrubCanary = "canary/7f3e+9b1c=5d2a&91"

// echoForms prints the secret raw, base64, hex and URL-encoded, to stdout and
// to stderr: what a careless step might do. It also prints the shapes a real
// credential leaves: a JSON document holding a secret with quote, backslash,
// ampersand, angle brackets and a newline (a service-account file), a PEM key
// with LF and with CRLF line endings, and Basic-auth headers whose user names
// put the secret at each of the three base64 byte offsets.
const echoForms = `python3 -c 'import os,sys,json,base64,urllib.parse
v=os.environ["CANARY_KEY"]
forms=[v, base64.b64encode(v.encode()).decode(), v.encode().hex(), urllib.parse.quote(v, safe="")]
for u in ["u", "us", "use"]:
    forms.append("Authorization: Basic " + base64.b64encode((u + ":" + v).encode()).decode())
forms.append(json.dumps({"type": "service_account", "private_key": os.environ["JSON_KEY"]}))
pem=os.environ["PEM_KEY"]
forms.append(pem)
forms.append(pem.replace("\n", "\r\n"))
for f in forms:
    print("out", f)
    print("err", f, file=sys.stderr)'`

// scrubJSONKey has every character JSON escapes; scrubPEM spans lines. Their
// alphanumeric runs (scrubMarkers) cannot be changed by any escaping, so a
// marker found anywhere means some spelling of the value got through.
const (
	scrubJSONKey = "Jz9Qk2\"\\&<x>\nWm4Ry8"
	scrubPEM     = "-----BEGIN TEST KEY-----\nQm9vZ2xlTW9vZ2xlMTIz\nTWFya2VyNDU2Nzg5MDEy\n-----END TEST KEY-----"
)

var scrubMarkers = []string{"Jz9Qk2", "Wm4Ry8", "Qm9vZ2xlTW9vZ2xlMTIz", "TWFya2VyNDU2Nzg5MDEy"}

func scrubForms() map[string]string {
	f := map[string]string{
		"raw":    scrubCanary,
		"base64": base64.StdEncoding.EncodeToString([]byte(scrubCanary)),
		"hex":    hex.EncodeToString([]byte(scrubCanary)),
		"url":    url.QueryEscape(scrubCanary),
	}
	for _, u := range []string{"u", "us", "use"} {
		f["basic auth as "+u] = base64.StdEncoding.EncodeToString([]byte(u + ":" + scrubCanary))
	}
	return f
}

func TestAStepThatPrintsASecretStoresNoFormOfIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	label := "scrubbox-" + uuid.NewString()[:8]
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	path := filepath.Join(t.TempDir(), "w.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	repo := t.TempDir()
	wf := filepath.Join(repo, ".wfx", "workflows")
	_ = os.MkdirAll(wf, 0o755)
	_ = os.WriteFile(filepath.Join(wf, "local.yaml"), []byte("name: local\nsteps:\n  - id: echo\n    shell: sh\n    run: "+quote(echoForms)+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(wf, "remote.yaml"), []byte("name: remote\nsteps:\n  - id: echo\n    shell: sh\n    runs-on: "+label+"\n    run: "+quote(echoForms)+"\n"), 0o644)
	cfg := config.Config{Addr: "127.0.0.1:0", WorkDir: t.TempDir(), WorkflowsDir: t.TempDir(),
		SecretKeyEnv: "WFX_SCRUB_TEST_NO_KEY", SecretKeyPath: filepath.Join(t.TempDir(), "k")}
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	if _, err := eng.ImportRepo(context.Background(), "demo", repo, ""); err != nil {
		t.Fatal(err)
	}
	f := exposeFixture{h: New(eng, st, nil, cfg.Addr, "", nil), st: st, eng: eng}
	ctx := context.Background()
	for k, v := range map[string]string{"CANARY_KEY": scrubCanary, "JSON_KEY": scrubJSONKey, "PEM_KEY": scrubPEM} {
		if err := eng.SetEnvVar(ctx, store.ScopeSystem, "", k, v, true); err != nil {
			t.Fatal(err)
		}
	}

	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	// A worker for the remote label, doing what wfx-runner does over the API:
	// claim, run with the job's env, post its live log, post the result.
	tok, err := eng.RegistrationToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	jb, _ := json.Marshal(map[string]any{"token": tok, "name": "scrub-worker", "labels": []string{label}})
	code, joined, _ := f.call(t, "POST", "/api/workers/join", jb, nil)
	if code != 200 && code != 201 {
		t.Fatalf("join = %d %v", code, joined)
	}
	wtok := joined["token"].(string)
	auth := map[string]string{"Authorization": "Bearer " + wtok}
	jobIDs := make(chan string, 1)
	go func() {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			req := httptest.NewRequest("POST", "/api/workers/claim?wait=1s", nil)
			req.Header.Set("Authorization", "Bearer "+wtok)
			rec := httptest.NewRecorder()
			f.h.ServeHTTP(rec, req)
			if rec.Code != 200 {
				continue
			}
			var job struct {
				ID      string            `json:"id"`
				Payload engine.JobPayload `json:"payload"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &job)
			c := exec.Command("sh", "-c", job.Payload.Command)
			c.Env = os.Environ()
			for k, v := range job.Payload.Env {
				c.Env = append(c.Env, k+"="+v)
			}
			var so, se bytes.Buffer
			c.Stdout, c.Stderr = &so, &se
			_ = c.Run()
			ev, _ := json.Marshal(map[string]any{"events": []map[string]any{{"stepId": "echo", "kind": "log", "payload": map[string]any{"stdout": so.String(), "stderr": se.String()}}}})
			f.call(t, "POST", "/api/workers/jobs/"+job.ID+"/events", ev, auth)
			res, _ := json.Marshal(engine.JobResult{OK: true, Stdout: so.String(), Stderr: se.String()})
			f.call(t, "POST", "/api/workers/jobs/"+job.ID+"/result", res, auth)
			jobIDs <- job.ID
			return
		}
	}()

	for _, name := range []string{"local", "remote"} {
		t.Run(name, func(t *testing.T) {
			code, out, _ := f.call(t, "POST", "/api/workflows/demo%2F"+name+"/runs", []byte(`{}`), nil)
			if code != 201 {
				t.Fatalf("start = %d %v", code, out)
			}
			id := uuid.MustParse(out["run"].(map[string]any)["id"].(string))
			var run *store.Run
			for i := 0; i < 400; i++ {
				run, _ = st.GetRun(ctx, id)
				if run != nil && (run.Status == "done" || run.Status == "failed") {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if run == nil || run.Status != "done" {
				t.Fatalf("run = %+v", run)
			}
			places := map[string]string{}
			b, _ := json.Marshal(run)
			places["run row"] = string(b)
			steps, _ := st.ListSteps(ctx, id)
			b, _ = json.Marshal(steps)
			places["step rows"] = string(b)
			evs, _ := st.ListEvents(ctx, id, 0, 10000)
			b, _ = json.Marshal(evs)
			places["events"] = string(b)
			req := httptest.NewRequest("GET", "/api/runs/"+id.String(), nil)
			rec := httptest.NewRecorder()
			f.h.ServeHTTP(rec, req)
			places["GET /api/runs/{id}"] = rec.Body.String()
			sctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			req = httptest.NewRequest("GET", "/api/runs/"+id.String()+"/events", nil).WithContext(sctx)
			rec = httptest.NewRecorder()
			f.h.ServeHTTP(rec, req)
			cancel()
			places["GET /api/runs/{id}/events"] = rec.Body.String()
			if name == "remote" {
				jid := uuid.MustParse(<-jobIDs)
				job, err := st.GetJob(ctx, jid)
				if err != nil {
					t.Fatal(err)
				}
				places["job result"] = string(job.Result)
			}
			places["server log"] = logs.String()
			if !strings.Contains(places["step rows"], "[redacted:CANARY_KEY]") {
				t.Errorf("the step did not print the secret, so nothing was tested: %s", places["step rows"])
			}
			for _, name := range []string{"JSON_KEY", "PEM_KEY"} {
				if !strings.Contains(places["step rows"], "[redacted:"+name+"]") {
					t.Errorf("%s was not redacted in the step rows", name)
				}
			}
			for where, text := range places {
				for _, m := range scrubMarkers {
					if strings.Contains(text, m) {
						t.Errorf("%s holds part of a multi-line or JSON-escaped secret (%s)", where, m)
					}
				}
				for form, v := range scrubForms() {
					esc, _ := json.Marshal(v) // as it is spelled inside JSON (& → \u0026)
					if strings.Contains(text, v) || strings.Contains(text, string(esc[1:len(esc)-1])) {
						t.Errorf("%s holds the %s form of the secret", where, form)
					}
				}
			}
		})
	}
}

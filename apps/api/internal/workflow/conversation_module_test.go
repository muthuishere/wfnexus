package workflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// mockConversation is a minimal external conversational service: it keys
// tasks on Idempotency-Key, and a task turns done on its second read.
type mockConversation struct {
	mu       sync.Mutex
	key      string
	tasks    map[string]map[string]any // by id
	byIdem   map[string]string
	reads    map[string]int
	never    bool   // never finish
	redirect string // answer every request with a redirect here
	bigBody  bool   // answer a status read with more than 1 MiB
	bigField bool   // a finished task carries a 20 KiB field value
	requests int
	authBad  int
}

func newMockConversation(t *testing.T, key string) (*mockConversation, *httptest.Server) {
	m := &mockConversation{key: key, tasks: map[string]map[string]any{}, byIdem: map[string]string{}, reads: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.requests++
		if m.redirect != "" {
			http.Redirect(w, r, m.redirect+r.URL.Path, http.StatusFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+m.key {
			m.authBad++
			http.Error(w, "unauthorized", 401)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/tasks":
			idem := r.Header.Get("Idempotency-Key")
			if idem == "" {
				http.Error(w, "Idempotency-Key required", 400)
				return
			}
			if id, ok := m.byIdem[idem]; ok {
				_ = json.NewEncoder(w).Encode(map[string]any{"task_id": id, "status": "queued"})
				return
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			id := fmt.Sprintf("t-%d", len(m.tasks)+1)
			m.tasks[id] = body
			m.byIdem[idem] = id
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(map[string]any{"task_id": id, "status": "queued"})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/tasks/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
			if _, ok := m.tasks[id]; !ok {
				http.Error(w, "no such task", 404)
				return
			}
			m.reads[id]++
			if m.bigBody {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "done", "fields": map[string]any{"x": strings.Repeat("a", 1100*1024)}})
				return
			}
			if m.bigField {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "done", "fields": map[string]any{"note": strings.Repeat("b", 20*1024), "name": "Ada"}})
				return
			}
			if m.reads[id] < 2 || m.never {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "running"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "done", "fields": map[string]any{"name": "Ada", "email": "ada@example.com"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return m, srv
}

// runModuleCommand expands one module command the way `wfx module call` does
// and runs its run steps with sh, returning stdout, stderr and the exit code.
func runModuleCommand(t *testing.T, name string, input map[string]any, env []string) (string, string, int) {
	t.Helper()
	root, _ := filepath.Abs(filepath.Join("..", "..", "..", "..", "modules"))
	mods, errs := FindModules("builtin", root)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	var mod *Module
	for _, m := range mods {
		if strings.HasPrefix(name, m.Name+".") {
			mod = m
		}
	}
	task, ok := mod.Callable(name)
	if !ok {
		t.Fatalf("no command %s", name)
	}
	with := map[string]string{}
	for k := range task.Inputs {
		with[k] = fmt.Sprintf(`{{ or .Input.%s "" }}`, k)
	}
	dir := t.TempDir()
	wj, _ := json.Marshal(with)
	write(t, filepath.Join(dir, "call.yaml"), fmt.Sprintf("name: call\nsteps:\n  - id: call\n    uses: %s@0\n    with: %s\n", name, wj))
	defs, err := LoadDirWithTasks(dir, "", catalog(), WithModules(mods))
	if err != nil {
		t.Fatal(err)
	}
	full := map[string]any{}
	for k, s := range task.Inputs {
		if s.Default != nil {
			full[k] = fmt.Sprint(s.Default)
		}
	}
	for k, v := range input {
		full[k] = v
	}
	work := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := 0
	for _, s := range defs["call"].Steps {
		script, denv := renderForTest(t, s.Run, full, work)
		c := exec.Command("sh", "-c", script)
		c.Dir = work
		c.Env = append(append(append(os.Environ(), denv...), "WFX_MODULE_DIR="+mod.Dir), env...)
		c.Stdout, c.Stderr = &stdout, &stderr
		if err := c.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
	}
	return stdout.String(), stderr.String(), code
}

func needNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
}

const convKey = "conv-key-0123456789abcdef"

func convEnv(srvURL string) []string {
	return []string{"WFX_CONVERSATION_URL=" + srvURL + "/v1?token=hidden", "WFX_CONVERSATION_KEY=" + convKey, "WFX_RUN_ID=run-1", "WFX_STEP_ID=ask.start"}
}

var convNames = map[string]any{"service_url_env": "WFX_CONVERSATION_URL", "api_key_env": "WFX_CONVERSATION_KEY"}

func with(base map[string]any, more map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range more {
		out[k] = v
	}
	return out
}

func TestConversationStartPollResult(t *testing.T) {
	needNode(t)
	m, srv := newMockConversation(t, convKey)
	env := convEnv(srv.URL)
	start := with(convNames, map[string]any{"task_type": "collect", "targets": `["+15550100",{"phone":"+15550101"}]`,
		"fields": `["name","email"]`, "channel": "whatsapp"})
	out, errOut, code := runModuleCommand(t, "conversation.start_task", start, env)
	if code != 0 {
		t.Fatalf("start: %d %s", code, errOut)
	}
	var started map[string]any
	if err := json.Unmarshal([]byte(out), &started); err != nil || started["task_id"] != "t-1" {
		t.Fatalf("start output %q", out)
	}
	if body := m.tasks["t-1"]; body["type"] != "collect" || body["channel"] != "whatsapp" || len(body["targets"].([]any)) != 2 {
		t.Fatalf("task sent = %v", body)
	}
	out, errOut, code = runModuleCommand(t, "conversation.task_status",
		with(convNames, map[string]any{"task_id": "t-1", "wait_seconds": "30", "poll_seconds": "0.1"}), env)
	if code != 0 {
		t.Fatalf("status: %d %s", code, errOut)
	}
	var res struct {
		TaskID string            `json:"task_id"`
		Status string            `json:"status"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Status != "done" || res.Fields["email"] != "ada@example.com" || m.reads["t-1"] != 2 {
		t.Fatalf("status output %q reads %d", out, m.reads["t-1"])
	}
	if strings.Contains(out+errOut, convKey) || strings.Contains(out+errOut, "hidden") {
		t.Fatal("the key or the URL query was printed")
	}
}

// A retried step sends the same Idempotency-Key, so the service records one task.
func TestConversationStartIsIdempotent(t *testing.T) {
	needNode(t)
	m, srv := newMockConversation(t, convKey)
	start := with(convNames, map[string]any{"task_type": "collect", "targets": `["a"]`, "fields": `["x"]`, "channel": "sms"})
	var ids []string
	for i := 0; i < 2; i++ {
		out, errOut, code := runModuleCommand(t, "conversation.start_task", start, convEnv(srv.URL))
		if code != 0 {
			t.Fatalf("start %d: %s", i, errOut)
		}
		var r map[string]any
		_ = json.Unmarshal([]byte(out), &r)
		ids = append(ids, fmt.Sprint(r["task_id"]))
	}
	if len(m.tasks) != 1 || ids[0] != ids[1] {
		t.Fatalf("tasks = %d, ids = %v", len(m.tasks), ids)
	}
}

func TestConversationDeadline(t *testing.T) {
	needNode(t)
	m, srv := newMockConversation(t, convKey)
	m.never = true
	m.tasks["t-9"] = map[string]any{}
	_, errOut, code := runModuleCommand(t, "conversation.task_status",
		with(convNames, map[string]any{"task_id": "t-9", "wait_seconds": "1", "poll_seconds": "0.2"}), convEnv(srv.URL))
	if code != 3 || !strings.Contains(errOut, "deadline reached: last status running") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestConversationRefusesBadInput(t *testing.T) {
	needNode(t)
	m, srv := newMockConversation(t, convKey)
	base := with(convNames, map[string]any{"task_type": "collect", "targets": `["a"]`, "fields": `["x"]`, "channel": "sms"})
	many := make([]string, 201)
	for i := range many {
		many[i] = `"t"`
	}
	for name, c := range map[string]struct {
		in   map[string]any
		env  []string
		want string
	}{
		"targets not JSON":  {with(base, map[string]any{"targets": "a,b"}), nil, "targets must be JSON"},
		"too many targets":  {with(base, map[string]any{"targets": "[" + strings.Join(many, ",") + "]"}), nil, "more than 200"},
		"fields not a list": {with(base, map[string]any{"fields": `{"a":1}`}), nil, "fields must be"},
		"long task type":    {with(base, map[string]any{"task_type": strings.Repeat("x", 65)}), nil, "task_type must be 1 to 64"},
		"task type shape":   {with(base, map[string]any{"task_type": "Collect now"}), nil, "task_type must be 1 to 64"},
		"channel shape":     {with(base, map[string]any{"channel": "WhatsApp;rm"}), nil, "channel must be 1 to 64"},
		"key name outside":  {with(base, map[string]any{"api_key_env": "GH_TOKEN"}), []string{"GH_TOKEN=other-secret-0123"}, "starting with WFX_CONVERSATION_"},
		"url name outside":  {with(base, map[string]any{"service_url_env": "HOME"}), nil, "starting with WFX_CONVERSATION_"},
		"bad env name":      {with(base, map[string]any{"api_key_env": "A-B"}), nil, "starting with WFX_CONVERSATION_"},
		"not http":          {base, []string{"WFX_CONVERSATION_URL=file:///etc/passwd"}, "must be an http(s) URL"},
		"plain http remote": {base, []string{"WFX_CONVERSATION_URL=http://conversations.example.com/v1"}, "must be https"},
	} {
		t.Run(name, func(t *testing.T) {
			before := m.requests
			_, errOut, code := runModuleCommand(t, "conversation.start_task", c.in, append(convEnv(srv.URL), c.env...))
			if code != 2 || !strings.Contains(errOut, c.want) {
				t.Fatalf("code %d stderr %q", code, errOut)
			}
			if m.requests != before {
				t.Fatal("a refused input still reached the service")
			}
			if strings.Contains(errOut, "other-secret-0123") {
				t.Fatal("another variable's value was printed")
			}
		})
	}
}

// A service that refuses answers with a status the step can read, and the
// error names neither the key nor the URL's query.
func TestConversationErrorsDoNotPrintTheKey(t *testing.T) {
	needNode(t)
	_, srv := newMockConversation(t, "another-key-entirely")
	_, errOut, code := runModuleCommand(t, "conversation.task_status",
		with(convNames, map[string]any{"task_id": "t-1"}), convEnv(srv.URL))
	if code != 4 || !strings.Contains(errOut, "401") || strings.Contains(errOut, convKey) || strings.Contains(errOut, "hidden") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
}

func TestConversationModuleShape(t *testing.T) {
	m, err := LoadModule(filepath.Join("..", "..", "..", "..", "modules", "conversation"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Commands) != 2 || m.Commands["start_task"].Effect != "write" || m.Commands["task_status"].Effect == "write" {
		t.Fatalf("commands = %+v", m.Commands)
	}
}

// A redirect is never followed, so the key cannot reach another origin.
func TestConversationRefusesRedirects(t *testing.T) {
	needNode(t)
	other, otherSrv := newMockConversation(t, convKey)
	m, srv := newMockConversation(t, convKey)
	m.redirect = otherSrv.URL
	_, errOut, code := runModuleCommand(t, "conversation.task_status", with(convNames, map[string]any{"task_id": "t-1"}), convEnv(srv.URL))
	if code != 4 || !strings.Contains(errOut, "redirects are refused") {
		t.Fatalf("code %d stderr %q", code, errOut)
	}
	if other.requests != 0 {
		t.Fatal("the redirect was followed")
	}
}

// An answer over 1 MiB is refused; a field value over 16 KiB is cut and marked.
func TestConversationCapsTheAnswer(t *testing.T) {
	needNode(t)
	m, srv := newMockConversation(t, convKey)
	m.tasks["t-1"] = map[string]any{}
	m.bigBody = true
	_, errOut, code := runModuleCommand(t, "conversation.task_status", with(convNames, map[string]any{"task_id": "t-1"}), convEnv(srv.URL))
	if code != 4 || !strings.Contains(errOut, "larger than 1048576 bytes") {
		t.Fatalf("big body: code %d stderr %.200q", code, errOut)
	}
	m.bigBody, m.bigField = false, true
	out, errOut, code := runModuleCommand(t, "conversation.task_status", with(convNames, map[string]any{"task_id": "t-1"}), convEnv(srv.URL))
	if code != 0 {
		t.Fatalf("big field: code %d %s", code, errOut)
	}
	var res struct {
		Fields map[string]string `json:"fields"`
	}
	_ = json.Unmarshal([]byte(out), &res)
	if n := res.Fields["note"]; len(n) > 16*1024+20 || !strings.HasSuffix(n, "…[truncated]") || res.Fields["name"] != "Ada" {
		t.Fatalf("fields: note %d bytes, name %q", len(n), res.Fields["name"])
	}
}

// Inside a loop each iteration has its own step id (each[0].…, each[1].…),
// which the engine passes as WFX_STEP_ID: two iterations start two tasks, and
// a retry of one iteration starts none.
func TestConversationLoopIterationsAreSeparateTasks(t *testing.T) {
	needNode(t)
	m, srv := newMockConversation(t, convKey)
	start := with(convNames, map[string]any{"task_type": "collect", "targets": `["a"]`, "fields": `["x"]`, "channel": "sms"})
	env := func(step string) []string {
		return append(convEnv(srv.URL), "WFX_STEP_ID="+step)
	}
	for _, step := range []string{"each[0].ask.start", "each[1].ask.start", "each[1].ask.start"} {
		if _, errOut, code := runModuleCommand(t, "conversation.start_task", start, env(step)); code != 0 {
			t.Fatalf("%s: %s", step, errOut)
		}
	}
	if len(m.tasks) != 2 {
		t.Fatalf("tasks = %d, want 2 (two iterations, one retried)", len(m.tasks))
	}
}

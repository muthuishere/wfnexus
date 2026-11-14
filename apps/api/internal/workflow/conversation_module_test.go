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
	mu      sync.Mutex
	key     string
	tasks   map[string]map[string]any // by id
	byIdem  map[string]string
	reads   map[string]int
	never   bool // never finish
	authBad int
}

func newMockConversation(t *testing.T, key string) (*mockConversation, *httptest.Server) {
	m := &mockConversation{key: key, tasks: map[string]map[string]any{}, byIdem: map[string]string{}, reads: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
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
	return []string{"CONV_URL=" + srvURL + "/v1?token=hidden", "CONV_KEY=" + convKey, "WFX_RUN_ID=run-1", "WFX_STEP_ID=ask.start"}
}

var convNames = map[string]any{"service_url_env": "CONV_URL", "api_key_env": "CONV_KEY"}

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
	_, srv := newMockConversation(t, convKey)
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
		"long task type":    {with(base, map[string]any{"task_type": strings.Repeat("x", 5000)}), nil, "longer than 4096"},
		"bad env name":      {with(base, map[string]any{"api_key_env": "A-B"}), nil, "must name an environment variable"},
		"not http":          {base, []string{"CONV_URL=file:///etc/passwd"}, "must be an http(s) URL"},
	} {
		t.Run(name, func(t *testing.T) {
			_, errOut, code := runModuleCommand(t, "conversation.start_task", c.in, append(convEnv(srv.URL), c.env...))
			if code != 2 || !strings.Contains(errOut, c.want) {
				t.Fatalf("code %d stderr %q", code, errOut)
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

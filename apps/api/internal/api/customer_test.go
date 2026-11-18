package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

type customerFixture struct {
	*tenantFixture
	call    func(method, path, tok string, body any) (int, string)
	acct    string // an account of acme
	dirs    map[string]string
	gitProj string
}

// A server with three projects that have their own plain directories: proj-a
// (acme), proj-b (globex) and proj-g (acme, git-backed), plus an issuer
// account linked to acme. wf-b already exists in proj-b.
func newCustomerFixture(t *testing.T, on bool) *customerFixture {
	t.Helper()
	m := withIssuer(t)
	customerAPIForTest = on
	t.Cleanup(func() { customerAPIForTest = false })
	root := t.TempDir()
	work := filepath.Join(root, "work")
	dirs := map[string]string{}
	var srcs []workflow.Source
	for _, p := range []string{"proj-a", "proj-b", "proj-g"} {
		d := filepath.Join(root, p)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		dirs[p] = d
		srcs = append(srcs, workflow.Source{Name: p, Dir: d})
	}
	// An EXISTING workflow of acme, so a refusal while off cannot be
	// explained by a missing definition.
	if err := os.WriteFile(filepath.Join(dirs["proj-a"], "seeded.yaml"), []byte("name: seeded\nsteps:\n  - id: a\n    prompt: hi\n    output_schema:\n      type: object\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirs["proj-b"], "wf-b.yaml"), []byte("name: wf-b\nsteps:\n  - id: a\n    prompt: hi\n    output_schema:\n      type: object\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		c := exec.Command("git", append([]string{"-C", dirs["proj-g"], "-c", "user.email=t@example.test", "-c", "user.name=t", "-c", "init.defaultBranch=main"}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	os.WriteFile(filepath.Join(dirs["proj-g"], "README"), []byte("x"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "init")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(srcs)
	if err := os.WriteFile(filepath.Join(work, "sources.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "w.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for name, perms := range DefaultRoles() {
		st.UpsertRole(context.Background(), name, perms)
	}
	cfg := config.Config{Addr: "0.0.0.0:8090", WorkDir: work, WorkflowsDir: filepath.Join(root, "local")}
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	if err := eng.ReloadDefinitions(); err != nil {
		t.Fatal(err)
	}
	h := New(eng, st, nil, "0.0.0.0:8090", "", nil)
	f, call := newTenantFixtureOn(t, h, st)
	if c, b := call("POST", "/api/tenants/acme/projects", f.admin, map[string]string{"project": "proj-g"}); c != 200 {
		t.Fatalf("bind proj-g: %d %s", c, b)
	}
	if c, b := call("PUT", "/api/tenants/acme/issuer-links", f.admin, map[string]string{"externalTenant": "ext-1"}); c != 200 {
		t.Fatalf("link: %d %s", c, b)
	}
	return &customerFixture{tenantFixture: f, call: call, acct: m.token(t, nil), dirs: dirs, gitProj: "proj-g"}
}

func hello(name string, steps ...map[string]any) map[string]any {
	if len(steps) == 0 {
		steps = []map[string]any{{"id": "a", "prompt": "Say hello."}}
	}
	for _, st := range steps {
		if _, ok := st["prompt"]; ok {
			if _, has := st["output_schema"]; !has {
				st["output_schema"] = map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}
			}
		}
	}
	return map[string]any{"name": name, "steps": steps}
}

func (f *customerFixture) save(project string, def map[string]any) (int, string) {
	return f.call("POST", "/api/svc/workflows", f.acct, map[string]any{"project": project, "definition": def})
}

// OFF: every customer route is a 404 that does nothing.
func TestTheCustomerAPIIsOffAndDoesNothing(t *testing.T) {
	f := newCustomerFixture(t, false)
	for _, rt := range []struct {
		m, p string
		b    any
	}{
		{"GET", "/api/svc/workflows", nil},
		{"GET", "/api/svc/workflows/wf-b", nil},
		{"POST", "/api/svc/workflows/validate", map[string]any{"project": "proj-a", "definition": hello("x")}},
		{"POST", "/api/svc/workflows", map[string]any{"project": "proj-a", "definition": hello("made-while-off")}},
		{"POST", "/api/svc/workflows/seeded/runs", map[string]any{}},
		{"GET", "/api/svc/workflows/seeded", nil},
	} {
		if c, b := f.call(rt.m, rt.p, f.acct, rt.b); c != 404 {
			t.Fatalf("%s %s while off: %d %s", rt.m, rt.p, c, b)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dirs["proj-a"], "made-while-off.yaml")); err == nil {
		t.Fatal("a workflow was written while the customer API is off")
	}
	if n := runsOf(t, f.st, "seeded"); n != 0 {
		t.Fatalf("%d runs started while the customer API is off", n)
	}
	// The same calls switched ON succeed, so the 404s above are the off
	// switch and nothing else.
	customerAPIForTest = true
	on := newCustomerFixture(t, true)
	if c, b := on.call("GET", "/api/svc/workflows/seeded", on.acct, nil); c != 200 {
		t.Fatalf("seeded workflow while on: %d %s", c, b)
	}
	if c, b := on.call("POST", "/api/svc/workflows/seeded/runs", on.acct, map[string]any{}); c != 201 {
		t.Fatalf("seeded run while on: %d %s", c, b)
	}
}

func TestACustomerSeesAndSavesOnlyItsTenantsWorkflows(t *testing.T) {
	f := newCustomerFixture(t, true)
	if c, b := f.save("proj-a", hello("greet")); c != 200 {
		t.Fatalf("save: %d %s", c, b)
	}
	if _, err := os.Stat(filepath.Join(f.dirs["proj-a"], "greet.yaml")); err != nil {
		t.Fatalf("not written: %v", err)
	}
	c, b := f.call("GET", "/api/svc/workflows", f.acct, nil)
	if c != 200 || !strings.Contains(b, "greet") || strings.Contains(b, "wf-b") {
		t.Fatalf("list: %d %s", c, b)
	}
	// Another tenant's workflow, an absent one: the same answer.
	_, absent := f.call("GET", "/api/svc/workflows/no-such", f.acct, nil)
	if c, b := f.call("GET", "/api/svc/workflows/wf-b", f.acct, nil); c != 404 || b != absent {
		t.Fatalf("another tenant's workflow: %d %s", c, b)
	}
	// Saving into another tenant's project, or an unbound one: the same 404,
	// and nothing written.
	for _, p := range []string{"proj-b", "local", "no-such"} {
		if c, b := f.save(p, hello("intrude")); c != 404 || b != absent {
			t.Fatalf("save into %s: %d %s", p, c, b)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dirs["proj-b"], "intrude.yaml")); err == nil {
		t.Fatal("written into another tenant's project")
	}
	// A git-backed project: refused, nothing proposed.
	if c, _ := f.save(f.gitProj, hello("viagit")); c != 409 {
		t.Fatalf("git-backed project: %d", c)
	}
}

// Only an issuer account: a principal, a person and anonymous are refused.
func TestOnlyAnIssuerAccountReachesTheCustomerAPI(t *testing.T) {
	f := newCustomerFixture(t, true)
	for _, tok := range []string{f.tokA, f.admin, ""} {
		if c, _ := f.call("GET", "/api/svc/workflows", tok, nil); c != 404 && c != 401 {
			t.Fatalf("a non-account listed customer workflows: %d", c)
		}
		if c, _ := f.call("POST", "/api/svc/workflows", tok, map[string]any{"project": "proj-a", "definition": hello("nope")}); c != 404 && c != 401 {
			t.Fatalf("a non-account saved: %d", c)
		}
	}
	if _, err := os.Stat(filepath.Join(f.dirs["proj-a"], "nope.yaml")); err == nil {
		t.Fatal("a non-account wrote a workflow")
	}
	// Connections stay the operator's.
	if c, _ := f.call("GET", "/api/tenants/acme/connections", f.acct, nil); c != 403 {
		t.Fatalf("an account reached connection management: %d", c)
	}
	if c, _ := f.call("POST", "/api/tenants/acme/connections", f.acct, map[string]any{"name": "x", "endpoint": "https://e.example.test"}); c != 403 {
		t.Fatalf("an account created a connection: %d", c)
	}
}

// Nothing that acts without an approval gate is accepted.
func TestACustomerWorkflowCannotActWithoutApproval(t *testing.T) {
	f := newCustomerFixture(t, true)
	bad := map[string]map[string]any{
		"run":       hello("s-run", map[string]any{"id": "a", "run": "echo hi"}),
		"uses":      hello("s-uses", map[string]any{"id": "a", "uses": "gh.pr-create"}),
		"tools":     hello("s-tools", map[string]any{"id": "a", "prompt": "p", "tools": []string{"bash"}}),
		"skills":    hello("s-skills", map[string]any{"id": "a", "prompt": "p", "skills": []string{"x"}}),
		"env":       hello("s-env", map[string]any{"id": "a", "prompt": "p", "env": map[string]string{"A": "${SECRET}"}}),
		"start":     hello("s-start", map[string]any{"id": "a", "start": map[string]any{"workflow": "wf-b"}}),
		"send":      hello("s-send", map[string]any{"id": "a", "send": map[string]any{"text": "hi"}}),
		"runs-on":   hello("s-runson", map[string]any{"id": "a", "prompt": "p", "runs_on": "box"}),
		"mcp":       hello("s-mcp", map[string]any{"id": "a", "prompt": "p", "mcp": []string{"crm"}}),
		"nested":    hello("s-nested", map[string]any{"id": "l", "foreach": []any{1}, "steps": []any{map[string]any{"id": "x", "run": "echo"}}}),
		"def env":   {"name": "d-env", "env": map[string]string{"A": "b"}, "steps": []any{map[string]any{"id": "a", "prompt": "p"}}},
		"def mount": {"name": "d-mount", "mount": []any{"/tmp:/data:ro"}, "steps": []any{map[string]any{"id": "a", "prompt": "p"}}},
		"trigger":   {"name": "d-on", "on": map[string]any{"schedule": []any{map[string]any{"cron": "* * * * *"}}}, "steps": []any{map[string]any{"id": "a", "prompt": "p"}}},
		"team":      hello("s-team", map[string]any{"id": "a", "prompt": "p", "team": []any{map[string]any{"id": "m", "prompt": "x"}}}),
		"state":     hello("s-state", map[string]any{"id": "a", "prompt": "p", "state": map[string]any{"project": map[string]any{"k": "v"}}}),
		"memory":    hello("s-memory", map[string]any{"id": "a", "prompt": "p", "memory": true}),
		"def runs":  {"name": "d-runson", "runs_on": "box", "steps": []any{map[string]any{"id": "a", "prompt": "p"}}},
		"def jobs":  {"name": "d-jobs", "jobs": map[string]any{"j": map[string]any{"steps": []any{map[string]any{"id": "a", "prompt": "p"}}}}},
		"def files": {"name": "d-files", "files": []any{map[string]any{"path": "a.txt", "content": "b"}}, "steps": []any{map[string]any{"id": "a", "prompt": "p"}}},
		"hook":      {"name": "d-hook", "on": map[string]any{"repository_dispatch": map[string]any{}}, "steps": []any{map[string]any{"id": "a", "prompt": "p"}}},
		"message":   {"name": "d-msg", "on": map[string]any{"message": []any{map[string]any{"channel": "c"}}}, "steps": []any{map[string]any{"id": "a", "prompt": "p"}}},
		"call":      {"name": "d-call", "on": map[string]any{"workflowCall": true}, "steps": []any{map[string]any{"id": "a", "prompt": "p"}}},
	}
	for what, def := range bad {
		c, b := f.save("proj-a", def)
		// Refused by THIS guard, not by some other check on the way.
		if c != 400 || !strings.Contains(b, "not available to customer workflows") {
			t.Errorf("%s: %d %s", what, c, b)
		}
		if _, err := os.Stat(filepath.Join(f.dirs["proj-a"], def["name"].(string)+".yaml")); err == nil {
			t.Errorf("%s was written", what)
		}
	}
	// Even behind requires_approval, no mcp yet.
	gated := hello("s-mcp-gated", map[string]any{"id": "a", "prompt": "p", "mcp": []string{"crm"}, "requires_approval": true})
	if c, b := f.save("proj-a", gated); c != 400 || !strings.Contains(b, "not available to customer workflows") {
		t.Fatalf("an mcp step was accepted: %d %s", c, b)
	}
	// A plain agent step is accepted.
	if c, b := f.save("proj-a", hello("plain-ok")); c != 200 {
		t.Fatalf("a plain agent workflow was refused: %d %s", c, b)
	}
}

// Switched on, an account runs its tenant's workflows and nothing else.
func TestACustomerRunsOnlyItsTenantsWorkflows(t *testing.T) {
	f := newCustomerFixture(t, true)
	if c, b := f.save("proj-a", hello("runme")); c != 200 {
		t.Fatalf("save: %d %s", c, b)
	}
	c, b := f.call("POST", "/api/svc/workflows/proj-a%2Frunme/runs", f.acct, map[string]any{})
	name := "proj-a/runme"
	if c == 404 {
		// the loader may key a project's workflow by its short name
		name = "runme"
		c, b = f.call("POST", "/api/svc/workflows/runme/runs", f.acct, map[string]any{})
	}
	if c != 201 {
		t.Fatalf("run %s: %d %s", name, c, b)
	}
	var got struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	json.Unmarshal([]byte(b), &got)
	if c, _ := f.call("GET", "/api/svc/runs/"+got.Run.ID, f.acct, nil); c != 200 {
		t.Fatalf("the run is not in the account's tenant: %d", c)
	}
	_, absent := f.call("POST", "/api/svc/workflows/no-such/runs", f.acct, map[string]any{})
	if c, b := f.call("POST", "/api/svc/workflows/wf-b/runs", f.acct, map[string]any{}); c != 404 || b != absent {
		t.Fatalf("another tenant's workflow: %d %s", c, b)
	}
	if n := runsOf(t, f.st, "wf-b"); n != 0 {
		t.Fatalf("%d runs of another tenant's workflow", n)
	}
	_ = http.StatusOK
}

package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Cross-tenant paths the #189 spec does not reach, closed with the fix. Same
// fixture and helpers as tenancy_test.go.

// The shipped templates are readable by every tenant, so changeable by none:
// their state would otherwise be a channel between organizations.
func TestOrgCannotWriteTheSharedTemplatesSource(t *testing.T) {
	f := newOrgFixture(t, "")
	r := f.do(f.alice, "PUT", "/api/state", map[string]any{"scope": "project", "name": "templates", "key": "k", "value": "v"})
	if r.code/100 == 2 {
		t.Errorf("alice (org-admin) wrote state of the shared templates source: %d %.200s", r.code, r.body)
	}
}

// The global state namespace is the server's: an organization reaches it
// neither through the operator's door (no scope, or scope=global) nor
// through a run of its own.
func TestOrgCannotReachGlobalState(t *testing.T) {
	f := newOrgFixture(t, "")
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/state", nil},
		{"GET", "/api/state?scope=global", nil},
		{"GET", "/api/runs/" + f.runA + "/state?scope=global", nil},
		{"POST", "/api/runs/" + f.runA + "/state", map[string]any{"scope": "global", "key": "k", "value": "v"}},
	} {
		if r := f.do(f.alice, c.method, c.path, c.body); r.code != 404 {
			t.Errorf("alice %s %s: got %d, want 404: %.200s", c.method, c.path, r.code, r.body)
		}
	}
	// A person in no organization keeps it.
	if r := f.do(f.carol, "GET", "/api/state?scope=global", nil); r.code != 200 {
		t.Errorf("carol (no tenant) lost the global state: %d", r.code)
	}
}

// A copy saved under a bare name another tenant's workflow holds answers
// with the copy, never with the other tenant's definition.
func TestOrgCopyNeverAnswersWithAnotherTenantsDefinition(t *testing.T) {
	f := newOrgFixture(t, "")
	r := f.do(f.alice, "POST", "/api/templates/"+orgAcmeT+"/copy", map[string]any{"as": orgGlobexW, "project": orgAcmeP})
	if hits := f.leaks(r.body); len(hits) > 0 && strings.Contains(r.body, "globex-secret-workflow") {
		t.Errorf("the copy answered with globex's definition: %d %.300s", r.code, r.body)
	}
}

// Channel adapters are the operator's configuration.
func TestOrgCannotListChannelAdapters(t *testing.T) {
	f := newOrgFixture(t, "")
	if r := f.do(f.alice, "GET", "/api/channels", nil); r.code != 403 {
		t.Errorf("alice listed the channel adapters: %d %.200s", r.code, r.body)
	}
	if r := f.do(f.carol, "GET", "/api/channels", nil); r.code != 200 {
		t.Errorf("carol (no tenant) lost the channel list: %d", r.code)
	}
}

// ---- the run guard on every path that executes (orgPolicy: guarded) ----

// orgShellRun is a run of acme's shell workflow that did not come through the
// guard: started by an operator, or before the policy was set.
func (f *orgFixture) orgShellRun(t *testing.T) string {
	t.Helper()
	r, err := f.st.CreateRun(context.Background(), orgAcmeP, "acme-shell", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.st.UpdateRun(context.Background(), r.ID, "failed", "sh", "x")
	return r.ID.String()
}

func runStatus(t *testing.T, st *store.Store, id string) (string, string) {
	t.Helper()
	r, err := st.GetRun(context.Background(), uuid.MustParse(id))
	if err != nil {
		t.Fatal(err)
	}
	return r.Status, r.Error
}

// A guarded organization's person cannot retry, approve, answer or fill in a
// run of a workflow with a shell step — nor can anyone retry it, the tenant's
// policy being guarded.
func TestOrgGuardedCannotResumeAShellRun(t *testing.T) {
	f := newOrgFixture(t, orgGuarded)
	id := f.orgShellRun(t)
	for _, who := range []*orgUser{f.alice, f.mallory} {
		for _, a := range []string{"retry", "approve", "answer", "input"} {
			if r := f.do(who, "POST", "/api/runs/"+id+"/"+a, map[string]any{"stepId": "sh"}); r.code/100 == 2 {
				t.Errorf("%s could %s a shell run under the guard: %d %.200s", who.name, a, r.code, r.body)
			}
		}
	}
	if r := f.do(f.root, "POST", "/api/runs/"+id+"/retry", map[string]any{"stepId": "sh"}); r.code/100 == 2 {
		t.Errorf("a retry of a shell run in a guarded tenant went through: %d %.200s", r.code, r.body)
	}
	f.eng.WaitIdle(10 * time.Second)
	if st, _ := runStatus(t, f.st, id); st != "failed" {
		t.Errorf("the refused run moved to %q", st)
	}
}

// Whatever starts a run — here the engine's own trigger path, which a
// schedule, a message and a hook take — a guarded tenant's shell workflow
// does not start. The same call without the policy does (the control).
func TestOrgGuardedTenantStartsNoShellRunFromATrigger(t *testing.T) {
	for _, c := range []struct {
		cfg     string
		refused bool
	}{{orgGuarded, true}, {"", false}} {
		f := newOrgFixture(t, c.cfg)
		orgYAML(t, f.dirs[orgAcmeP], "acme-cron.yaml", "name: acme-cron\non:\n  schedule:\n    - cron: \"0 3 * * *\"\nsteps:\n  - id: sh\n    run: \"true\"\n")
		if err := f.eng.ReloadDefinitions(); err != nil {
			t.Fatal(err)
		}
		before := runsOf(t, f.st, "acme-cron")
		err := f.eng.StartTriggered(context.Background(), "acme-cron", workflow.TriggerSchedule, nil)
		f.eng.WaitIdle(10 * time.Second)
		after := runsOf(t, f.st, "acme-cron")
		if c.refused && (err == nil || after != before) {
			t.Errorf("a scheduled shell run of a guarded tenant started: err %v, runs %d -> %d", err, before, after)
		}
		if !c.refused && (err != nil || after != before+1) {
			t.Errorf("control: without the policy the scheduled run should start: err %v, runs %d -> %d", err, before, after)
		}
	}
}

// A run that reaches execution without passing the guard at creation — it
// was queued before the policy — fails with the reason when it resumes.
func TestOrgGuardedShellRunFailsWhenItResumes(t *testing.T) {
	f := newOrgFixture(t, orgGuarded)
	r, err := f.st.CreateRun(context.Background(), orgAcmeP, "acme-shell", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	f.eng.Start(r.ID)
	f.eng.WaitIdle(10 * time.Second)
	if st, msg := runStatus(t, f.st, r.ID.String()); st != "failed" || !strings.Contains(msg, "guarded") {
		t.Errorf("a queued shell run of a guarded tenant ran: %s %q", st, msg)
	}
}

// A hook token fires only its own project's workflow, and a guarded
// tenant's shell workflow does not start from a hook even with a valid token.
func TestOrgHooksResolveInsideTheTokensProjectAndAreGuarded(t *testing.T) {
	hookToken := func(f *orgFixture, wf string) string {
		r := f.do(f.root, "POST", "/api/workflows/"+wf+"/expose/hook", map[string]any{})
		var v struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(r.body), &v); err != nil || v.Token == "" {
			t.Fatalf("issuing a hook token for %s: %d %.200s", wf, r.code, r.body)
		}
		return v.Token
	}
	fire := func(f *orgFixture, path, tok string) int {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, req)
		return w.Code
	}
	f := newOrgFixture(t, "")
	tok := hookToken(f, orgAcmeP+"%2F"+orgShared)
	before := runsOfProject(t, f.st, orgGlobexP)
	if c := fire(f, "/hooks/"+orgGlobexP+"/"+orgShared, tok); c/100 == 2 {
		t.Errorf("acme's hook token fired globex's %q: %d", orgShared, c)
	}
	if after := runsOfProject(t, f.st, orgGlobexP); after != before {
		t.Errorf("a globex run started from acme's token (%d -> %d)", before, after)
	}
	if c := fire(f, "/hooks/"+orgAcmeP+"/"+orgShared, tok); c/100 != 2 {
		t.Errorf("acme's token no longer fires acme's own %q: %d", orgShared, c)
	}
	f.eng.WaitIdle(10 * time.Second)

	// The control: without the policy the same token fires the shell
	// workflow, so a refusal under the policy is the guard's doing.
	// (Tokens are issued by the qualified name, which is the name a hook
	// URL resolves to.)
	if c := fire(f, "/hooks/"+orgAcmeP+"/acme-shell", hookToken(f, orgAcmeP+"%2Facme-shell")); c/100 != 2 {
		t.Fatalf("control: a hook without the policy did not start acme-shell: %d", c)
	}
	f.eng.WaitIdle(10 * time.Second)

	g := newOrgFixture(t, orgGuarded)
	gtok := hookToken(g, orgAcmeP+"%2Facme-shell")
	before = runsOf(t, g.st, "acme-shell")
	if c := fire(g, "/hooks/"+orgAcmeP+"/acme-shell", gtok); c/100 == 2 {
		t.Errorf("a hook started a guarded tenant's shell workflow: %d", c)
	}
	if after := runsOf(t, g.st, "acme-shell"); after != before {
		t.Errorf("a hook created a run of acme-shell under the guard (%d -> %d)", before, after)
	}
}

// Module commands are the operator's: an organization's person cannot call
// one.
func TestOrgCannotCallModuleCommands(t *testing.T) {
	f := newOrgFixture(t, "")
	if r := f.do(f.alice, "POST", "/api/modules/any/call", map[string]any{"command": "x"}); r.code/100 == 2 {
		t.Errorf("alice called a module command: %d %.200s", r.code, r.body)
	}
}

// ---- state and memory are the tenant's own ----

// Two tenants each run a workflow called "deploy": each one's state and memory
// are its own, through the run, the workflow page and the operator's door.
func TestOrgStateAndMemoryAreKeptPerTenant(t *testing.T) {
	f := newOrgFixture(t, "")
	ctx := context.Background()
	run := func(project string) string {
		r, err := f.st.CreateRun(ctx, project, orgShared, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		return r.ID.String()
	}
	ra, rg := run(orgAcmeP), run(orgGlobexP)
	for _, c := range []struct {
		who      *orgUser
		run, val string
	}{{f.alice, ra, "acme-own-state"}, {f.bob, rg, "globex-own-state"}} {
		if r := f.do(c.who, "POST", "/api/runs/"+c.run+"/state", map[string]any{"scope": "workflow", "key": "k", "value": c.val}); r.code != 200 {
			t.Fatalf("%s writing run state: %d %.200s", c.who.name, r.code, r.body)
		}
		if r := f.do(c.who, "PUT", "/api/workflows/"+orgShared+"/memory/file?target=index", map[string]any{"body": c.val + "-memory"}); r.code != 200 {
			t.Fatalf("%s writing memory: %d %.200s", c.who.name, r.code, r.body)
		}
	}
	for _, c := range []struct {
		who       *orgUser
		run       string
		own, other string
	}{{f.alice, ra, "acme-own-state", "globex-own-state"}, {f.bob, rg, "globex-own-state", "acme-own-state"}} {
		for _, p := range []string{
			"/api/runs/" + c.run + "/state?scope=workflow",
			"/api/workflows/" + orgShared + "/state",
			"/api/state?scope=workflow&name=" + orgShared,
			"/api/workflows/" + orgShared + "/memory/file?target=index",
		} {
			r := f.do(c.who, "GET", p, nil)
			if !strings.Contains(r.body, c.own) || strings.Contains(r.body, c.other) {
				t.Errorf("%s GET %s: want only %q, got %d %.300s", c.who.name, p, c.own, r.code, r.body)
			}
		}
	}
}

// ---- no enumeration oracle ----

// For a person in an organization, a workflow name another tenant holds and
// a name nobody holds answer alike — status, body, error — on every verb.
// Saving such a name into one's own project simply works.
func TestOrgAnotherTenantsNameIsIndistinguishableFromNone(t *testing.T) {
	const foreign, nowhere = orgGlobexW, "nowhere-wf"
	norm := func(body, name string) string { return strings.ReplaceAll(body, name, "NAME") }
	for _, c := range []struct {
		method, path string
		body         func(string) any
	}{
		{"GET", "/api/workflows/%s", nil},
		{"POST", "/api/workflows/%s/runs", func(string) any { return map[string]any{} }},
		{"POST", "/api/workflows/%s/dryrun", func(string) any { return map[string]any{} }},
		{"DELETE", "/api/workflows/%s", nil},
		{"PUT", "/api/workflows/%s", func(n string) any { return orgDef(n) }},
		{"PUT", "/api/workflows/%s?project=" + orgAcmeP, func(n string) any { return orgDef(n) }},
		{"GET", "/api/workflows/%s/state", nil},
	} {
		res := map[string]orgRes{}
		for _, n := range []string{foreign, nowhere} {
			f := newOrgFixture(t, "") // fresh: a save must not change the next answer
			var body any
			if c.body != nil {
				body = c.body(n)
			}
			r := f.do(f.alice, c.method, strings.Replace(c.path, "%s", n, 1), body)
			// Each fixture has its own temp directory; only that may differ.
			r.body = norm(strings.ReplaceAll(r.body, filepath.Dir(f.dirs[orgAcmeP]), "ROOT"), n)
			res[n] = r
		}
		a, b := res[foreign], res[nowhere]
		if a.code != b.code || strings.TrimSpace(a.body) != strings.TrimSpace(b.body) {
			t.Errorf("%s %s: another tenant's name answers %d %.200s; a name nobody holds answers %d %.200s",
				c.method, c.path, a.code, a.body, b.code, b.body)
		}
	}
	// The save into her own project worked, and her bare name is hers now.
	f := newOrgFixture(t, "")
	if r := f.do(f.alice, "PUT", "/api/workflows/"+foreign+"?project="+orgAcmeP, orgDef(foreign)); r.code != 200 {
		t.Fatalf("alice saving %q into her own project: %d %.300s", foreign, r.code, r.body)
	}
	if r := f.do(f.alice, "GET", "/api/workflows/"+foreign, nil); r.code != 200 || strings.Contains(r.body, "globex-secret-workflow") {
		t.Errorf("alice's bare %q is not her own: %d %.300s", foreign, r.code, r.body)
	}
	if r := f.do(f.bob, "GET", "/api/workflows/"+foreign, nil); r.code != 200 || !strings.Contains(r.body, "globex-secret-workflow") {
		t.Errorf("bob lost his own %q: %d %.300s", foreign, r.code, r.body)
	}
}

// ---- the stored project prefix cannot be forged ----

func TestOrgProjectNamesCannotForgeTheTenantPrefix(t *testing.T) {
	f := newOrgFixture(t, "")
	for _, name := range []string{orgAcme + "--x", orgGlobex + "--x", "a--b"} {
		for _, body := range []map[string]any{
			{"name": name, "create": true},
			{"name": name, "repo": "https://git.example.test/x.git"},
		} {
			if r := f.do(f.bob, "POST", "/api/projects", body); r.code != 400 {
				t.Errorf("bob creating %v: got %d, want 400: %.200s", body, r.code, r.body)
			}
		}
	}
	// Visibility is the binding, never the prefix: an unbound project the
	// operator named "acme--sneaky" is not acme's.
	if r := f.do(f.root, "POST", "/api/projects", map[string]any{"name": orgAcme + "--sneaky", "create": true}); r.code != 201 {
		t.Fatalf("the operator creating an unbound project: %d %.200s", r.code, r.body)
	}
	for _, p := range []string{"/api/projects/sneaky", "/api/projects/" + orgAcme + "--sneaky"} {
		if r := f.do(f.alice, "GET", p, nil); r.code != 404 {
			t.Errorf("alice GET %s: got %d, want 404", p, r.code)
		}
	}
	if r := f.do(f.alice, "GET", "/api/projects", nil); strings.Contains(r.body, "sneaky") {
		t.Errorf("alice lists the unbound %q project: %.300s", "acme--sneaky", r.body)
	}
	if r := f.do(f.carol, "GET", "/api/projects/"+orgAcme+"--sneaky", nil); r.code != 200 {
		t.Errorf("carol (no tenant) cannot read the unbound project: %d", r.code)
	}
}

// ---- server-wide numbers ----

// The Prometheus endpoint is the whole server's: on an install with tenants
// only a superadmin reads it.
func TestOrgPrometheusIsTheOperatorsWithOrganizations(t *testing.T) {
	f := newOrgFixture(t, "")
	for _, who := range []*orgUser{f.alice, f.carol} {
		if r := f.do(who, "GET", "/metrics", nil); r.code != 403 {
			t.Errorf("%s GET /metrics on an install with organizations: got %d, want 403", who.name, r.code)
		}
	}
	if r := f.do(f.root, "GET", "/metrics", nil); r.code != 200 {
		t.Errorf("the superadmin lost /metrics: %d", r.code)
	}
	// Without tenants, as before: a signed-in person reads it.
	h, st := testServer(t, "127.0.0.1:8090")
	u := &store.User{Name: "plain"}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/metrics", nil)
	req = req.WithContext(withSubject(req.Context(), &Subject{Kind: SubjectUser, Name: u.Name, User: u}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("without organizations a signed-in person lost /metrics: %d", w.Code)
	}
}

// The runners are the server's: an organization's metrics leave them out.
func TestOrgMetricsLeaveOutTheRunners(t *testing.T) {
	f := newOrgFixture(t, "")
	if err := f.st.RegisterWorker(context.Background(), &store.Worker{Name: "runner-1"}, "h"); err != nil {
		t.Fatal(err)
	}
	read := func(who *orgUser) store.Metrics {
		var m store.Metrics
		r := f.do(who, "GET", "/api/metrics", nil)
		if err := json.Unmarshal([]byte(r.body), &m); err != nil {
			t.Fatalf("%d %s", r.code, r.body)
		}
		return m
	}
	if m := read(f.alice); m.Workers.Total != 0 || len(m.Workers.Workers) != 0 {
		t.Errorf("alice's metrics show the server's runners: %+v", m.Workers)
	}
	if m := read(f.carol); m.Workers.Total != 1 {
		t.Errorf("carol (no tenant) lost the runners: %+v", m.Workers)
	}
}

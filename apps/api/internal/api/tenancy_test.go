package api

// Organization (tenant) isolation of the BROWSER API — the spec for #189.
//
// These tests are written before the fix and are expected to FAIL on code
// that predates it. What they pin down:
//
//   - A signed-in user bound to a tenant sees only that tenant's projects,
//     workflows, runs, state, memory, bundles, proposals, events,
//     conversations and metrics.
//   - Every read and write addressed to ANOTHER tenant's project, workflow,
//     run, proposal or bundle answers 404 — not 403, not 200 — and changes
//     nothing. The 404 body is the same as for a thing that never existed.
//   - Org members may create projects; a new project is bound to the
//     creator's tenant, and two tenants may each use the same short name.
//   - Every browser route under /api/ is either tenant-checked by a case in
//     orgCases() or named in orgRouteAllowList, with a reason. A new route
//     that is neither fails TestOrgEveryBrowserRouteIsTenantChecked.
//   - With `orgPolicy: guarded`, org members cannot save or start a workflow
//     that has a `run:` shell step or a file-write tool.
//   - A user with no tenant (a single-owner install) sees what they see today.
//
// The database is Postgres (the package's own throwaway database, pgtest):
// TEST_DATABASE_URL names the server.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/muthuishere/wfnexus/apps/api/internal/blob"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/store/pgtest"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The package's Postgres database is created on first use and dropped when
// the package's tests finish (pgtest.Main).
func TestMain(m *testing.M) { pgtest.Main(m) }

// ---------------------------------------------------------------------------
// The ONE place a tenant-bound user subject is made.
//
// Until user->tenant binding (#188) lands, nothing in the request path puts a
// tenant on a USER subject, so the subject is built here and placed on the
// request context directly, the way actor_test.go and tenants_test.go do. The
// server is bound to loopback so requireSubject passes a request without a
// credential through with its context intact; authorize and every handler
// then see exactly this subject.
//
// When #188 lands, adapt orgSubject/orgAs (and only them): e.g. issue a real
// session for a user bound to the tenant and send it as a bearer.
// ---------------------------------------------------------------------------

// Org roles as #188 names them.
const (
	orgRoleAdmin  = "org-admin"
	orgRoleMember = "member"
)

type orgUser struct {
	name    string
	tenant  string // "" = not in any organization (single-owner install)
	orgRole string // orgRoleAdmin | orgRoleMember | ""
	u       *store.User
}

// orgSubject is the subject #188 resolves for a user.
func orgSubject(who *orgUser) *Subject {
	sub := &Subject{Kind: SubjectUser, Name: who.u.Name, Role: who.u.Role, User: who.u, Tenant: who.tenant}
	// Subject.OrgRole arrives with #188. Set it when the field exists so the
	// helper keeps compiling on main and starts carrying the role the moment
	// the field is there.
	if f := reflect.ValueOf(sub).Elem().FieldByName("OrgRole"); f.IsValid() && f.CanSet() && f.Kind() == reflect.String {
		f.SetString(who.orgRole)
	}
	return sub
}

// orgAs puts who's subject on the request. Every request in this file goes
// through it.
func orgAs(r *http.Request, who *orgUser) *http.Request {
	if who == nil {
		return r
	}
	return r.WithContext(withSubject(r.Context(), orgSubject(who)))
}

// ---------------------------------------------------------------------------
// Fixture
// ---------------------------------------------------------------------------

const (
	orgAcme    = "acme"
	orgGlobex  = "globex"
	orgAcmeP   = "acme-app"   // acme's project
	orgGlobexP = "globex-app" // globex's project
	orgSoloP   = "solo-app"   // bound to no tenant (the default tenant)
	orgAcmeW   = "acme-only"
	orgGlobexW = "globex-only"
	orgShared  = "deploy" // a workflow short name BOTH tenants use (#163)
	orgGlobexT = "globex-starter"
	orgAcmeT   = "acme-starter"
)

type orgFixture struct {
	t       *testing.T
	h       http.Handler // the real router (loopback bind; see orgAs)
	bundles http.Handler // the bundle routes, see orgBundleRouter
	st      *store.Store
	eng     *engine.Engine
	dirs    map[string]string

	alice   *orgUser // acme, org-admin, no member rows
	mallory *orgUser // acme, member; project_admin of acme-app AND leftover grants on globex's items
	bob     *orgUser // globex, member
	carol   *orgUser // no tenant (single-owner install), no grants
	root    *orgUser // superadmin, no tenant

	runA, runG    string   // the first run of each tenant
	runsG         []string // every globex run
	propA, propG  string
	digA, digG    string
	globexMarkers []string // substrings that may only ever reach a globex caller
	acmeMarkers   []string
}

var (
	orgMigrated bool
	orgResetSQL string
)

// orgBuildReset runs once, on the freshly migrated database: it copies what
// the migrations seeded into a side schema and returns one script that
// empties every table (children before parents) and puts the seed back.
// DELETE in dependency order, because TRUNCATE costs about a second per
// fixture. Every name comes from the catalog, never from input.
func orgBuildReset(t *testing.T, db *sql.DB) string {
	t.Helper()
	q := func(query string) [][2]string {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out [][2]string
		for rows.Next() {
			var a, b string
			_ = rows.Scan(&a, &b)
			out = append(out, [2]string{a, b})
		}
		return out
	}
	var tables []string
	for _, r := range q(`SELECT tablename, '' FROM pg_tables WHERE schemaname='public' AND tablename <> 'schema_migrations' ORDER BY tablename`) {
		tables = append(tables, r[0])
	}
	// child -> parents
	parents := map[string][]string{}
	for _, r := range q(`SELECT c.conrelid::regclass::text, c.confrelid::regclass::text FROM pg_constraint c
		JOIN pg_namespace n ON n.oid = c.connamespace WHERE c.contype='f' AND n.nspname='public'`) {
		child, parent := strings.Trim(r[0], `"`), strings.Trim(r[1], `"`)
		if child != parent {
			parents[child] = append(parents[child], parent)
		}
	}
	// Parents first.
	var order []string
	seen := map[string]int{}
	var visit func(string)
	visit = func(n string) {
		if seen[n] != 0 {
			return
		}
		seen[n] = 1
		for _, p := range parents[n] {
			visit(p)
		}
		seen[n] = 2
		order = append(order, n)
	}
	for _, tb := range tables {
		visit(tb)
	}
	var b strings.Builder
	b.WriteString("DROP SCHEMA IF EXISTS orgseed CASCADE; CREATE SCHEMA orgseed;\n")
	for _, tb := range order {
		fmt.Fprintf(&b, "CREATE TABLE orgseed.%q AS SELECT * FROM public.%q;\n", tb, tb)
	}
	if _, err := db.Exec(b.String()); err != nil {
		t.Fatalf("saving the seed: %v", err)
	}
	b.Reset()
	b.WriteString("BEGIN;\n")
	for i := len(order) - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "DELETE FROM public.%q;\n", order[i])
	}
	for _, tb := range order {
		fmt.Fprintf(&b, "INSERT INTO public.%q SELECT * FROM orgseed.%q;\n", tb, tb)
	}
	b.WriteString("COMMIT;\n")
	return b.String()
}

// orgStore opens the package's Postgres database with every table emptied, so
// each fixture starts from nothing and a case that (wrongly, today) mutates
// another tenant's data cannot leak into the next case.
func orgStore(t *testing.T) *store.Store {
	t.Helper()
	dsn, err := pgtest.DSN()
	if err != nil {
		pgtest.Unavailable(t, fmt.Errorf("%w — point TEST_DATABASE_URL at a Postgres server", err))
	}
	if !orgMigrated {
		if err := store.Migrate("postgres", dsn); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		orgMigrated = true
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if orgResetSQL == "" {
		orgResetSQL = orgBuildReset(t, db)
	}
	if _, err := db.Exec(orgResetSQL); err != nil {
		t.Fatalf("resetting the database: %v", err)
	}
	st, err := store.Open(context.Background(), "postgres", dsn)
	if err != nil {
		pgtest.Unavailable(t, err)
	}
	t.Cleanup(st.Close)
	return st
}

// orgWF is an INERT workflow: one `true` shell step, so a run that (wrongly,
// today) starts finishes at once and touches no model. The tenancy fixture
// runs no guard; the guard fixture uses orgPromptWF for what must be allowed.
func orgWF(name string, extra string) string {
	return "name: " + name + "\n" + extra + "steps:\n  - id: a\n    run: \"true\"\n"
}

// orgPromptWF is a workflow with no shell step and no tools.
func orgPromptWF(name string, extra string) string {
	return "name: " + name + "\n" + extra + "steps:\n  - id: a\n    prompt: hi\n    output_schema:\n      type: object\n"
}

// orgYAML writes one workflow file.
func orgYAML(t *testing.T, dir, file, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newOrgFixture: tenants acme and globex, each with one bound project, a
// workflow only it has, the shared short name `deploy`, a template, runs,
// state, a proposal, a bundle and a conversation; plus an unbound project.
//
// globex's project is listed FIRST in sources.json, so the bare name `deploy`
// belongs to globex — the name-collision trap of #163.
//
// cfgFile, when not empty, is a server config file loaded the way the server
// loads one (orgGuardConfig).
func newOrgFixture(t *testing.T, cfgFile string) *orgFixture {
	t.Helper()
	ctx := context.Background()
	st := orgStore(t)
	for name, perms := range DefaultRoles() {
		if err := st.UpsertRole(ctx, name, perms); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	f := &orgFixture{t: t, st: st, dirs: map[string]string{}}
	var srcs []workflow.Source
	for _, p := range []string{orgGlobexP, orgAcmeP, orgSoloP} {
		d := filepath.Join(root, p)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		f.dirs[p] = d
		srcs = append(srcs, workflow.Source{Name: p, Dir: d})
	}
	orgYAML(t, f.dirs[orgGlobexP], "deploy.yaml", orgWF(orgShared, "description: globex-deploy\n"))
	orgYAML(t, f.dirs[orgGlobexP], "globex-only.yaml", orgWF(orgGlobexW, "description: globex-secret-workflow\n"))
	orgYAML(t, f.dirs[orgGlobexP], "globex-starter.yaml", orgWF(orgGlobexT, "template: true\ndescription: globex-template\n"))
	orgYAML(t, f.dirs[orgAcmeP], "deploy.yaml", orgWF(orgShared, "description: acme-deploy\n"))
	orgYAML(t, f.dirs[orgAcmeP], "acme-only.yaml", orgWF(orgAcmeW, "description: acme-visible-workflow\n"))
	orgYAML(t, f.dirs[orgAcmeP], "acme-starter.yaml", orgPromptWF(orgAcmeT, "template: true\ndescription: acme-template\n"))
	// What the guard must still allow: no shell, no tools.
	orgYAML(t, f.dirs[orgAcmeP], "acme-plain.yaml", orgPromptWF("acme-plain", "on:\n  workflow_dispatch: {}\n  repository_dispatch:\n    types: [x]\n"))
	// The unbound project of a single-owner install.
	orgYAML(t, f.dirs[orgSoloP], "solo-wf.yaml", orgWF("solo-wf", "description: solo-visible-workflow\n"))
	// A workflow with a shell step and one with a file-write tool, ON DISK in
	// acme's project: the guard must stop them at run time, independently of
	// the save-time check (they were never saved through the API).
	dispatchable := "on:\n  workflow_dispatch: {}\n  repository_dispatch:\n    types: [x]\n"
	orgYAML(t, f.dirs[orgAcmeP], "acme-shell.yaml", "name: acme-shell\n"+dispatchable+"steps:\n  - id: sh\n    run: \"true\"\n")
	orgYAML(t, f.dirs[orgAcmeP], "acme-writer.yaml", "name: acme-writer\n"+dispatchable+"steps:\n  - id: w\n    prompt: write a file\n    tools: [write]\n    output_schema:\n      type: object\n")
	orgYAML(t, f.dirs[orgAcmeP], "acme-shell-starter.yaml", "name: acme-shell-starter\ntemplate: true\nsteps:\n  - id: sh\n    run: \"true\"\n")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(srcs)
	if err := os.WriteFile(filepath.Join(work, "sources.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	const addr = "127.0.0.1:8090" // loopback: see orgAs
	cfg := config.Config{}
	if cfgFile != "" {
		cfg = orgGuardConfig(t, cfgFile)
	}
	cfg.Addr, cfg.WorkDir, cfg.WorkflowsDir = addr, work, filepath.Join(root, "local")
	// Workflow memory lives beside the artifact directory (Engine.MemoryRoot);
	// keep both inside the temp dir.
	cfg.ArtifactDir = filepath.Join(root, "data", "artifacts")
	// The env store's key: generated on first use into the temp dir.
	cfg.SecretKeyEnv, cfg.SecretKeyPath = "WFX_ORGTEST_NO_SUCH_KEY", filepath.Join(root, "secret.key")
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	if err := eng.ReloadDefinitions(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.WaitIdle(30 * time.Second) })
	f.eng = eng
	f.h = New(eng, st, nil, addr, "", nil)
	bl, err := blob.OpenFolder(filepath.Join(root, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	f.bundles = orgBundleRouter(eng, st, bl)

	// Tenants and their projects.
	for tenant, project := range map[string]string{orgAcme: orgAcmeP, orgGlobex: orgGlobexP} {
		if _, err := st.CreateTenant(ctx, tenant, tenant); err != nil {
			t.Fatal(err)
		}
		if err := st.BindProject(ctx, tenant, project); err != nil {
			t.Fatal(err)
		}
	}

	// People.
	mk := func(name, role, tenant, orgRole string) *orgUser {
		u := &store.User{Name: name, Role: role}
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		return &orgUser{name: name, tenant: tenant, orgRole: orgRole, u: u}
	}
	f.alice = mk("alice", "", orgAcme, orgRoleAdmin)
	f.mallory = mk("mallory", "", orgAcme, orgRoleMember)
	f.bob = mk("bob", "", orgGlobex, orgRoleMember)
	f.carol = mk("carol", "", "", "")
	f.root = mk("root", RoleSuperadmin, "", "")
	// mallory: an admin of her own tenant's project, which makes her an
	// "admin anywhere" today (roles.go adminAnywhere) — plus member rows on
	// globex's items left over from before the projects were bound (a grant
	// must never outrank the tenant boundary).
	for _, g := range []struct{ scope, name, role string }{
		{store.MemberProject, orgAcmeP, store.ProjectAdmin},
		{store.MemberProject, orgGlobexP, store.ProjectAdmin},
		{store.MemberWorkflow, orgGlobexW, store.WorkflowAdmin},
		{store.MemberWorkflow, orgGlobexP + "/" + orgGlobexW, store.WorkflowAdmin},
		{store.MemberWorkflow, orgShared, store.WorkflowAdmin},
	} {
		if err := st.SetMember(ctx, g.scope, g.name, f.mallory.u.ID, g.role); err != nil {
			t.Fatal(err)
		}
	}

	// bob holds real member rows on globex's things, so removing him is a
	// change the witness can see.
	for _, g := range []struct{ scope, name, role string }{
		{store.MemberProject, orgGlobexP, store.ProjectAdmin},
		{store.MemberWorkflow, orgGlobexW, store.WorkflowRunner},
		{store.MemberWorkflow, orgGlobexP + "/" + orgGlobexW, store.WorkflowRunner},
	} {
		if err := st.SetMember(ctx, g.scope, g.name, f.bob.u.ID, g.role); err != nil {
			t.Fatal(err)
		}
	}

	// Workflow memory: under the default project and under globex's own.
	for _, p := range []string{"", orgGlobexP} {
		if err := eng.WriteMemory(ctx, orgGlobexW, p, "index", "globex-secret-memory"); err != nil {
			t.Fatal(err)
		}
	}

	// Runs: one in acme, three in globex (unequal on purpose, for metrics).
	run := func(project, wf string) string {
		r, err := st.CreateRun(ctx, project, wf, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = st.UpdateRun(ctx, r.ID, "succeeded", "", "")
		return r.ID.String()
	}
	f.runA = run(orgAcmeP, orgAcmeW)
	for i := 0; i < 3; i++ {
		f.runsG = append(f.runsG, run(orgGlobexP, orgGlobexW))
	}
	f.runG = f.runsG[0]

	// State.
	for _, s := range []struct{ scope, name, key, val string }{
		{"workflow", orgGlobexW, "gkey", "globex-secret-state"},
		{"project", orgGlobexP, "gpkey", "globex-secret-pstate"},
		{"workflow", orgAcmeW, "akey", "acme-visible-state"},
		{"project", orgAcmeP, "apkey", "acme-visible-pstate"},
	} {
		if err := st.PutState(ctx, s.scope, s.name, s.key, s.val); err != nil {
			t.Fatal(err)
		}
	}

	// Proposals.
	prop := func(project, wf string) string {
		p := &store.Proposal{Project: project, Workflow: wf, Kind: "edit", Branch: "wfx/" + wf, Base: "main", Note: project + "-proposal"}
		if err := st.CreateProposal(ctx, p); err != nil {
			t.Fatal(err)
		}
		return p.ID.String()
	}
	f.propA, f.propG = prop(orgAcmeP, orgAcmeW), prop(orgGlobexP, orgGlobexW)

	// Bundles (index rows; the routes are on f.bundles).
	bun := func(project, wf, fill string) string {
		d := "sha256:" + strings.Repeat(fill, 64)
		b := &store.PublishedBundle{Kind: "workflow", Project: project, Name: wf, Version: "1.0.0", Digest: d, Manifest: json.RawMessage(`{}`)}
		if err := st.CreateBundle(ctx, b); err != nil {
			t.Fatal(err)
		}
		return d
	}
	f.digA, f.digG = bun(orgAcmeP, orgAcmeW, "a"), bun(orgGlobexP, orgGlobexW, "b")

	// Conversations and a channel message that started a globex run.
	for _, c := range []model.Conversation{
		{Channel: "chat", Chat: "globex-chat", Workflow: orgGlobexW, Project: orgGlobexP, LastRunID: f.runG},
		{Channel: "chat", Chat: "acme-chat", Workflow: orgAcmeW, Project: orgAcmeP, LastRunID: f.runA},
	} {
		if err := st.TouchConversation(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.InsertChannelMessage(ctx, &store.ChannelMessage{Channel: "chat", MsgID: "g1", Sender: "x", Text: "globex-secret-message"}); err != nil {
		t.Fatal(err)
	}
	_ = st.SetChannelMessageOutcome(ctx, "chat", "g1", f.runG, "started")

	f.globexMarkers = append([]string{orgGlobexP, orgGlobexW, orgGlobexT, "globex-deploy", "globex-secret", "globex-chat", f.propG, f.digG}, f.runsG...)
	f.acmeMarkers = []string{orgAcmeP, orgAcmeW, f.runA, f.propA, f.digA, "acme-visible", "acme-chat"}
	return f
}

// orgBundleRouter mounts the six bundle routes. They exist only off loopback
// with a blob store (publishingEnabled), and off loopback the injected
// subject would not survive requireSubject; so here they are wired the same
// way New wires them — escaped-path routing, then authorize — with the
// subject put on by orgAs. TestOrgEveryBrowserRouteIsTenantChecked checks
// these patterns against the real router's.
func orgBundleRouter(eng *engine.Engine, st *store.Store, bl blob.Store) http.Handler {
	s := &Server{eng: eng, store: st, blob: bl, addr: "0.0.0.0:8090", loopbackOnly: false}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if rctx := chi.RouteContext(req.Context()); rctx != nil {
				rctx.RoutePath = req.URL.EscapedPath()
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Route("/api", func(r chi.Router) {
		r.Use(s.authorize)
		r.Post("/bundles", s.publishBundle)
		r.Get("/bundles", s.listBundles)
		r.Put("/bundles/tags/{tag}", s.setBundleTag)
		r.Get("/bundles/{digest}", s.getBundle)
		r.Get("/bundles/{digest}/tar", s.getBundleTar)
		r.Post("/bundles/{digest}/install", s.installBundle)
	})
	return r
}

// orgGuardConfig loads a server config file the way the server does (strict:
// an unknown key is an error). The file is expected to say `orgPolicy:
// guarded`; on code without that setting this fails, by design.
func orgGuardConfig(t *testing.T, body string) config.Config {
	t.Helper()
	// LoadWithFile derives defaults from the host; keep it off the host.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WFX_ROOT", home)
	p := filepath.Join(t.TempDir(), "wfx.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithFile(p)
	if err != nil {
		t.Fatalf("the server config %q does not load (a top-level orgPolicy key is part of #189): %v", strings.TrimSpace(body), err)
	}
	return cfg
}

type orgRes struct {
	code int
	body string
}

// call sends one request as who. The request carries a deadline so an event
// stream (SSE) that is wrongly served does not hang the test: it ends with
// whatever it had written.
func (f *orgFixture) call(h http.Handler, who *orgUser, method, path string, body any) orgRes {
	var rdr io.Reader = http.NoBody
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(req.Context(), 750*time.Millisecond)
	defer cancel()
	req = orgAs(req.WithContext(ctx), who)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return orgRes{w.Code, w.Body.String()}
}

func (f *orgFixture) do(who *orgUser, method, path string, body any) orgRes {
	return f.call(f.h, who, method, path, body)
}

// ---------------------------------------------------------------------------
// The table
// ---------------------------------------------------------------------------

type orgKind int

const (
	// deny404: the request addresses another tenant's thing and must answer
	// 404, with the same body as for a thing that never existed, and change
	// nothing that tenant can see.
	deny404 orgKind = iota
	// listExcludes: a listing; it must contain none of globex's markers (and,
	// where acme has items of that kind, at least one of acme's).
	listExcludes
	// createBound: a create; what is created belongs to the caller's tenant.
	createBound
)

type orgCase struct {
	name   string
	kind   orgKind
	method string
	path   string // with {GP} {GW} {GWQ} {GR} {GPROP} {GDIG} {GT} placeholders
	body   any    // strings inside may use the same placeholders
	// bundles: served by orgBundleRouter.
	bundles bool
	// noProbe: the superadmin cannot be expected to get a non-404 on this
	// URL either (e.g. an artifact id that does not exist), so the URL is not
	// probed for reality first.
	noProbe bool
	// acmeToo: for listExcludes, acme has items of this kind and alice must
	// see at least one of them.
	acmeToo bool
}

func (f *orgFixture) placeholders(ghost bool) *strings.Replacer {
	if ghost {
		return strings.NewReplacer(
			"{GWQ}", "ghost-app%2Fghost-only", "{GP}", "ghost-app", "{GW}", "ghost-only", "{GT}", "ghost-starter",
			"{GR}", "2f1c9a3e-0000-4000-8000-000000000000", "{GPROP}", "2f1c9a3e-0000-4000-8000-000000000001",
			"{GDIG}", "sha256:"+strings.Repeat("c", 64))
	}
	return strings.NewReplacer(
		"{GWQ}", orgGlobexP+"%2F"+orgGlobexW, "{GP}", orgGlobexP, "{GW}", orgGlobexW, "{GT}", orgGlobexT,
		"{GR}", f.runG, "{GPROP}", f.propG, "{GDIG}", f.digG)
}

func fillBody(body any, rep *strings.Replacer) any {
	if body == nil {
		return nil
	}
	raw, _ := json.Marshal(body)
	var out any
	_ = json.Unmarshal([]byte(rep.Replace(string(raw))), &out)
	return out
}

func orgDef(name string) map[string]any {
	return map[string]any{"definition": map[string]any{"name": name, "steps": []any{
		map[string]any{"id": "a", "prompt": "hi", "output_schema": map[string]any{"type": "object"}}}}}
}

// orgCases is every tenant-relevant browser route, addressed at globex's
// things. Each workflow route is tried by bare name and by the qualified
// `project/name` (encoded slash), since both reach the same definition.
func orgCases() []orgCase {
	var cs []orgCase
	add := func(c orgCase) { cs = append(cs, c) }
	d := func(name, method, path string, body any) orgCase {
		return orgCase{name: name, kind: deny404, method: method, path: path, body: body}
	}
	l := func(name, path string, acmeToo bool) orgCase {
		return orgCase{name: name, kind: listExcludes, method: "GET", path: path, acmeToo: acmeToo}
	}

	// ---- projects ----
	add(l("list projects", "/api/projects", true))
	add(orgCase{name: "create project", kind: createBound, method: "POST", path: "/api/projects", body: map[string]any{"name": "fresh-app", "create": true}})
	add(d("get project", "GET", "/api/projects/{GP}", nil))
	add(d("patch project", "PATCH", "/api/projects/{GP}", map[string]any{"category": "security"}))
	add(d("delete project", "DELETE", "/api/projects/{GP}", nil))
	add(d("project env read", "GET", "/api/projects/{GP}/env", nil))
	add(d("project env write", "PUT", "/api/projects/{GP}/env", map[string]any{"key": "K", "value": "v"}))
	add(d("project env delete", "DELETE", "/api/projects/{GP}/env/K", nil))
	add(d("project events read", "GET", "/api/projects/{GP}/events", nil))
	add(d("project events write", "PUT", "/api/projects/{GP}/events", map[string]any{"url": "https://hooks.example.test/x"}))
	add(d("project events delete", "DELETE", "/api/projects/{GP}/events", nil))
	add(d("project members read", "GET", "/api/projects/{GP}/members", nil))
	add(d("project members self-grant", "PUT", "/api/projects/{GP}/members/mallory", map[string]any{"role": "project_admin"}))
	add(d("project members remove owner", "DELETE", "/api/projects/{GP}/members/bob", nil))
	add(d("project expose read", "GET", "/api/projects/{GP}/expose", nil))
	add(d("project expose issue", "POST", "/api/projects/{GP}/expose/mcp", map[string]any{}))
	add(d("project expose revoke", "DELETE", "/api/projects/{GP}/expose/mcp", nil))
	add(l("sources list every project's contents", "/api/sources", true))

	// ---- workflows ----
	add(l("list workflows", "/api/workflows", true))
	add(orgCase{name: "reload returns every definition", kind: listExcludes, method: "POST", path: "/api/workflows/reload", acmeToo: true})
	for _, w := range []struct{ tag, p string }{{"bare", "{GW}"}, {"qualified", "{GWQ}"}} {
		pre := "/api/workflows/" + w.p
		add(d("get workflow "+w.tag, "GET", pre, nil))
		add(d("delete workflow "+w.tag, "DELETE", pre, nil))
		add(d("workflow state "+w.tag, "GET", pre+"/state", nil))
		add(d("workflow schedule "+w.tag, "GET", pre+"/schedule", nil))
		add(d("workflow memory "+w.tag, "GET", pre+"/memory", nil))
		add(d("workflow memory forget "+w.tag, "DELETE", pre+"/memory", nil))
		add(d("workflow memory file "+w.tag, "GET", pre+"/memory/file?target=index", nil))
		add(d("workflow memory file ?project=globex "+w.tag, "GET", pre+"/memory/file?target=index&project={GP}", nil))
		add(d("workflow memory file write "+w.tag, "PUT", pre+"/memory/file?target=index", map[string]any{"body": "x"}))
		add(d("workflow memory file delete "+w.tag, "DELETE", pre+"/memory/file?target=index", nil))
		add(d("workflow memory dream "+w.tag, "POST", pre+"/memory/dream", map[string]any{}))
		add(d("workflow members "+w.tag, "GET", pre+"/members", nil))
		add(d("workflow members self-grant "+w.tag, "PUT", pre+"/members/alice", map[string]any{"role": "workflow_admin"}))
		add(d("workflow members remove "+w.tag, "DELETE", pre+"/members/bob", nil))
		add(d("workflow expose "+w.tag, "GET", pre+"/expose", nil))
		add(d("workflow expose issue "+w.tag, "POST", pre+"/expose/hook", map[string]any{}))
		add(d("workflow expose revoke "+w.tag, "DELETE", pre+"/expose/hook", nil))
		add(d("run workflow "+w.tag, "POST", pre+"/runs", map[string]any{}))
		add(d("dispatch workflow "+w.tag, "POST", pre+"/dispatches", map[string]any{"event_type": "x"}))
		add(d("dry-run workflow "+w.tag, "POST", pre+"/dryrun", map[string]any{}))
		add(d("copy workflow out "+w.tag, "POST", pre+"/copy", map[string]any{"as": "stolen", "project": orgAcmeP}))
	}
	add(d("save workflow bare", "PUT", "/api/workflows/{GW}", orgDef(orgGlobexW)))
	add(d("save workflow qualified", "PUT", "/api/workflows/{GWQ}", orgDef(orgGlobexP+"/"+orgGlobexW)))
	// ?project= decides the directory a save writes into.
	add(d("save NEW workflow into globex via ?project=", "PUT", "/api/workflows/planted?project={GP}", orgDef("planted")))
	// Double-encoded slash: must not decode its way to globex's workflow.
	add(orgCase{name: "get workflow double-encoded", kind: deny404, method: "GET", path: "/api/workflows/{GP}%252F{GW}", noProbe: true})

	// ---- templates ----
	add(l("list templates", "/api/templates", true))
	add(d("get template", "GET", "/api/templates/{GT}", nil))
	add(d("copy template out", "POST", "/api/templates/{GT}/copy", map[string]any{"as": "stolen-tpl", "project": orgAcmeP}))

	// ---- state (the operator's door names the scope outright) ----
	add(l("state of globex workflow", "/api/state?scope=workflow&name={GW}", false))
	add(l("state of globex project", "/api/state?scope=project&name={GP}", false))
	add(d("set state of globex workflow", "PUT", "/api/state", map[string]any{"scope": "workflow", "name": "{GW}", "key": "gkey", "value": "pwned"}))
	add(d("delete state of globex workflow", "DELETE", "/api/state/gkey?scope=workflow&name={GW}", nil))

	// ---- runs ----
	add(l("list runs", "/api/runs", true))
	add(l("list runs ?project=globex", "/api/runs?project={GP}", false))
	add(l("list runs ?workflow=globex", "/api/runs?workflow={GW}", false))
	add(d("get run", "GET", "/api/runs/{GR}", nil))
	add(d("run events stream", "GET", "/api/runs/{GR}/events", nil))
	for _, a := range []string{"approve", "reject", "input", "answer", "retry", "cancel"} {
		add(d("run "+a, "POST", "/api/runs/{GR}/"+a, map[string]any{}))
	}
	add(orgCase{name: "run artifact", kind: deny404, method: "GET", path: "/api/runs/{GR}/artifacts/" + uuid.NewString(), noProbe: true})
	add(d("run state read", "GET", "/api/runs/{GR}/state?scope=workflow", nil))
	add(d("run state write", "POST", "/api/runs/{GR}/state", map[string]any{"scope": "workflow", "key": "gkey", "value": "pwned"}))
	add(d("run state delete", "DELETE", "/api/runs/{GR}/state/gkey?scope=workflow", nil))

	// ---- proposals ----
	add(l("list proposals", "/api/proposals", true))
	add(l("list proposals ?project=globex", "/api/proposals?project={GP}", false))
	add(l("drift ?project=globex", "/api/proposals/drift?project={GP}", false))
	add(d("propose drift in globex", "POST", "/api/proposals/drift", map[string]any{"project": "{GP}", "workflow": "{GW}"}))
	add(d("get proposal", "GET", "/api/proposals/{GPROP}", nil))
	add(d("approve proposal", "POST", "/api/proposals/{GPROP}/approve", map[string]any{}))
	add(d("reject proposal", "POST", "/api/proposals/{GPROP}/reject", map[string]any{"reason": "x"}))

	// ---- conversations, channel messages, metrics ----
	add(l("list conversations", "/api/conversations", true))
	add(d("move a globex conversation", "POST", "/api/conversations/state", map[string]any{"channel": "chat", "chat": "globex-chat", "state": "closed"}))
	add(l("channel messages", "/api/channels/messages", false))
	add(l("channel messages ?run=globex", "/api/channels/messages?run={GR}", false))
	add(l("metrics", "/api/metrics", true))

	// ---- bundles ----
	add(orgCase{name: "list bundles", kind: listExcludes, method: "GET", path: "/api/bundles", bundles: true, acmeToo: true})
	add(orgCase{name: "list bundles ?project=globex", kind: listExcludes, method: "GET", path: "/api/bundles?project={GP}", bundles: true})
	add(orgCase{name: "get bundle", kind: deny404, method: "GET", path: "/api/bundles/{GDIG}", bundles: true})
	add(orgCase{name: "bundle tar", kind: deny404, method: "GET", path: "/api/bundles/{GDIG}/tar", bundles: true, noProbe: true})
	add(orgCase{name: "install bundle", kind: deny404, method: "POST", path: "/api/bundles/{GDIG}/install", body: map[string]any{}, bundles: true})
	add(orgCase{name: "tag bundle", kind: deny404, method: "PUT", path: "/api/bundles/tags/latest", bundles: true,
		body: map[string]any{"project": "{GP}", "kind": "workflow", "name": "{GW}", "version": "1.0.0"}})
	add(orgCase{name: "publish into globex project", kind: deny404, method: "POST", path: "/api/bundles", bundles: true,
		body: map[string]any{"project": "{GP}", "kind": "workflow", "name": "{GW}", "version": "9.9.9", "tarGz": ""}})
	return cs
}

// witness is what globex's world looks like to the superadmin: a mutating
// request from another tenant must leave every line of it unchanged.
func (f *orgFixture) witness() map[string]string {
	out := map[string]string{}
	for _, p := range []string{
		"/api/projects/" + orgGlobexP,
		"/api/projects/" + orgGlobexP + "/env",
		"/api/projects/" + orgGlobexP + "/events",
		"/api/projects/" + orgGlobexP + "/members",
		"/api/projects/" + orgGlobexP + "/expose",
		"/api/workflows/" + orgGlobexW,
		"/api/workflows/" + orgGlobexW + "/state",
		"/api/workflows/" + orgGlobexW + "/members",
		"/api/workflows/" + orgGlobexW + "/expose",
		"/api/workflows/" + orgGlobexW + "/memory",
		"/api/workflows/" + orgGlobexW + "/memory/file?target=index",
		"/api/workflows/" + orgGlobexW + "/memory/file?target=index&project=" + orgGlobexP,
		"/api/runs?project=" + orgGlobexP,
		"/api/runs/" + f.runG,
		"/api/proposals?project=" + orgGlobexP,
		"/api/state?scope=project&name=" + orgGlobexP,
		"/api/conversations",
	} {
		r := f.do(f.root, "GET", p, nil)
		out[p] = fmt.Sprintf("%d %s", r.code, r.body)
	}
	if bs, err := f.st.ListBundles(context.Background(), store.BundleFilter{Project: orgGlobexP}); err == nil {
		raw, _ := json.Marshal(bs)
		out["bundles"] = string(raw)
	}
	var files []string
	_ = filepath.Walk(f.dirs[orgGlobexP], func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			raw, _ := os.ReadFile(p)
			files = append(files, p+"="+string(raw))
		}
		return nil
	})
	sort.Strings(files)
	out["disk"] = strings.Join(files, "\n")
	tp, _ := f.st.TenantProjects(context.Background(), orgGlobex)
	out["tenant projects"] = strings.Join(tp, ",")
	return out
}

func (f *orgFixture) handlerFor(c orgCase) http.Handler {
	if c.bundles {
		return f.bundles
	}
	return f.h
}

func (f *orgFixture) leaks(body string) []string {
	var hits []string
	for _, m := range f.globexMarkers {
		if strings.Contains(body, m) {
			hits = append(hits, m)
		}
	}
	return hits
}

func (f *orgFixture) seesAcme(body string) bool {
	for _, m := range f.acmeMarkers {
		if strings.Contains(body, m) {
			return true
		}
	}
	return false
}

// runOrgCase checks one case for one attacker on fixture f.
func runOrgCase(t *testing.T, f *orgFixture, c orgCase, attacker func(*orgFixture) *orgUser) {
	who := attacker(f)
	real, ghost := f.placeholders(false), f.placeholders(true)
	h := f.handlerFor(c)
	path, body := real.Replace(c.path), fillBody(c.body, real)

	switch c.kind {
	case listExcludes:
		res := f.call(h, who, c.method, path, body)
		if hits := f.leaks(res.body); len(hits) > 0 {
			t.Errorf("%s %s as %s (%s) answered %d showing another tenant's items %v", c.method, path, who.name, who.tenant, res.code, hits)
		}
		if c.acmeToo && (res.code != 200 || !f.seesAcme(res.body)) {
			t.Errorf("%s %s as %s: want 200 listing her own tenant's items, got %d %.300s", c.method, path, who.name, res.code, res.body)
		}

	case createBound:
		before := f.tenantProjects(t, who.tenant)
		res := f.call(h, who, c.method, path, body)
		if res.code != 201 {
			t.Fatalf("%s %s as %s (%s %s): %d %.300s", c.method, path, who.name, who.tenant, who.orgRole, res.code, res.body)
		}
		if got := f.newProjects(t, who.tenant, before); len(got) != 1 {
			t.Errorf("after the create, tenant %q gained %v; want exactly one new project", who.tenant, got)
		}

	case deny404:
		if c.method == "GET" && !c.noProbe {
			// The URL is real: the superadmin reaches it.
			if p := f.call(h, f.root, c.method, path, body); p.code == 404 {
				t.Fatalf("fixture: the superadmin gets 404 on %s %s, so the case proves nothing: %s", c.method, path, p.body)
			}
		}
		before := f.witness()
		res := f.call(h, who, c.method, path, body)
		if res.code != http.StatusNotFound {
			t.Errorf("%s %s as %s (%s): got %d, want 404: %.300s", c.method, path, who.name, who.tenant, res.code, res.body)
		}
		if hits := f.leaks(res.body); len(hits) > 0 {
			t.Errorf("%s %s as %s: the answer names another tenant's items %v", c.method, path, who.name, hits)
		}
		if after := f.witness(); !reflect.DeepEqual(before, after) {
			for k := range before {
				if before[k] != after[k] {
					t.Errorf("%s %s as %s changed globex's %s:\n before: %.300s\n after:  %.300s", c.method, path, who.name, k, before[k], after[k])
				}
			}
		}
		// Indistinguishable from never-existed.
		g := f.call(h, who, c.method, ghost.Replace(c.path), fillBody(c.body, ghost))
		if res.code == 404 && g.code == 404 && strings.TrimSpace(g.body) != strings.TrimSpace(res.body) {
			t.Errorf("%s %s: another tenant's 404 differs from a never-existed 404:\n other tenant: %s\n never existed: %s", c.method, path, res.body, g.body)
		}
	}
}

// newProjects returns what is bound to tenant now and was not in before.
func (f *orgFixture) newProjects(t *testing.T, tenant string, before []string) []string {
	t.Helper()
	now, err := f.st.TenantProjects(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	old := map[string]bool{}
	for _, p := range before {
		old[p] = true
	}
	var out []string
	for _, p := range now {
		if !old[p] {
			out = append(out, p)
		}
	}
	return out
}

func (f *orgFixture) tenantProjects(t *testing.T, tenant string) []string {
	t.Helper()
	ps, err := f.st.TenantProjects(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

// ---------------------------------------------------------------------------
// The tests
// ---------------------------------------------------------------------------

// Tenant A cannot list, read, run, dry-run, edit or delete anything of
// tenant B: 404, every route group. Two attackers from acme: alice (org-admin,
// no member rows) and mallory (a member who is admin of an acme project — an
// "admin anywhere" — and holds leftover grants on globex's items).
func TestOrgOtherTenantIsNotFound(t *testing.T) {
	attackers := map[string]func(*orgFixture) *orgUser{
		"alice":   func(f *orgFixture) *orgUser { return f.alice },
		"mallory": func(f *orgFixture) *orgUser { return f.mallory },
	}
	// Reads share one fixture and run first; anything that may write gets a
	// fresh fixture (each resets the database), because today some of these
	// requests really do change globex's data.
	shared := newOrgFixture(t, "")
	var writes []orgCase
	for _, c := range orgCases() {
		if c.kind == createBound {
			continue // TestOrgMembersCreateProjectsInTheirTenant
		}
		if c.method != "GET" {
			writes = append(writes, c)
			continue
		}
		for _, an := range []string{"alice", "mallory"} {
			c, an := c, an
			t.Run(an+"/"+c.name, func(t *testing.T) { runOrgCase(t, shared, c, attackers[an]) })
		}
	}
	for _, c := range writes {
		for _, an := range []string{"alice", "mallory"} {
			c, an := c, an
			t.Run(an+"/"+c.name, func(t *testing.T) { runOrgCase(t, newOrgFixture(t, ""), c, attackers[an]) })
		}
	}
}

// The bare short name `deploy` belongs to globex (its source loaded first).
// alice asking for `deploy` must get acme's, or 404 — never globex's.
func TestOrgSharedShortNameResolvesInsideTheTenant(t *testing.T) {
	f := newOrgFixture(t, "")
	res := f.do(f.alice, "GET", "/api/workflows/"+orgShared, nil)
	if strings.Contains(res.body, "globex-deploy") || strings.Contains(res.body, orgGlobexP) {
		t.Errorf("alice asked for %q and was served globex's workflow: %d %.300s", orgShared, res.code, res.body)
	}
	// Running it must not start globex's workflow either.
	before := runsOfProject(t, f.st, orgGlobexP)
	f.do(f.alice, "POST", "/api/workflows/"+orgShared+"/runs", map[string]any{})
	f.do(f.mallory, "POST", "/api/workflows/"+orgShared+"/runs", map[string]any{})
	if after := runsOfProject(t, f.st, orgGlobexP); after != before {
		t.Errorf("a run of %q by an acme user started in globex's project (%d -> %d runs)", orgShared, before, after)
	}
	// And bob, who owns it, still reaches his own by the bare name.
	if r := f.do(f.bob, "GET", "/api/workflows/"+orgShared, nil); r.code != 200 || !strings.Contains(r.body, "globex-deploy") {
		t.Errorf("bob lost his own %q: %d %.300s", orgShared, r.code, r.body)
	}
}

func runsOfProject(t *testing.T, st *store.Store, project string) int {
	t.Helper()
	rs, err := st.FindRuns(context.Background(), store.RunFilter{Project: project})
	if err != nil {
		t.Fatal(err)
	}
	return len(rs)
}

// Metrics count only the caller's tenant: acme has 1 run, globex 3.
func TestOrgMetricsCountOnlyTheTenant(t *testing.T) {
	f := newOrgFixture(t, "")
	res := f.do(f.alice, "GET", "/api/metrics", nil)
	var m store.Metrics
	if err := json.Unmarshal([]byte(res.body), &m); err != nil {
		t.Fatalf("%d %s", res.code, res.body)
	}
	if m.Runs.Total != 1 {
		t.Errorf("alice's metrics count %d runs; acme has 1 (globex's 3 must not be counted)", m.Runs.Total)
	}
}

// Positive controls: alice sees and acts on her own tenant's things according
// to her org role, with no member rows at all.
func TestOrgAdminActsOnHerOwnTenant(t *testing.T) {
	orgAdminActsOnHerOwnTenant(t, newOrgFixture(t, ""))
}

func orgAdminActsOnHerOwnTenant(t *testing.T, f *orgFixture) {
	for _, c := range []struct {
		method, path string
		body         any
		want         int
	}{
		{"GET", "/api/projects/" + orgAcmeP, nil, 200},
		{"GET", "/api/workflows/" + orgAcmeW, nil, 200},
		{"GET", "/api/workflows/" + orgAcmeP + "%2F" + orgAcmeW, nil, 200},
		{"GET", "/api/runs/" + f.runA, nil, 200},
		{"GET", "/api/proposals/" + f.propA, nil, 200},
		{"GET", "/api/projects/" + orgAcmeP + "/env", nil, 200},
		{"PATCH", "/api/projects/" + orgAcmeP, map[string]any{"category": "security"}, 200},
		{"PUT", "/api/workflows/fresh-wf?project=" + orgAcmeP, orgDef("fresh-wf"), 200},
		{"POST", "/api/workflows/" + orgAcmeW + "/dryrun", map[string]any{}, 200},
	} {
		if r := f.do(f.alice, c.method, c.path, c.body); r.code != c.want {
			t.Errorf("alice (acme org-admin) %s %s: got %d, want %d: %.300s", c.method, c.path, r.code, c.want, r.body)
		}
	}
	if r := f.do(f.alice, "POST", "/api/workflows/"+orgAcmeW+"/runs", map[string]any{}); r.code/100 != 2 {
		t.Errorf("alice (acme org-admin) could not run her tenant's workflow: %d %.300s", r.code, r.body)
	}
	if r := f.call(f.bundles, f.alice, "GET", "/api/bundles/"+f.digA, nil); r.code != 200 {
		t.Errorf("alice could not read her tenant's bundle: %d %.300s", r.code, r.body)
	}
	// Her listings hold her tenant's runs (an org-admin sees the tenant's
	// runs, not only the ones she started).
	if r := f.do(f.alice, "GET", "/api/runs", nil); !strings.Contains(r.body, f.runA) {
		t.Errorf("alice's run list lacks her tenant's run: %d %.300s", r.code, r.body)
	}
}

// An org member may create a project; it is bound to her tenant. Two tenants
// may each create a project with the same short name, and each sees it short.
func TestOrgMembersCreateProjectsInTheirTenant(t *testing.T) {
	f := newOrgFixture(t, "")
	for _, c := range orgCases() {
		if c.kind != createBound {
			continue
		}
		runOrgCase(t, f, c, func(f *orgFixture) *orgUser { return f.bob })
	}

	// The same short name in two tenants.
	beforeA, beforeG := f.tenantProjects(t, orgAcme), f.tenantProjects(t, orgGlobex)
	ra := f.do(f.alice, "POST", "/api/projects", map[string]any{"name": "shared", "create": true})
	rb := f.do(f.bob, "POST", "/api/projects", map[string]any{"name": "shared", "create": true})
	if ra.code != 201 || rb.code != 201 {
		t.Fatalf("both tenants creating %q: acme %d %.200s / globex %d %.200s", "shared", ra.code, ra.body, rb.code, rb.body)
	}
	na, ng := f.newProjects(t, orgAcme, beforeA), f.newProjects(t, orgGlobex, beforeG)
	if len(na) != 1 || len(ng) != 1 || na[0] == ng[0] {
		t.Fatalf("each tenant should gain one project with its own stored name: acme %v, globex %v", na, ng)
	}
	shortNamed := func(who *orgUser) []map[string]any {
		var ps []map[string]any
		_ = json.Unmarshal([]byte(f.do(who, "GET", "/api/projects", nil).body), &ps)
		var out []map[string]any
		for _, p := range ps {
			if p["name"] == "shared" {
				out = append(out, p)
			}
		}
		return out
	}
	a, b := shortNamed(f.alice), shortNamed(f.bob)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("each tenant should list exactly one project shown as %q: acme %d, globex %d", "shared", len(a), len(b))
	}
	if a[0]["dir"] == b[0]["dir"] {
		t.Errorf("the two tenants' %q projects are the same directory %v", "shared", a[0]["dir"])
	}
	ga := f.do(f.alice, "GET", "/api/projects/shared", nil)
	gb := f.do(f.bob, "GET", "/api/projects/shared", nil)
	if ga.code != 200 || gb.code != 200 || ga.body == gb.body {
		t.Errorf("GET /api/projects/shared should resolve inside each tenant: acme %d / globex %d (same body: %v)", ga.code, gb.code, ga.body == gb.body)
	}
}

// Old behaviour: a user bound to no tenant sees what she sees today. Only
// things bound to no tenant are asserted on (the single-owner install): what a
// tenantless, non-superadmin user should see of an organization's projects in
// an install that HAS organizations is an open question #189 does not settle.
// These pass on main and must keep passing.
func TestOrgNoTenantSeesWhatItSeesToday(t *testing.T) {
	f := newOrgFixture(t, "")
	if r := f.do(f.carol, "GET", "/api/projects", nil); !strings.Contains(r.body, `"`+orgSoloP+`"`) {
		t.Errorf("carol (no tenant) no longer lists the unbound project: %d %.300s", r.code, r.body)
	}
	if r := f.do(f.carol, "GET", "/api/projects/"+orgSoloP, nil); r.code != 200 {
		t.Errorf("carol can no longer read the unbound project: %d", r.code)
	}
	if r := f.do(f.carol, "GET", "/api/workflows", nil); !strings.Contains(r.body, "solo-visible-workflow") {
		t.Errorf("carol no longer lists the unbound project's workflow: %d %.300s", r.code, r.body)
	}
	if r := f.do(f.carol, "GET", "/api/workflows/solo-wf", nil); r.code != 200 {
		t.Errorf("carol can no longer read a workflow: %d", r.code)
	}
	if r := f.do(f.carol, "GET", "/api/workflows/"+orgSoloP+"%2Fsolo-wf", nil); r.code != 200 {
		t.Errorf("carol can no longer read a workflow by its qualified name: %d", r.code)
	}
	// Writes still need a role, exactly as today.
	if r := f.do(f.carol, "POST", "/api/workflows/solo-wf/runs", map[string]any{}); r.code != 403 {
		t.Errorf("carol (no role) running a workflow: got %d, want 403 as today", r.code)
	}
	// The superadmin (no tenant) still reaches everything.
	for _, p := range []string{"/api/runs/" + f.runG, "/api/runs/" + f.runA, "/api/projects/" + orgGlobexP} {
		if r := f.do(f.root, "GET", p, nil); r.code != 200 {
			t.Errorf("the superadmin can no longer read %s: %d", p, r.code)
		}
	}
}

// ---------------------------------------------------------------------------
// The authoring guard (orgPolicy: guarded)
// ---------------------------------------------------------------------------

func orgShellDef(name string) map[string]any {
	return map[string]any{"definition": map[string]any{"name": name, "steps": []any{
		map[string]any{"id": "sh", "run": "true"}}}}
}

func orgWriterDef(name string) map[string]any {
	return map[string]any{"definition": map[string]any{"name": name, "steps": []any{
		map[string]any{"id": "w", "prompt": "write a file", "tools": []any{"write"}, "output_schema": map[string]any{"type": "object"}}}}}
}

func orgCronShellDef(name string) map[string]any {
	d := orgShellDef(name)
	d["definition"].(map[string]any)["on"] = map[string]any{"schedule": []any{map[string]any{"cron": "0 3 * * *"}}}
	return d
}

const orgGuarded = "orgPolicy: guarded\n"

// orgGuardReq is one request the guard must refuse to an org member. Every
// one of them SUCCEEDS for the superadmin without the policy
// (TestOrgGuardTargetsWorkWithoutThePolicy), so a refusal under the policy is
// the guard's doing and not some unrelated failure.
type orgGuardReq struct {
	what, method, path string
	body               any
	// created: a workflow name that must not exist after a refusal.
	created string
	// runsOf: a workflow whose run count must not change after a refusal.
	runsOf string
	// validate: a 2xx answer counts as refused when it says valid:false.
	validate bool
}

// orgGuardReqs is every way an org member could get shell or a file-write
// tool onto a shared runner through the API: save (plain, and with a
// schedule), run, dispatch and dry-run what is on disk, dry-run or validate a
// draft, and copy a template or workflow that has one. (Schedules and
// message triggers fire from the scheduler, which no API request reaches; a
// save carrying them is refused here like any other.)
func orgGuardReqs() []orgGuardReq {
	p := "?project=" + orgAcmeP
	return []orgGuardReq{
		{what: "save a run: step", method: "PUT", path: "/api/workflows/sneaky-shell" + p, body: orgShellDef("sneaky-shell"), created: "sneaky-shell"},
		{what: "save a write tool", method: "PUT", path: "/api/workflows/sneaky-writer" + p, body: orgWriterDef("sneaky-writer"), created: "sneaky-writer"},
		{what: "save a scheduled run: step", method: "PUT", path: "/api/workflows/sneaky-cron" + p, body: orgCronShellDef("sneaky-cron"), created: "sneaky-cron"},
		{what: "run a shell workflow", method: "POST", path: "/api/workflows/acme-shell/runs", body: map[string]any{}, runsOf: "acme-shell"},
		{what: "run a writer workflow", method: "POST", path: "/api/workflows/acme-writer/runs", body: map[string]any{}, runsOf: "acme-writer"},
		{what: "dispatch a shell workflow", method: "POST", path: "/api/workflows/acme-shell/dispatches", body: map[string]any{"event_type": "x"}, runsOf: "acme-shell"},
		{what: "dispatch a writer workflow", method: "POST", path: "/api/workflows/acme-writer/dispatches", body: map[string]any{"event_type": "x"}, runsOf: "acme-writer"},
		{what: "dry-run a shell workflow", method: "POST", path: "/api/workflows/acme-shell/dryrun", body: map[string]any{}},
		{what: "dry-run a writer workflow", method: "POST", path: "/api/workflows/acme-writer/dryrun", body: map[string]any{}},
		{what: "dry-run a shell draft", method: "POST", path: "/api/dryrun", body: orgShellDef("draft-shell")},
		{what: "validate a shell draft", method: "POST", path: "/api/workflows/validate", body: orgShellDef("draft-shell"), validate: true},
		{what: "copy a shell template", method: "POST", path: "/api/templates/acme-shell-starter/copy", body: map[string]any{"as": "copied-shell", "project": orgAcmeP}, created: "copied-shell"},
		{what: "copy a shell workflow", method: "POST", path: "/api/workflows/acme-shell/copy", body: map[string]any{"as": "copied-shell2", "project": orgAcmeP}, created: "copied-shell2"},
	}
}

func orgAccepted(q orgGuardReq, r orgRes) bool {
	if r.code/100 != 2 {
		return false
	}
	if q.validate {
		return strings.Contains(r.body, `"valid":true`)
	}
	// A dry run that reports the workflow could not work is not acceptance.
	return !strings.Contains(r.body, `"ok":false`)
}

// With orgPolicy guarded, an org member's workflow may not run shell or write
// files on a shared runner: refused on save AND on every path that starts or
// simulates a run.
func TestOrgGuardedPolicyRefusesShellAndFileWrites(t *testing.T) {
	f := newOrgFixture(t, orgGuarded)
	for _, who := range []*orgUser{f.alice, f.mallory} {
		for _, q := range orgGuardReqs() {
			before := 0
			if q.runsOf != "" {
				before = runsOf(t, f.st, q.runsOf)
			}
			r := f.do(who, q.method, q.path, q.body)
			if orgAccepted(q, r) {
				t.Errorf("%s (%s %s) could %s: %d %.300s", who.name, who.tenant, who.orgRole, q.what, r.code, r.body)
			}
			if q.created != "" && f.eng.Definitions()[q.created] != nil {
				t.Errorf("%s: %s exists after a refused %q", who.name, q.created, q.what)
			}
			if q.runsOf != "" {
				if after := runsOf(t, f.st, q.runsOf); after != before {
					t.Errorf("%s: %q created a run of %s (%d -> %d)", who.name, q.what, q.runsOf, before, after)
				}
			}
		}
	}
	// What the guard must still allow the org-admin: no shell, no tools.
	for _, c := range []struct {
		what, method, path string
		body               any
	}{
		{"save a plain prompt workflow", "PUT", "/api/workflows/plain-ok?project=" + orgAcmeP, orgDef("plain-ok")},
		{"dry-run a plain workflow", "POST", "/api/workflows/acme-plain/dryrun", map[string]any{}},
		{"copy a plain template", "POST", "/api/templates/" + orgAcmeT + "/copy", map[string]any{"as": "plain-copy", "project": orgAcmeP}},
		{"run a plain workflow", "POST", "/api/workflows/acme-plain/runs", map[string]any{}},
	} {
		if r := f.do(f.alice, c.method, c.path, c.body); r.code/100 != 2 {
			t.Errorf("alice (acme org-admin) under the guard could not %s: %d %.300s", c.what, r.code, r.body)
		}
	}
	// A tenantless admin is not an org member: the guard does not apply.
	f.orgSoloAdminSavesShell(t)
}

// The control for the guard test: without the policy, the superadmin can do
// every one of those things today. Passes on main.
func TestOrgGuardTargetsWorkWithoutThePolicy(t *testing.T) {
	f := newOrgFixture(t, "")
	for _, q := range orgGuardReqs() {
		if r := f.do(f.root, q.method, q.path, q.body); !orgAccepted(q, r) {
			t.Errorf("the superadmin, no policy, could not %s (%s %s): %d %.300s", q.what, q.method, q.path, r.code, r.body)
		}
	}
}

// orgSoloAdminSavesShell: carol, bound to no tenant and admin of the unbound
// project, saves a run: step, as today.
func (f *orgFixture) orgSoloAdminSavesShell(t *testing.T) {
	t.Helper()
	// Today a save with ?project= is authorized against the workflow name's
	// own project, which for a new name is "local" (roles.go scopedAllows),
	// so carol administers both.
	for _, p := range []string{orgSoloP, "local"} {
		if err := f.st.SetMember(context.Background(), store.MemberProject, p, f.carol.u.ID, store.ProjectAdmin); err != nil {
			t.Fatal(err)
		}
	}
	if r := f.do(f.carol, "PUT", "/api/workflows/solo-shell?project="+orgSoloP, orgShellDef("solo-shell")); r.code != 200 {
		t.Errorf("carol (no tenant) saving a run: step: got %d, want 200 as today: %.300s", r.code, r.body)
	}
}

// Without the policy (but through the same strict config loader), today's
// behaviour for a tenantless admin. Passes on main.
func TestOrgGuardDoesNotTouchTenantlessUsers(t *testing.T) {
	f := newOrgFixture(t, "mode: local\n")
	f.orgSoloAdminSavesShell(t)
}

// ---------------------------------------------------------------------------
// Route coverage
// ---------------------------------------------------------------------------

// orgRouteAllowList is the ONLY escape hatch from TestOrgEveryBrowserRouteIsTenantChecked.
// A browser route under /api/ either has a case in orgCases() — which proves
// another tenant's things are 404 / filtered out on it — or is named here with
// the reason it holds nothing of any tenant. Adding a route here is a claim a
// reviewer must be able to check; when in doubt, write a case instead.
var orgRouteAllowList = map[string]string{
	// Public, or how a subject is obtained / asks about itself.
	"GET /api/health":                         "liveness",
	"POST /api/device/code":                   "device grant",
	"POST /api/device/token":                  "device grant",
	"POST /api/device/verify":                 "device grant",
	"GET /api/whoami":                         "the caller about itself",
	"DELETE /api/tokens/self":                 "the caller's own token",
	"POST /api/auth/session":                  "sign-in",
	"POST /api/auth/link":                     "sign-in",
	"POST /api/auth/password":                 "sign-in",
	"POST /api/auth/password/change":          "the caller's own password",
	"POST /api/auth/refresh":                  "sign-in",
	"GET /api/auth/providers":                 "sign-in page",
	"GET /api/auth/oauth/{provider}/start":    "sign-in",
	"GET /api/auth/oauth/{provider}/callback": "sign-in",

	// Superadmin-only administration of the install (admin:* / workers:* /
	// env:write on the system scope): no tenant reaches them.
	"GET /api/users":                                                "superadmin",
	"POST /api/users":                                               "superadmin",
	"PUT /api/users/{name}":                                         "superadmin",
	"POST /api/users/{name}/password":                               "superadmin",
	"DELETE /api/users/{name}":                                      "superadmin",
	"GET /api/users/links":                                          "superadmin",
	"POST /api/users/{name}/link/confirm":                           "superadmin",
	"POST /api/users/{name}/link/reject":                            "superadmin",
	"GET /api/roles":                                                "superadmin",
	"PUT /api/roles/{name}":                                         "superadmin",
	"DELETE /api/roles/{name}":                                      "superadmin",
	"GET /api/tenants":                                              "superadmin: tenant administration",
	"POST /api/tenants":                                             "superadmin: tenant administration",
	"GET /api/tenants/{tenant}/projects":                            "superadmin: tenant administration",
	"POST /api/tenants/{tenant}/projects":                           "superadmin: tenant administration",
	"GET /api/tenants/{tenant}/principals":                          "superadmin: tenant administration",
	"POST /api/tenants/{tenant}/principals":                         "superadmin: tenant administration",
	"DELETE /api/tenants/{tenant}/principals/{id}":                  "superadmin: tenant administration",
	"PUT /api/tenants/{tenant}/principals/{id}/surface":             "superadmin: tenant administration",
	"PUT /api/tenants/{tenant}/principals/{id}/workflows":           "superadmin: tenant administration",
	"PUT /api/tenants/{tenant}/issuer-links":                        "superadmin: tenant administration",
	"POST /api/tenants/{tenant}/issuer-revocations":                 "superadmin: tenant administration",
	"GET /api/tenants/{tenant}/connections":                         "superadmin: tenant administration",
	"POST /api/tenants/{tenant}/connections":                        "superadmin: tenant administration",
	"PUT /api/tenants/{tenant}/connections/{name}/tools":            "superadmin: tenant administration",
	"DELETE /api/tenants/{tenant}/connections/{name}":               "superadmin: tenant administration",
	"GET /api/carry":                                                "superadmin: machine setup",
	"GET /api/carry/states":                                         "superadmin: machine setup",
	"GET /api/carry/tools":                                          "superadmin: machine setup",
	"POST /api/carry/tools":                                         "superadmin: machine setup",
	"DELETE /api/carry/tools/{name}":                                "superadmin: machine setup",
	"GET /api/carry/files":                                          "superadmin: machine setup",
	"GET /api/carry/file":                                           "superadmin: machine setup",
	"PUT /api/carry/file":                                           "superadmin: machine setup",
	"DELETE /api/carry/file":                                        "superadmin: machine setup",
	"GET /api/carry/env":                                            "superadmin: machine setup",
	"PUT /api/carry/env":                                            "superadmin: machine setup",
	"DELETE /api/carry/env/{key}":                                   "superadmin: machine setup",
	"GET /api/carry/log":                                            "superadmin: machine setup",
	"POST /api/carry/rollback":                                      "superadmin: machine setup",
	"GET /api/env":                                                  "superadmin: system env (env:read)",
	"PUT /api/env":                                                  "superadmin: system env",
	"DELETE /api/env/{key}":                                         "superadmin: system env",
	"GET /api/workers":                                              "superadmin: runners",
	"POST /api/workers/token/rotate":                                "superadmin: runners",
	"DELETE /api/workers/{id}":                                      "superadmin: runners",
	"GET /api/builtins":                                             "workers:read",
	"POST /api/builtins/{name}/update":                              "superadmin",
	"POST /api/sources":                                             "superadmin: projects:write (import a repository as a source)",
	"DELETE /api/sources/{name}":                                    "superadmin: projects:write",
	"POST /api/skills/installed":                                    "superadmin: installs code",
	"DELETE /api/skills/installed/{name}":                           "superadmin: installs code",
	"PUT /api/mcp/{name}":                                           "superadmin: installs code",
	"DELETE /api/mcp/{name}":                                        "superadmin: installs code",
	"PUT /api/providers/{name}":                                     "superadmin: registry write",
	"DELETE /api/providers/{name}":                                  "superadmin: registry write",
	"PUT /api/classifiers/{name}":                                   "superadmin: registry write",
	"DELETE /api/classifiers/{name}":                                "superadmin: registry write",
	"PUT /api/prices":                                               "superadmin: registry write",
	"DELETE /api/prices":                                            "superadmin: registry write",
	"POST /api/skill-sources":                                       "superadmin: registry write",
	"PUT /api/skill-sources/{name}/ref":                             "superadmin: registry write",
	"POST /api/skill-sources/{name}/refresh":                        "superadmin: registry write",
	"DELETE /api/skill-sources/{name}":                              "superadmin: registry write",
	"POST /api/modules/{name}/install":                              "superadmin: installs code",
	"PUT /api/modules/{name}/config":                                "superadmin: system env",
	"POST /api/modules/{name}/services":                             "superadmin: module services",
	"DELETE /api/modules/{name}/services/{account}":                 "superadmin: module services",
	"GET /api/modules/{name}/services/{account}/watches":            "registry read: a module service's watches",
	"POST /api/modules/{name}/services/{account}/watches":           "superadmin: module services",
	"DELETE /api/modules/{name}/services/{account}/watches/{watch}": "superadmin: module services",
	"PUT /api/modules/{name}/profiles/{profile}":                    "superadmin: module profiles",
	"GET /api/modules/{name}/profiles":                              "registry read",

	// Machines: worker tokens and module/service tokens, checked in the
	// handler; never a browser user.
	"POST /api/workers/join":                              "runner",
	"POST /api/workers/claim":                             "runner",
	"POST /api/workers/heartbeat":                         "runner",
	"GET /api/workers/services":                           "runner",
	"POST /api/workers/services/heartbeat":                "runner",
	"GET /api/workers/providers":                          "runner",
	"GET /api/workers/builtins":                           "runner",
	"POST /api/workers/builtins":                          "runner",
	"POST /api/workers/jobs/{jobId}/events":               "runner",
	"POST /api/workers/jobs/{jobId}/result":               "runner",
	"GET /api/service-self/{name}/{account}/watches":      "module service token",
	"POST /api/service-self/{name}/{account}/heartbeat":   "module service token",
	"GET /api/service-self/{name}/{account}/outbox":       "module service token",
	"POST /api/service-self/{name}/{account}/outbox/{id}": "module service token",
	"POST /api/module-self/{module}/outbox":               "module token",
	"GET /api/module-self/{module}/outbox":                "module token",
	"GET /api/module-self/{module}/profile":               "module token",

	// The service API: its tenant comes from the credential (tenants.go,
	// customer.go) and has its own tests.
	"GET /api/svc/self":                   "service subject, tenant from credential",
	"GET /api/svc/runs":                   "service subject, tenant from credential",
	"GET /api/svc/runs/{id}":              "service subject, tenant from credential",
	"POST /api/svc/workflows/{name}/runs": "service subject, tenant from credential",
	"GET /api/svc/workflows":              "service subject, tenant from credential",
	"POST /api/svc/workflows":             "service subject, tenant from credential",
	"POST /api/svc/workflows/validate":    "service subject, tenant from credential",
	"GET /api/svc/workflows/{name}":       "service subject, tenant from credential",

	// The shared registry: readable by every signed-in person, written only
	// by a superadmin (#189: "the shared registry stays readable").
	"GET /api/skills":                      "shared registry",
	"GET /api/skills/installed":            "shared registry",
	"GET /api/tools":                       "shared registry",
	"GET /api/mcp":                         "shared registry",
	"GET /api/providers":                   "shared registry",
	"GET /api/providers/{name}/models":     "shared registry",
	"GET /api/adapters":                    "shared registry",
	"GET /api/classifiers":                 "shared registry",
	"GET /api/registries":                  "shared registry",
	"GET /api/models":                      "shared registry",
	"GET /api/doctor":                      "shared registry (server health)",
	"GET /api/doctor/wfx":                  "shared registry (server health)",
	"GET /api/prices":                      "shared registry",
	"GET /api/categories":                  "the closed category list",
	"GET /api/skill-sources":               "shared registry",
	"GET /api/skill-sources/{name}/skills": "shared registry",
	"GET /api/skill-sources/{name}/refs":   "shared registry",
	"GET /api/modules":                     "shared registry",
	"GET /api/modules/{name}":              "shared registry",

	// Tenant-neutral by construction: the body is a draft that belongs to no
	// project. The org guard covers them (TestOrgGuardedPolicyRefusesShellAndFileWrites).
	"POST /api/workflows/validate": "draft, no project; guarded",
	"POST /api/dryrun":             "draft, no project; guarded",

	// OPEN QUESTIONS — listed so the decision is visible, not made silently.
	// Channel adapters and module commands are configured per server today;
	// a channel send and a module call are not addressed to any tenant's
	// project. If channels or modules become per-tenant, these need cases.
	"GET /api/channels":                 "OPEN: server-level channel adapters",
	"POST /api/channels/send":           "OPEN: server-level channel adapters",
	"POST /api/channels/{name}/receive": "OPEN: server-level channel adapters",
	"POST /api/modules/{name}/call":     "OPEN: a module command run belongs to which tenant?",
	"POST /api/judge":                   "OPEN: spends the server's classifier key; no tenant data",
}

// Every browser route under /api/ is tenant-checked by a case in orgCases(),
// or is in orgRouteAllowList. The route list is the REAL router's (chi.Walk),
// off loopback and with a blob store so the bundle routes are mounted too; a
// route added without a case, and without a reasoned allow-list entry, fails
// here. Each case's route is found by the router itself (Mux.Find), so a case
// cannot claim a route its URL does not reach.
func TestOrgEveryBrowserRouteIsTenantChecked(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "w.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	bl, err := blob.OpenFolder(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	const addr = "0.0.0.0:8090"
	eng := engine.New(config.Config{Addr: addr}, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	mux, ok := New(eng, st, bl, addr, "", nil).(*chi.Mux)
	if !ok {
		t.Fatal("the handler is not a *chi.Mux")
	}

	routes := map[string]bool{}
	if err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/api/") {
			routes[method+" "+route] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	f := &orgFixture{runG: uuid.NewString(), propG: uuid.NewString(), digG: "sha256:" + strings.Repeat("b", 64)}
	rep := f.placeholders(false)
	covered := map[string][]string{}
	for _, c := range orgCases() {
		p := rep.Replace(c.path)
		if i := strings.IndexByte(p, '?'); i >= 0 {
			p = p[:i]
		}
		pattern := mux.Find(chi.NewRouteContext(), c.method, p)
		key := c.method + " " + pattern
		if pattern == "" || !routes[key] {
			t.Errorf("case %q (%s %s) reaches no route of the real router (found %q)", c.name, c.method, p, pattern)
			continue
		}
		covered[key] = append(covered[key], c.name)
	}

	var missing, stale []string
	for r := range routes {
		if _, ok := orgRouteAllowList[r]; ok {
			if len(covered[r]) > 0 {
				stale = append(stale, r)
			}
			continue
		}
		if len(covered[r]) == 0 {
			missing = append(missing, r)
		}
	}
	for r := range orgRouteAllowList {
		if !routes[r] {
			stale = append(stale, r+" (no such route)")
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("browser routes with no tenancy case and no allow-list entry (add a case to orgCases, or a reasoned entry to orgRouteAllowList):\n  %s", strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("allow-list entries that are covered by a case or name no route (remove them):\n  %s", strings.Join(stale, "\n  "))
	}

	// A "superadmin" reason must be true: the route needs a permission a
	// plain signed-in person does not hold.
	for r, why := range orgRouteAllowList {
		if !strings.HasPrefix(why, "superadmin") {
			continue
		}
		method, route, _ := strings.Cut(r, " ")
		if perm := PermissionFor(method, route); perm == PermPublic || signedInReads[perm] {
			t.Errorf("allow-list says %s is %q, but any signed-in person holds its permission %q", r, why, perm)
		}
	}

	// The bundle routes are exercised on orgBundleRouter; it must mirror the
	// real router's bundle routes exactly.
	f2 := orgBundleRouter(eng, st, bl).(*chi.Mux)
	mirror := map[string]bool{}
	_ = chi.Walk(f2, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		mirror[method+" "+route] = true
		return nil
	})
	for r := range routes {
		if strings.Contains(r, " /api/bundles") && !mirror[r] {
			t.Errorf("bundle route %s is not mirrored by orgBundleRouter", r)
		}
	}
	for r := range mirror {
		if !routes[r] {
			t.Errorf("orgBundleRouter has %s, which the real router does not", r)
		}
	}
}

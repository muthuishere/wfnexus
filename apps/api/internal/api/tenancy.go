package api

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// ORGANIZATION SCOPING OF THE BROWSER API (#189, ADR 0035).
//
// A person signed in through an organization provider is bound to one tenant
// (Subject.Tenant, from user_tenants). Everything of another tenant — its
// projects, their workflows, runs, state, memory, proposals, bundles,
// conversations — is answered exactly as a thing that does not exist: 404,
// the same body, nothing changed.
//
// ONE decision: tenantVisible(r, project). Every tenant question is first
// turned into "which project is this?" (a workflow's is the project it was
// loaded from, a run's is the run's project, state's is its workflow's or its
// project's) and then asked here. The callers are:
//
//   - authorize, through tenantGate, for everything a request names in its
//     path or in ?project= — BEFORE the role check, so another tenant's thing
//     is a 404 and never a 403;
//   - the role checks (roles.go): a grant counts only inside the caller's
//     tenant, and an org-admin administers the tenant's projects;
//   - inScope (authz.go), which the handlers already call for what a body
//     names (a copy's destination, a drift proposal, a bundle's project);
//   - the listings, which filter on it (or, for runs and metrics, push the
//     same rule into SQL through callerTenant).
//
// Who sees what:
//
//	superadmin, worker, loopback   every tenant (no filter)
//	person in an organization      that tenant only
//	person in no organization      the default tenant: every project not
//	                               bound to a tenant — on an install without
//	                               organizations, everything, as before
//
// The shipped templates (engine.TemplatesSource) belong to no tenant: every
// signed-in person may read and copy them, like the rest of the shared
// registry.

// callerTenant is the tenant the caller is confined to. confined is false for
// a caller that sees every tenant: no subject (only reachable on a loopback
// bind), a worker, a superadmin.
func (s *Server) callerTenant(r *http.Request) (tenant string, confined bool) {
	sub, ok := SubjectFrom(r.Context())
	if !ok {
		return "", false
	}
	if sub.Kind == SubjectWorker || sub.Role == RoleSuperadmin {
		return "", false
	}
	if sub.Tenant == "" {
		return store.DefaultTenant, true
	}
	return sub.Tenant, true
}

// tenantVisible is THE tenant decision: may the caller see what belongs to
// this project? A project name that names nothing is the default tenant's,
// which is why a never-existed thing and another tenant's thing answer an
// organization's person the same way. A store error is "no".
func (s *Server) tenantVisible(r *http.Request, project string) bool {
	tenant, confined := s.callerTenant(r)
	if !confined || project == engine.TemplatesSource {
		return true
	}
	owner, err := s.projectTenant(r, project)
	return err == nil && owner == tenant
}

// tenantWritable is tenantVisible for the role checks, which decide what a
// caller may CHANGE: the shipped templates are readable by every tenant and
// so changeable by none — their env, state and memory would otherwise be a
// channel between organizations. A person in no organization keeps what
// they had.
func (s *Server) tenantWritable(r *http.Request, project string) bool {
	return s.tenantVisible(r, project) && (project != engine.TemplatesSource || orgPerson(r) == nil)
}

// projectTenant names a project's tenant from the bindings, read once per
// request.
func (s *Server) projectTenant(r *http.Request, project string) (string, error) {
	if s.store == nil {
		return store.DefaultTenant, nil
	}
	sub, ok := SubjectFrom(r.Context())
	if !ok {
		return s.store.ProjectTenant(r.Context(), project)
	}
	if !sub.projectTenantsLoaded {
		sub.projectTenants, sub.projectTenantsErr = s.store.ProjectTenants(r.Context())
		sub.projectTenantsLoaded = true
	}
	if sub.projectTenantsErr != nil {
		return "", sub.projectTenantsErr
	}
	if t, ok := sub.projectTenants[project]; ok {
		return t, nil
	}
	return store.DefaultTenant, nil
}

// orgPerson is the caller when it is a person in an organization, else nil.
func orgPerson(r *http.Request) *Subject {
	sub, ok := SubjectFrom(r.Context())
	if !ok || sub.Kind != SubjectUser || sub.Tenant == "" {
		return nil
	}
	return sub
}

// orgAdmin: an organization's org-admin, who administers every project of
// the tenant without member rows.
func orgAdmin(r *http.Request) bool {
	sub := orgPerson(r)
	return sub != nil && sub.OrgRole == config.OrgRoleAdmin
}

// orgMember: any person in an organization (org-admin or member). A member
// may create projects in the tenant, and run and save its workflows.
func orgMember(r *http.Request) bool { return orgPerson(r) != nil }

// ---- the gate in authorize ----

// tenantGate is authorize's tenant question, asked before the role question.
// It turns what the request names in its path (a project, a workflow, a
// template, a run, a proposal, a bundle, a state namespace) and in ?project=
// into a project and asks tenantVisible. false: answer 404.
//
// Something the path names that does not exist is let through for a person
// in no organization, whose handlers answer it as they always have; for a
// person in an organization it is a 404 here, the same as another tenant's.
func (s *Server) tenantGate(r *http.Request) bool {
	if _, confined := s.callerTenant(r); !confined {
		return true
	}
	org := orgMember(r)
	q := r.URL.Query()
	// ?project= names the project a request reads (runs, proposals, drift,
	// bundles, memory) or writes into (a save).
	if p := q.Get("project"); p != "" && !s.tenantVisible(r, p) {
		return false
	}
	path, method := r.URL.EscapedPath(), r.Method
	name, sub := "", ""
	at := func(prefix string) bool {
		if !strings.HasPrefix(path, prefix) {
			return false
		}
		parts := strings.SplitN(strings.TrimPrefix(path, prefix), "/", 3)
		name, _ = url.PathUnescape(parts[0])
		sub = ""
		if len(parts) > 1 {
			sub = parts[1]
		}
		return true
	}
	switch {
	case at("/api/projects/"):
		return s.tenantVisible(r, name)
	case at("/api/workflows/"):
		if method == http.MethodPost && sub == "" && (name == "reload" || name == "validate") {
			return true
		}
		// A NEW workflow saved into ?project= belongs to that project,
		// checked above.
		if method == http.MethodPut && sub == "" && q.Get("project") != "" && s.eng.Definitions()[name] == nil {
			return true
		}
		return s.tenantVisible(r, s.eng.ProjectFor(name))
	case at("/api/templates/"):
		def, err := s.eng.Template(name)
		if err != nil {
			return !org
		}
		return s.tenantVisible(r, sourceProject(def))
	case at("/api/runs/"):
		id, err := uuid.Parse(name)
		if err != nil {
			return true // the handler answers 400
		}
		run, err := s.store.GetRun(r.Context(), id)
		if err != nil {
			return !org
		}
		return s.tenantVisible(r, run.Project)
	case at("/api/proposals/"):
		if name == "drift" {
			return true // ?project= above; the body's project in proposeDrift
		}
		id, err := uuid.Parse(name)
		if err != nil {
			return true
		}
		p, err := s.store.GetProposal(r.Context(), id)
		if err != nil {
			return !org
		}
		return s.tenantVisible(r, p.Project)
	case at("/api/bundles/"):
		if name == "tags" {
			return true // the body's project, in setBundleTag
		}
		b, err := s.store.BundleByDigest(r.Context(), name)
		if err != nil {
			return !org
		}
		return s.tenantVisible(r, b.Project)
	case path == "/api/state" && method == http.MethodPut:
		return true // the body names the namespace: setState (orgStateWrite)
	case path == "/api/state" || strings.HasPrefix(path, "/api/state/"):
		// A list or a delete names its namespace in the query. No scope, or
		// the global one, is the server's (stateProject "").
		return s.tenantVisible(r, s.stateProject(q.Get("scope"), q.Get("name")))
	}
	return true
}

// sourceProject is the project a definition was loaded from.
func sourceProject(d *workflow.Definition) string {
	if d.Source == "" {
		return "local"
	}
	return d.Source
}

// stateProject names the project a state namespace belongs to: a workflow's
// (and its steps') is the workflow's project, a project's is the project.
// The global namespace, and anything else, belongs to no project: "" — the
// default tenant's, which no organization sees.
func (s *Server) stateProject(scope, name string) string {
	switch scope {
	case model.StateScopeWorkflow:
		return s.eng.ProjectFor(name)
	case model.StateScopeStep:
		// <workflow>/<step>; the workflow may itself be qualified.
		if i := strings.LastIndexByte(name, '/'); i > 0 {
			return s.eng.ProjectFor(name[:i])
		}
		return s.eng.ProjectFor(name)
	case model.StateScopeProject:
		return name
	}
	return ""
}

// ---- project names inside an organization ----

// orgProjectSep joins a tenant and the short name its people gave a project.
// Two organizations may each have a project called "app"; on disk and in the
// store they are "acme--app" and "globex--app", and each is shown, and
// addressed, as "app" inside its own tenant.
const orgProjectSep = "--"

// storedProjectName is the name a project created by a person in tenant is
// stored under.
func storedProjectName(tenant, short string) string { return tenant + orgProjectSep + short }

// shortProjectName is how a stored project name is shown inside its tenant.
func shortProjectName(r *http.Request, stored string) string {
	if sub := orgPerson(r); sub != nil {
		return strings.TrimPrefix(stored, sub.Tenant+orgProjectSep)
	}
	return stored
}

// resolveProject turns the name a person in an organization addressed a
// project by into the stored one: the name itself when it is the tenant's,
// else the tenant's namespaced project of that short name, else unchanged
// (and then tenantVisible refuses it).
func (s *Server) resolveProject(r *http.Request, name string) string {
	sub := orgPerson(r)
	if sub == nil || name == "" || s.tenantVisible(r, name) {
		return name
	}
	if stored := storedProjectName(sub.Tenant, name); s.tenantVisible(r, stored) {
		return stored
	}
	return name
}

// addressShortProjects rewrites a request from a person in an organization
// that names a project by its short name — /api/projects/{name}… or
// ?project= — to the stored name, in the URL and in the router's path, so
// every handler and every check after this one sees the stored name. It runs
// in authorize, before tenantGate.
func (s *Server) addressShortProjects(r *http.Request) {
	if orgPerson(r) == nil {
		return
	}
	if q := r.URL.Query(); q.Get("project") != "" {
		if stored := s.resolveProject(r, q.Get("project")); stored != q.Get("project") {
			q.Set("project", stored)
			r.URL.RawQuery = q.Encode()
		}
	}
	const prefix = "/api/projects/"
	path := r.URL.EscapedPath()
	if !strings.HasPrefix(path, prefix) {
		return
	}
	seg, rest, _ := strings.Cut(strings.TrimPrefix(path, prefix), "/")
	name, err := url.PathUnescape(seg)
	if err != nil {
		return
	}
	stored := s.resolveProject(r, name)
	if stored == name {
		return
	}
	escaped := prefix + url.PathEscape(stored)
	if rest != "" || strings.HasSuffix(path, seg+"/") {
		escaped += "/" + rest
	}
	if rctx := chi.RouteContext(r.Context()); rctx != nil && strings.HasSuffix(rctx.RoutePath, strings.TrimPrefix(path, "/api")) {
		rctx.RoutePath = strings.TrimSuffix(rctx.RoutePath, strings.TrimPrefix(path, "/api")) + strings.TrimPrefix(escaped, "/api")
	}
	r.URL.RawPath = escaped
	if p, err := url.PathUnescape(escaped); err == nil {
		r.URL.Path = p
	}
}

// ---- the authoring guard for organizations (orgPolicy: guarded) ----

// orgGuardSave refuses, for a person whose organization policy is guarded
// (orgGuarded), a definition the customer authoring guard refuses: no `run:`
// shell step, no tools (file writes among them), no mount, env, jobs, uses,
// runs-on — and no triggers, since a saved schedule or hook is a run the
// scheduler starts later with no person in front of it. nil for everyone
// else.
func (s *Server) orgGuardSave(r *http.Request, d *workflow.Definition) error {
	if d == nil || !s.orgGuarded(r) {
		return nil
	}
	if err := customerWorkflowGuard(d); err != nil {
		return fmt.Errorf("refused by your organization's policy (guarded): %w", err)
	}
	return nil
}

// orgGuardRun is orgGuardSave for a run, a dispatch or a dry run of what is
// already on disk: the same checks, triggers aside — this request is the
// person starting the run, and a trigger in the file does not start it.
func (s *Server) orgGuardRun(r *http.Request, d *workflow.Definition) error {
	if d == nil || !s.orgGuarded(r) {
		return nil
	}
	if err := customerRunGuard(d); err != nil {
		return fmt.Errorf("refused by your organization's policy (guarded): %w", err)
	}
	return nil
}

// tenantSkips is the caller's tenant's load problems. A problem of a
// tenant's own workflow can still name another tenant's project — "the name
// "deploy" is already used by source "globex-app"" (#163) — and such a
// reason is replaced with one that names nobody.
func (s *Server) tenantSkips(r *http.Request, skips []workflow.Skip) []workflow.Skip {
	out := []workflow.Skip{}
	for _, sk := range skips {
		if !s.tenantVisible(r, sk.Source) {
			continue
		}
		for _, src := range s.eng.Sources() {
			if !s.tenantVisible(r, src.Name) && strings.Contains(sk.Reason, `"`+src.Name) {
				sk.Reason = "the name is already used by a workflow this account cannot see"
				break
			}
		}
		out = append(out, sk)
	}
	return out
}

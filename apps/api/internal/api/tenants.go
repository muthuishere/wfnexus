package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// Tenants and service principals.
//
// ADMINISTRATION (/api/tenants/...) is an operator's: admin:read/admin:write,
// which only a superadmin holds (or nobody on a loopback bind, the same rule
// as /api/users).
//
// THE TENANT ROUTES (/api/svc/...) are a service principal's. The tenant a
// request acts in is the one stored on the principal its credential resolved
// to; nothing in the path, query or body can name another. A resource of
// another tenant answers exactly as an absent one does.

// serviceRunsForTest is how a test switches service runs on. Nothing outside
// _test.go files assigns it.
var serviceRunsForTest bool

func (s *Server) tenantRoutes(r chi.Router) {
	r.Get("/tenants", s.listTenants)
	r.Post("/tenants", s.createTenant)
	r.Get("/tenants/{tenant}/projects", s.listTenantProjects)
	r.Post("/tenants/{tenant}/projects", s.bindTenantProject)
	r.Get("/tenants/{tenant}/principals", s.listPrincipals)
	r.Post("/tenants/{tenant}/principals", s.createPrincipal)
	r.Delete("/tenants/{tenant}/principals/{id}", s.revokePrincipal)
	r.Put("/tenants/{tenant}/principals/{id}/surface", s.setPrincipalSurface)
	r.Put("/tenants/{tenant}/principals/{id}/workflows", s.setPrincipalWorkflows)

	r.Get("/svc/self", s.svcSelf)
	r.Get("/svc/runs", s.svcListRuns)
	r.Get("/svc/runs/{id}", s.svcGetRun)
	r.Post("/svc/workflows/{name}/runs", s.svcStartRun)
}

// ---- administration ----

// operator answers whether the caller may administer tenants: a superadmin,
// or nobody on a loopback bind. A worker token is a machine claiming jobs and
// is NOT an operator here, although can() lets it through the route table.
// Returns false having already answered.
func (s *Server) operator(w http.ResponseWriter, r *http.Request) bool {
	sub, ok := SubjectFrom(r.Context())
	if (!ok && s.loopbackOnly) || (ok && sub.Kind == SubjectUser && sub.Role == RoleSuperadmin) {
		return true
	}
	writeErr(w, http.StatusForbidden, errors.New("forbidden"))
	return false
}

func (s *Server) listTenants(w http.ResponseWriter, r *http.Request) {
	if !s.operator(w, r) {
		return
	}
	ts, err := s.store.ListTenants(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if ts == nil {
		ts = []*store.Tenant{}
	}
	writeJSON(w, 200, ts)
}

func (s *Server) createTenant(w http.ResponseWriter, r *http.Request) {
	if !s.operator(w, r) {
		return
	}
	var b struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, 400, err)
		return
	}
	t, err := s.store.CreateTenant(r.Context(), b.ID, b.Name)
	switch {
	case errors.Is(err, store.ErrBadTenantID):
		writeErr(w, 400, err)
	case errors.Is(err, store.ErrTenantExists):
		writeErr(w, 409, err)
	case err != nil:
		writeErr(w, 500, err)
	default:
		writeJSON(w, 201, t)
	}
}

// adminTenant reads {tenant} and answers 404 itself when it does not exist.
func (s *Server) adminTenant(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.operator(w, r) {
		return "", false
	}
	id := chi.URLParam(r, "tenant")
	if _, err := s.store.GetTenant(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w)
		} else {
			writeErr(w, 500, err)
		}
		return "", false
	}
	return id, true
}

func (s *Server) listTenantProjects(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	ps, err := s.store.TenantProjects(r.Context(), t)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, ps)
}

func (s *Server) bindTenantProject(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	var b struct {
		Project string `json:"project"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Project == "" {
		writeErr(w, 400, errors.New("project is required"))
		return
	}
	if err := s.store.BindProject(r.Context(), t, b.Project); err != nil {
		if errors.Is(err, store.ErrProjectBound) {
			writeErr(w, 409, err)
			return
		}
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tenant": t, "project": b.Project})
}

func (s *Server) listPrincipals(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	ps, err := s.store.ListServicePrincipals(r.Context(), t)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if ps == nil {
		ps = []*store.ServicePrincipal{}
	}
	writeJSON(w, 200, ps)
}

// createPrincipal mints the principal's token and returns it ONCE. Only its
// hash is stored and no route reads it back; a lost token is replaced by
// revoking the principal and creating another.
func (s *Server) createPrincipal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	var b struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || !principalNameRe.MatchString(b.Name) {
		writeErr(w, 400, errors.New("name must be 1-64 of [A-Za-z0-9._-]"))
		return
	}
	tok := auth.NewServiceToken()
	p, err := s.store.CreateServicePrincipal(r.Context(), t, b.Name, auth.HashToken(tok), tok[:len(auth.ServiceTokenPrefix)+6])
	if err != nil {
		if errors.Is(err, store.ErrPrincipalExists) {
			writeErr(w, 409, err)
			return
		}
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 201, map[string]any{"principal": p, "token": tok})
}

func (s *Server) adminPrincipalID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		notFound(w)
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) revokePrincipal(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	id, ok := s.adminPrincipalID(w, r)
	if !ok {
		return
	}
	if err := s.store.RevokeServicePrincipal(r.Context(), t, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w)
			return
		}
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revoked": true})
}

var (
	principalNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	toolNameRe      = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,128}$`)
)

const maxDeclaredTools = 500

// setPrincipalSurface records where a principal's tools are reached and their
// names. A REFERENCE list only: it is stored and shown, never called here.
func (s *Server) setPrincipalSurface(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	id, ok := s.adminPrincipalID(w, r)
	if !ok {
		return
	}
	var b struct {
		Endpoint string   `json:"endpoint"`
		Tools    []string `json:"tools"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, 400, err)
		return
	}
	if b.Endpoint != "" {
		u, err := url.Parse(b.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
			writeErr(w, 400, errors.New("endpoint must be an absolute http(s) URL without credentials"))
			return
		}
	}
	if len(b.Tools) > maxDeclaredTools {
		writeErr(w, 400, errors.New("too many tools"))
		return
	}
	for _, tool := range b.Tools {
		if !toolNameRe.MatchString(tool) {
			writeErr(w, 400, errors.New("tool names must be 1-128 of [A-Za-z0-9._:/-]"))
			return
		}
	}
	if err := s.store.SetServicePrincipalSurface(r.Context(), t, id, b.Endpoint, b.Tools); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w)
			return
		}
		writeErr(w, 500, err)
		return
	}
	p, err := s.store.ServicePrincipalInTenant(r.Context(), t, id)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, p)
}

// setPrincipalWorkflows replaces the workflow names a principal may start.
func (s *Server) setPrincipalWorkflows(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	id, ok := s.adminPrincipalID(w, r)
	if !ok {
		return
	}
	var b struct {
		Workflows []string `json:"workflows"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, 400, err)
		return
	}
	if len(b.Workflows) > maxDeclaredTools {
		writeErr(w, 400, errors.New("too many workflows"))
		return
	}
	for _, n := range b.Workflows {
		if !toolNameRe.MatchString(n) {
			writeErr(w, 400, errors.New("workflow names must be 1-128 of [A-Za-z0-9._:/-]"))
			return
		}
	}
	if err := s.store.SetServicePrincipalWorkflows(r.Context(), t, id, b.Workflows); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w)
			return
		}
		writeErr(w, 500, err)
		return
	}
	p, err := s.store.ServicePrincipalInTenant(r.Context(), t, id)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, p)
}

// ---- the tenant routes ----

// serviceSubject is the gate every /api/svc/ handler goes through: the caller
// is a service principal, and its tenant is the one on the stored row. It
// returns false having ALREADY answered. A nil subject (loopback) is refused
// too: there is no tenant to act in without a credential.
func serviceSubject(w http.ResponseWriter, r *http.Request) (*Subject, bool) {
	sub, ok := SubjectFrom(r.Context())
	if !ok || sub.Kind != SubjectService || sub.Tenant == "" {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthenticated: a service principal token is required"))
		return nil, false
	}
	return sub, true
}

func (s *Server) svcSelf(w http.ResponseWriter, r *http.Request) {
	sub, ok := serviceSubject(w, r)
	if !ok {
		return
	}
	projects, err := s.store.TenantProjects(r.Context(), sub.Tenant)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"tenant":    sub.Tenant,
		"principal": sub.Principal,
		"projects":  projects,
	})
}

func (s *Server) svcListRuns(w http.ResponseWriter, r *http.Request) {
	sub, ok := serviceSubject(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.store.RunsInTenant(r.Context(), sub.Tenant, limit)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, runs)
}

func (s *Server) svcGetRun(w http.ResponseWriter, r *http.Request) {
	sub, ok := serviceSubject(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		notFound(w)
		return
	}
	run, err := s.store.RunInTenant(r.Context(), sub.Tenant, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w)
			return
		}
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, run)
}

// svcStartRun starts a workflow for a service principal. It answers 404, and
// creates nothing, unless ALL hold:
//   - starting runs is switched on for this server (serviceRuns; off);
//   - the principal lists the workflow by name;
//   - the workflow exists and its project is in the principal's tenant.
//
// Every refusal is the same 404, so a principal cannot learn which workflows
// exist outside its list or its tenant.
func (s *Server) svcStartRun(w http.ResponseWriter, r *http.Request) {
	if !s.serviceRuns {
		notFound(w)
		return
	}
	sub, ok := serviceSubject(w, r)
	if !ok {
		return
	}
	name := urlName(r, "name")
	may, err := s.store.PrincipalMayStart(r.Context(), sub.Principal.ID, name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if !may || s.eng.Definitions()[name] == nil {
		notFound(w)
		return
	}
	tn, err := s.store.ProjectTenant(r.Context(), s.eng.ProjectFor(name))
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if tn != sub.Tenant {
		notFound(w)
		return
	}
	var input json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeErr(w, 400, err)
		return
	}
	input, err = s.eng.PrepareInput(name, input)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	// No force: a principal cannot override `concurrency:`.
	run, err := s.eng.CreateRunAdmitted(r.Context(), name, input, false)
	if errors.Is(err, engine.ErrConcurrencyBusy) {
		writeErr(w, 409, err)
		return
	}
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	s.eng.Start(run.ID)
	writeJSON(w, 201, map[string]any{"run": run})
}

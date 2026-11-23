package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// The four roles (ADR 0028), and nothing else:
//
//	superadmin       users.role — everything
//	project_admin    members(project, P) — P's workflows, members, settings
//	workflow_admin   members(workflow, W) — edit, publish, expose, approve, members of W
//	workflow_runner  members(workflow, W) — run W, see its runs, answer its approvals
//
// Any signed-in person may read the catalogue (signedInReads); every write is
// decided here from those four, and nowhere else.

var signedInReads = map[string]bool{
	PermWorkflowsRead: true, PermRunsRead: true, PermRegistryRead: true,
	PermProjectsRead: true, PermStateRead: true, PermBundlesRead: true,
}

// isSuper: a superadmin, a worker, or nobody on a loopback bind.
func (s *Server) isSuper(r *http.Request) bool {
	sub, ok := SubjectFrom(r.Context())
	if !ok {
		return s.loopbackOnly
	}
	return sub.Kind == SubjectWorker || sub.Role == RoleSuperadmin
}

// grants loads the caller's member rows once per request. A row on a project
// or workflow outside the caller's tenant is dropped (#189): a grant never
// reaches across the tenant boundary, whatever rows a person holds from
// before a project was bound.
func (s *Server) grants(r *http.Request) []*store.Member {
	sub, ok := SubjectFrom(r.Context())
	if !ok || sub.User == nil {
		return nil
	}
	if !sub.grantsLoaded {
		all, _ := s.store.GrantsOf(r.Context(), sub.User.ID)
		sub.grants = nil
		for _, g := range all {
			project := g.Name
			if g.Scope == store.MemberWorkflow {
				project = s.eng.ProjectFor(g.Name)
			}
			if s.tenantVisible(r, project) {
				sub.grants = append(sub.grants, g)
			}
		}
		sub.grantsLoaded = true
	}
	return sub.grants
}

func (s *Server) holds(r *http.Request, scope, name string, roles ...string) bool {
	for _, g := range s.grants(r) {
		if g.Scope == scope && g.Name == name {
			for _, role := range roles {
				if g.Role == role {
					return true
				}
			}
		}
	}
	return false
}

// canAdminProject: a superadmin, a project_admin of it, or — inside their own
// tenant only — an organization's org-admin (#189).
func (s *Server) canAdminProject(r *http.Request, project string) bool {
	return s.isSuper(r) || s.tenantVisible(r, project) &&
		(orgAdmin(r) || s.holds(r, store.MemberProject, project, store.ProjectAdmin))
}

func (s *Server) canAdminWorkflow(r *http.Request, wf string) bool {
	return s.canAdminProject(r, s.eng.ProjectFor(wf)) || s.holds(r, store.MemberWorkflow, wf, store.WorkflowAdmin)
}

// canRunWorkflow: an admin of it, a workflow_runner of it, or any person in
// the organization whose tenant it is in (#189).
func (s *Server) canRunWorkflow(r *http.Request, wf string) bool {
	return s.canAdminWorkflow(r, wf) || s.holds(r, store.MemberWorkflow, wf, store.WorkflowRunner) ||
		orgMember(r) && s.tenantVisible(r, s.eng.ProjectFor(wf))
}

// canSaveInto: may write a workflow into project — its admin, or any person
// in the organization whose tenant it is in (a member edits the tenant's
// workflows, #189).
func (s *Server) canSaveInto(r *http.Request, project string) bool {
	return s.canAdminProject(r, project) || orgMember(r) && s.tenantVisible(r, project)
}

// orgMayAdmin is, for a person in an organization, the check a route that is
// "admin anywhere" for everyone else gets: admin of the project the request
// is about. It answers 403 itself; for anyone else it says yes.
func (s *Server) orgMayAdmin(w http.ResponseWriter, r *http.Request, project string) bool {
	if orgMember(r) && !s.canAdminProject(r, project) {
		writeErr(w, http.StatusForbidden, errors.New("forbidden: you do not administer this project"))
		return false
	}
	return true
}

// adminAnywhere: holds an admin role at some scope. Publishing a bundle,
// validating or dry-running a draft and proposing a change are not addressed
// to one workflow, so they need only that.
func (s *Server) adminAnywhere(r *http.Request) bool {
	if s.isSuper(r) || orgAdmin(r) {
		return true
	}
	for _, g := range s.grants(r) {
		if g.Role == store.ProjectAdmin || g.Role == store.WorkflowAdmin {
			return true
		}
	}
	return false
}

// scopedAllows is the authorize middleware's second question, for what
// reading alone does not grant.
func (s *Server) scopedAllows(r *http.Request) bool {
	if _, ok := SubjectFrom(r.Context()); !ok {
		return false
	}
	path, method := r.URL.EscapedPath(), r.Method
	read := method == http.MethodGet || method == http.MethodHead
	seg := func(rest string) (string, string) {
		parts := strings.SplitN(rest, "/", 3)
		name, _ := url.PathUnescape(parts[0])
		sub := ""
		if len(parts) > 1 {
			sub = parts[1]
		}
		return name, sub
	}
	switch {
	// #189: a person in an organization may create a project (bound to the
	// tenant, createProject), reload the definitions when they administer
	// something (the answer is filtered to the tenant), and reach the
	// state and conversation writes, whose handlers check the tenant and
	// the role of what the body names. Everyone else: as before.
	case orgMember(r) && method == http.MethodPost && path == "/api/projects":
		return true
	case orgMember(r) && method == http.MethodPost && path == "/api/workflows/reload":
		return s.adminAnywhere(r)
	case orgMember(r) && !read && (path == "/api/state" || strings.HasPrefix(path, "/api/state/") || path == "/api/conversations/state"):
		return true
	case !read && (strings.HasPrefix(path, "/api/bundles") || path == "/api/workflows/validate" ||
		path == "/api/dryrun" || strings.HasPrefix(path, "/api/proposals") ||
		(strings.HasPrefix(path, "/api/templates/") && strings.HasSuffix(path, "/copy"))):
		return s.adminAnywhere(r)
	case strings.HasPrefix(path, "/api/workflows/") && !read:
		name, sub := seg(strings.TrimPrefix(path, "/api/workflows/"))
		switch sub {
		case "":
			// A save with ?project= writes into that project, so that is
			// the project it is authorized against — not the name's own,
			// which for a new name is "local".
			if method == http.MethodPut {
				if p := r.URL.Query().Get("project"); p != "" {
					// …or the workflow's own admin, saving it where it is.
					return s.canSaveInto(r, p) ||
						s.eng.Definitions()[name] != nil && s.eng.ProjectFor(name) == p && s.canAdminWorkflow(r, name)
				}
				return s.canAdminWorkflow(r, name) || s.canSaveInto(r, s.eng.ProjectFor(name)) && orgMember(r)
			}
			return method == http.MethodDelete && s.canAdminWorkflow(r, name)
		case "memory", "expose", "members":
			return s.canAdminWorkflow(r, name)
		case "runs", "dryrun", "dispatches":
			return s.canRunWorkflow(r, name)
		}
	case strings.HasPrefix(path, "/api/projects/"):
		name, sub := seg(strings.TrimPrefix(path, "/api/projects/"))
		switch {
		case read:
			return (sub == "env" || sub == "events") && s.canAdminProject(r, name)
		case sub == "" && method == http.MethodPatch, sub == "env", sub == "expose", sub == "members", sub == "events":
			return s.canAdminProject(r, name)
		}
	case strings.HasPrefix(path, "/api/runs/") && !read:
		id, _ := seg(strings.TrimPrefix(path, "/api/runs/"))
		rid, err := uuid.Parse(id)
		if err != nil {
			return false
		}
		run, err := s.store.GetRun(r.Context(), rid)
		return err == nil && (s.canRunWorkflow(r, run.Workflow) || s.canAdminProject(r, run.Project))
	}
	return false
}

// runVisible: a superadmin sees every run; anyone else only runs of its
// tenant (#189), and of those the runs it started and the runs of workflows
// (or projects) it holds a role on — a person in an organization, every run
// of the tenant.
func (s *Server) runVisible(r *http.Request, run *store.Run) bool {
	if s.isSuper(r) {
		return true
	}
	if !s.tenantVisible(r, run.Project) {
		return false
	}
	sub, _ := SubjectFrom(r.Context())
	return orgMember(r) || (sub != nil && run.TriggeredBy == sub.Name) || s.canRunWorkflow(r, run.Workflow) || s.canAdminProject(r, run.Project)
}

// visibility is runVisible as a listing filter; nil for a superadmin.
func (s *Server) visibility(r *http.Request) *store.Visibility {
	if s.isSuper(r) {
		return nil
	}
	sub, _ := SubjectFrom(r.Context())
	v := &store.Visibility{TriggeredBy: sub.Name, TenantWide: orgMember(r)}
	v.Tenant, _ = s.callerTenant(r)
	for _, g := range s.grants(r) {
		if g.Scope == store.MemberWorkflow {
			v.Workflows = append(v.Workflows, g.Name)
		} else {
			v.Projects = append(v.Projects, g.Name)
		}
	}
	return v
}

// ---- members API (project and workflow scope) ----

// normalizeRole accepts "workflow-runner", "workflow runner", "Workflow_Runner".
func normalizeRole(s string) string {
	return strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(s)))
}

func memberScope(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/api/projects/") {
		return store.MemberProject
	}
	return store.MemberWorkflow
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	ms, err := s.store.ListMembers(r.Context(), memberScope(r), urlName(r, "name"))
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, ms)
}

// setMember is PUT …/members/{user} {role}. The middleware has already
// required an admin at this scope.
func (s *Server) setMember(w http.ResponseWriter, r *http.Request) {
	scope, name := memberScope(r), urlName(r, "name")
	var b struct {
		Role string `json:"role"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&b); err != nil {
			writeErr(w, 400, errors.New(`expected {"role": …}`))
			return
		}
	}
	role := normalizeRole(b.Role)
	if role == "" {
		role = map[string]string{store.MemberProject: store.ProjectAdmin, store.MemberWorkflow: store.WorkflowRunner}[scope]
	}
	okRole := (scope == store.MemberProject && role == store.ProjectAdmin) ||
		(scope == store.MemberWorkflow && (role == store.WorkflowAdmin || role == store.WorkflowRunner))
	if !okRole {
		writeErr(w, 400, errors.New("a project takes project_admin; a workflow takes workflow_admin or workflow_runner"))
		return
	}
	u, err := s.store.UserByName(r.Context(), urlName(r, "user"))
	if err != nil || !s.granteeSees(r, u, scope, name) {
		notFound(w)
		return
	}
	if err := s.store.SetMember(r.Context(), scope, name, u.ID, role); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"scope": scope, "name": name, "user": u.Name, "role": role})
}

// granteeSees: a grant never crosses the tenant boundary (#189). The person
// being granted must be able to see what the grant is on — asked of
// tenantVisible as that person would be asked, from their own binding. A
// person of another organization is answered as an unknown user.
func (s *Server) granteeSees(r *http.Request, u *store.User, scope, name string) bool {
	tenant, orgRole, err := s.store.UserTenant(r.Context(), u.ID)
	if err != nil {
		return false
	}
	project := name
	if scope == store.MemberWorkflow {
		project = s.eng.ProjectFor(name)
	}
	as := &Subject{Kind: SubjectUser, Name: u.Name, Role: u.Role, User: u, Tenant: tenant, OrgRole: orgRole}
	return s.tenantVisible(r.WithContext(withSubject(r.Context(), as)), project)
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) {
	u, err := s.store.UserByName(r.Context(), urlName(r, "user"))
	if err != nil {
		notFound(w)
		return
	}
	if err := s.store.RemoveMember(r.Context(), memberScope(r), urlName(r, "name"), u.ID); err != nil {
		notFound(w)
		return
	}
	writeJSON(w, 200, map[string]any{"removed": true})
}

// grantSpec applies one configured role spec ("superadmin",
// "project_admin:P", "workflow_admin:W", "workflow_runner:W") to a user.
func (s *Server) grantSpec(ctx context.Context, u *store.User, spec string) error {
	role, name, _ := strings.Cut(strings.TrimSpace(spec), ":")
	role = normalizeRole(role)
	switch role {
	case "":
		return nil
	case RoleSuperadmin:
		return s.store.SetUserRole(ctx, u.Name, RoleSuperadmin)
	case store.ProjectAdmin:
		return s.store.SetMember(ctx, store.MemberProject, name, u.ID, role)
	case store.WorkflowAdmin, store.WorkflowRunner:
		return s.store.SetMember(ctx, store.MemberWorkflow, name, u.ID, role)
	}
	return errors.New("unknown role " + spec)
}

// revokeSpec takes away what grantSpec gives for the same spec, and says
// whether anything changed.
func (s *Server) revokeSpec(ctx context.Context, u *store.User, spec string) (bool, error) {
	role, name, _ := strings.Cut(strings.TrimSpace(spec), ":")
	role = normalizeRole(role)
	switch role {
	case "":
		return false, nil
	case RoleSuperadmin:
		if u.Role != RoleSuperadmin {
			return false, nil
		}
		// The host keeps an administrator: a group change never takes the
		// last one away, even when several leave at once (the check and the
		// change are one transaction). It is logged for someone to sort out.
		revoked, err := s.store.RevokeSuperadminUnlessLast(ctx, u.Name)
		if err != nil {
			return false, err
		}
		if !revoked {
			log.Printf("auth: WARNING %s left the group that grants superadmin but is the only superadmin; the role is kept — add another superadmin, then sign in again to have it removed", u.Name)
		} else {
			s.warnIfNoAdminCanSignIn(ctx)
		}
		return revoked, nil
	case store.ProjectAdmin, store.WorkflowAdmin, store.WorkflowRunner:
		scope := store.MemberWorkflow
		if role == store.ProjectAdmin {
			scope = store.MemberProject
		}
		err := s.store.RemoveMember(ctx, scope, name, u.ID)
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return err == nil, err
	}
	return false, errors.New("unknown role " + spec)
}

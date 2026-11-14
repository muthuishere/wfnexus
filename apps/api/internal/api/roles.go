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

// grants loads the caller's member rows once per request.
func (s *Server) grants(r *http.Request) []*store.Member {
	sub, ok := SubjectFrom(r.Context())
	if !ok || sub.User == nil {
		return nil
	}
	if !sub.grantsLoaded {
		sub.grants, _ = s.store.GrantsOf(r.Context(), sub.User.ID)
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

func (s *Server) canAdminProject(r *http.Request, project string) bool {
	return s.isSuper(r) || s.holds(r, store.MemberProject, project, store.ProjectAdmin)
}

func (s *Server) canAdminWorkflow(r *http.Request, wf string) bool {
	return s.canAdminProject(r, s.eng.ProjectFor(wf)) || s.holds(r, store.MemberWorkflow, wf, store.WorkflowAdmin)
}

func (s *Server) canRunWorkflow(r *http.Request, wf string) bool {
	return s.canAdminWorkflow(r, wf) || s.holds(r, store.MemberWorkflow, wf, store.WorkflowRunner)
}

// adminAnywhere: holds an admin role at some scope. Publishing a bundle,
// validating or dry-running a draft and proposing a change are not addressed
// to one workflow, so they need only that.
func (s *Server) adminAnywhere(r *http.Request) bool {
	if s.isSuper(r) {
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
	case !read && (strings.HasPrefix(path, "/api/bundles") || path == "/api/workflows/validate" ||
		path == "/api/dryrun" || strings.HasPrefix(path, "/api/proposals") ||
		(strings.HasPrefix(path, "/api/templates/") && strings.HasSuffix(path, "/copy"))):
		return s.adminAnywhere(r)
	case strings.HasPrefix(path, "/api/workflows/") && !read:
		name, sub := seg(strings.TrimPrefix(path, "/api/workflows/"))
		switch sub {
		case "":
			return (method == http.MethodPut || method == http.MethodDelete) && s.canAdminWorkflow(r, name)
		case "memory", "expose", "members":
			return s.canAdminWorkflow(r, name)
		case "runs", "dryrun", "dispatches":
			return s.canRunWorkflow(r, name)
		}
	case strings.HasPrefix(path, "/api/projects/"):
		name, sub := seg(strings.TrimPrefix(path, "/api/projects/"))
		switch {
		case read:
			return sub == "env" && s.canAdminProject(r, name)
		case sub == "" && method == http.MethodPatch, sub == "env", sub == "expose", sub == "members":
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

// runVisible: a superadmin sees every run; anyone else the runs it started
// and the runs of workflows (or projects) it holds a role on.
func (s *Server) runVisible(r *http.Request, run *store.Run) bool {
	if s.isSuper(r) {
		return true
	}
	sub, _ := SubjectFrom(r.Context())
	return (sub != nil && run.TriggeredBy == sub.Name) || s.canRunWorkflow(r, run.Workflow) || s.canAdminProject(r, run.Project)
}

// visibility is runVisible as a listing filter; nil for a superadmin.
func (s *Server) visibility(r *http.Request) *store.Visibility {
	if s.isSuper(r) {
		return nil
	}
	sub, _ := SubjectFrom(r.Context())
	v := &store.Visibility{TriggeredBy: sub.Name}
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
	if err != nil {
		notFound(w)
		return
	}
	if err := s.store.SetMember(r.Context(), scope, name, u.ID, role); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"scope": scope, "name": name, "user": u.Name, "role": role})
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

// revokeSpec takes away what grantSpec gives for the same spec.
func (s *Server) revokeSpec(ctx context.Context, u *store.User, spec string) error {
	role, name, _ := strings.Cut(strings.TrimSpace(spec), ":")
	role = normalizeRole(role)
	switch role {
	case "":
		return nil
	case RoleSuperadmin:
		if u.Role != RoleSuperadmin {
			return nil
		}
		// The host keeps an administrator: a group change never takes the
		// last one away. It is logged for someone to sort out instead.
		if n, err := s.store.CountAdminsExcept(ctx, u.Name); err != nil {
			return err
		} else if n == 0 {
			log.Printf("auth: WARNING %s left the group that grants superadmin but is the only superadmin; the role is kept — add another superadmin, then sign in again to have it removed", u.Name)
			return nil
		}
		return s.store.SetUserRole(ctx, u.Name, "")
	case store.ProjectAdmin:
		return s.store.RemoveMember(ctx, store.MemberProject, name, u.ID)
	case store.WorkflowAdmin, store.WorkflowRunner:
		return s.store.RemoveMember(ctx, store.MemberWorkflow, name, u.ID)
	}
	return errors.New("unknown role " + spec)
}

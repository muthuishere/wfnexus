package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// What a subject may do (ADR 0017).
//
// AUTHORIZATION IS NEVER DELEGATED. A request may carry any provider assertion
// it likes — an OIDC id_token, a group claim, an `X-` header naming a role —
// and none of it is read here. Authentication says WHO; this file says WHAT,
// out of the local `roles` table, and a role revoked locally stops working on
// the next request whatever the provider still asserts.

// The permission vocabulary. It is named from the routes that EXIST rather
// than invented, and it is stored in roles.permissions — the role set is rows,
// never a set baked into the binary.
const (
	PermPublic = "public" // how a subject is obtained, or a liveness check

	PermWorkflowsRead  = "workflows:read"
	PermWorkflowsWrite = "workflows:write"
	PermRunsRead       = "runs:read"
	PermRunsWrite      = "runs:write"
	PermRegistryRead   = "registry:read"
	PermRegistryWrite  = "registry:write"
	PermProjectsRead   = "projects:read"
	PermProjectsWrite  = "projects:write"
	PermEnvRead        = "env:read"
	PermEnvWrite       = "env:write"
	PermStateRead      = "state:read"
	PermStateWrite     = "state:write"
	PermWorkersRead    = "workers:read"
	PermWorkersWrite   = "workers:write"
	PermBundlesRead    = "bundles:read"
	PermPublish        = "publish"
	PermAdminRead      = "admin:read"
	PermAdminWrite     = "admin:write"
)

// DefaultRoles is what first boot SEEDS — as rows, editable afterwards. It is
// a seed, not the authority: the authority is whatever the table holds when a
// request arrives.
func DefaultRoles() map[string][]string {
	viewer := []string{PermWorkflowsRead, PermRunsRead, PermRegistryRead, PermProjectsRead,
		PermEnvRead, PermStateRead, PermWorkersRead, PermBundlesRead}
	publisher := append(append([]string{}, viewer...),
		PermWorkflowsWrite, PermRunsWrite, PermStateWrite, PermPublish)
	return map[string][]string{
		"admin":     {"*"},
		"publisher": publisher,
		"viewer":    viewer,
	}
}

// resourcePerms maps the first path segment under /api to its read and write
// permission. Two permissions per resource, read on GET and write on anything
// else, is the whole rule — a per-route vocabulary would be a list nobody
// keeps current, and a route that fell off it would silently be unguarded.
var resourcePerms = map[string][2]string{
	"workflows":   {PermWorkflowsRead, PermWorkflowsWrite},
	"dryrun":      {PermWorkflowsRead, PermWorkflowsRead},
	"templates":   {PermWorkflowsRead, PermWorkflowsWrite},
	"runs":        {PermRunsRead, PermRunsWrite},
	"skills":      {PermRegistryRead, PermRegistryWrite},
	"tools":       {PermRegistryRead, PermRegistryWrite},
	"mcp":         {PermRegistryRead, PermRegistryWrite},
	"providers":   {PermRegistryRead, PermRegistryWrite},
	"classifiers": {PermRegistryRead, PermRegistryWrite},
	"registries":  {PermRegistryRead, PermRegistryWrite},
	"models":      {PermRegistryRead, PermRegistryWrite},
	"doctor":      {PermRegistryRead, PermRegistryWrite},
	"sources":     {PermProjectsRead, PermProjectsWrite},
	"projects":    {PermProjectsRead, PermProjectsWrite},
	"env":         {PermEnvRead, PermEnvWrite},
	"state":       {PermStateRead, PermStateWrite},
	"workers":     {PermWorkersRead, PermWorkersWrite},
	"bundles":     {PermBundlesRead, PermPublish},
	"users":       {PermAdminRead, PermAdminWrite},
	"roles":       {PermAdminRead, PermAdminWrite},
	// How a subject is obtained, and what it may always ask about itself.
	"device":  {PermPublic, PermPublic},
	"health":  {PermPublic, PermPublic},
	"whoami":  {PermPublic, PermPublic},
	"tokens":  {PermPublic, PermPublic},
	"install": {PermRegistryRead, PermRegistryWrite},
}

// PermissionFor names the permission a request needs. An empty string means no
// permission is defined for that route, which the route table test treats as a
// failure — a route nobody named is a route nobody guarded.
func PermissionFor(method, path string) string {
	seg := strings.Trim(strings.TrimPrefix(path, "/api"), "/")
	if i := strings.IndexByte(seg, '/'); i >= 0 {
		seg = seg[:i]
	}
	p, ok := resourcePerms[seg]
	if !ok {
		return ""
	}
	if method == http.MethodGet || method == http.MethodHead {
		return p[0]
	}
	return p[1]
}

// can answers whether a subject holds a permission. A NIL subject is only
// reachable on a loopback bind, where there is no identity to authorize and the
// machine is the trust boundary — so the check is not reached rather than
// waived for a principal.
func (s *Server) can(r *http.Request, perm string) bool {
	if perm == PermPublic {
		return true
	}
	sub, ok := SubjectFrom(r.Context())
	if !ok {
		return s.loopbackOnly
	}
	// A worker is a machine claiming jobs; authWorker on the four worker routes
	// is its authorization and this table is not about it.
	if sub.Kind == SubjectWorker {
		return true
	}
	// Read from the TABLE on every decision. No cache, so an admin editing a
	// role changes the next request's outcome — which is what "takes effect on
	// the next authorization decision" means.
	perms, err := s.store.Role(r.Context(), sub.Role)
	if err != nil {
		return false
	}
	for _, p := range perms {
		if p == "*" || p == perm {
			return true
		}
	}
	return false
}

// authorize is the middleware half: it refuses before the handler runs.
func (s *Server) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		perm := PermissionFor(r.Method, r.URL.Path)
		if perm == "" {
			// A route with no named permission is refused, not allowed. The
			// table test exists so this never fires in practice; if it does,
			// the fail-closed direction is the only defensible one.
			writeErr(w, http.StatusForbidden, errors.New("forbidden"))
			return
		}
		if !s.can(r, perm) {
			writeErr(w, http.StatusForbidden, fmt.Errorf("forbidden: this subject does not hold %s", perm))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- project scope ----

// errNotFound is the ONE answer a request outside scope gets. A scoped subject
// must not be able to learn that project B's workflow exists by the difference
// between "forbidden" and "not found" — so out-of-scope and absent are the same
// status and the same body, here, in one place.
var errNotFound = errors.New("not found")

func notFound(w http.ResponseWriter) { writeErr(w, http.StatusNotFound, errNotFound) }

// scopeOf is the project a subject is confined to. Empty means every project
// the role allows, which is what an unscoped token and a loopback request both
// resolve to.
func (s *Server) scopeOf(r *http.Request) string {
	sub, ok := SubjectFrom(r.Context())
	if !ok {
		return ""
	}
	return sub.Project
}

// inScope reports whether a resource's project is within the caller's scope.
func (s *Server) inScope(r *http.Request, project string) bool {
	scope := s.scopeOf(r)
	return scope == "" || scope == project
}

// workflowInScope is the gate every workflow-addressed route goes through. It
// returns false having ALREADY answered, so a caller cannot forget to.
func (s *Server) workflowInScope(w http.ResponseWriter, r *http.Request, name string) bool {
	if s.inScope(r, s.eng.ProjectFor(name)) {
		return true
	}
	notFound(w)
	return false
}

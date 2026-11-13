package api

// Provisioning (plan 18, W2): a platform that hosts projects for its own
// customers creates a project, mints a service token confined to it, revokes
// that token, and deletes the project.
//
//	POST   /api/projects {name, create: true, serviceToken: true, tokenLabel}
//	                                             project + token in one call
//	GET    /api/projects/{name}/tokens           active tokens (never a value)
//	POST   /api/projects/{name}/tokens {label}   mint one; the value is shown once
//	DELETE /api/projects/{name}/tokens/{id}      revoke one
//	DELETE /api/projects/{name}                  delete; its tokens are revoked
//
// A service token (wfxp_…) is a SubjectService: project_admin of its one
// project, and every route outside that project answers 404.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// createdProject is a project as created, with its service token when one
// was asked for.
type createdProject struct {
	*engine.Project
	ServiceToken *issuedProjectToken `json:"serviceToken,omitempty"`
}

type issuedProjectToken struct {
	store.ProjectToken
	// Token is the value, present only in the answer that minted it.
	Token string `json:"token"`
}

// serviceTarget is the project a request addresses, for confining a service
// token. ok is false for a route that addresses no single project.
func (s *Server) serviceTarget(r *http.Request) (string, bool) {
	sub, _ := SubjectFrom(r.Context())
	path := r.URL.EscapedPath()
	first := func(rest string) string {
		seg, _, _ := strings.Cut(rest, "/")
		v, err := url.PathUnescape(seg)
		if err != nil {
			return ""
		}
		return v
	}
	switch {
	case path == "/api/whoami":
		return sub.Project, true
	case path == "/api/runs" && r.Method == http.MethodGet:
		return sub.Project, true // filtered to what the subject may see
	case strings.HasPrefix(path, "/api/projects/"):
		return first(strings.TrimPrefix(path, "/api/projects/")), true
	case strings.HasPrefix(path, "/api/workflows/"):
		name := first(strings.TrimPrefix(path, "/api/workflows/"))
		if s.eng.Definitions()[name] == nil {
			return "", false
		}
		return s.eng.ProjectFor(name), true
	case strings.HasPrefix(path, "/api/runs/"):
		id, err := uuid.Parse(first(strings.TrimPrefix(path, "/api/runs/")))
		if err != nil {
			return "", false
		}
		run, err := s.store.GetRun(r.Context(), id)
		if err != nil {
			return "", false
		}
		return run.Project, true
	}
	return "", false
}

func (s *Server) listProjectTokens(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.projectKnown(r, name) {
		notFound(w)
		return
	}
	out, err := s.store.ListProjectTokens(r.Context(), name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) mintProjectToken(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.projectKnown(r, name) {
		notFound(w)
		return
	}
	// A service token does not mint further tokens: the platform that
	// provisioned the project does.
	if sub, ok := SubjectFrom(r.Context()); ok && sub.Kind == SubjectService {
		writeErr(w, http.StatusForbidden, errors.New("forbidden: a service token cannot mint tokens"))
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	t, secret, err := s.store.IssueProjectToken(r.Context(), name, body.Label)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 201, issuedProjectToken{ProjectToken: t, Token: secret})
}

func (s *Server) revokeProjectToken(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.projectKnown(r, name) {
		notFound(w)
		return
	}
	if err := s.store.RevokeProjectToken(r.Context(), name, urlName(r, "id")); err != nil {
		if errors.Is(err, store.ErrNoToken) {
			notFound(w)
			return
		}
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// projectKnown: the project exists and is within the caller's scope.
func (s *Server) projectKnown(r *http.Request, name string) bool {
	if !s.inScope(r, name) {
		return false
	}
	_, err := s.eng.Project(r.Context(), name)
	return err == nil
}

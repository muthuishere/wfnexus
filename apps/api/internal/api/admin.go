package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// Administration: users and roles as ROWS (ADR 0017).
//
// The one invariant these handlers defend is that the host keeps an
// administrator. Everything else here is CRUD.

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, users)
}

// listPendingLinks is GET /api/users/links: accounts linked on their first
// sign-in after the upgrade that held roles, waiting for confirmation.
func (s *Server) listPendingLinks(w http.ResponseWriter, r *http.Request) {
	links, err := s.store.PendingLinks(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, links)
}

// confirmLink is POST /api/users/{name}/link/confirm (a superadmin; the
// server-side `wfx-server link confirm NAME` does the same with no sign-in).
func (s *Server) confirmLink(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if err := s.store.ConfirmLink(r.Context(), name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			notFound(w)
			return
		}
		writeErr(w, 500, err)
		return
	}
	log.Printf("auth: AUDIT account link for %s confirmed by %s", name, actorOf(r, stepBody{}).ID)
	writeJSON(w, 200, map[string]any{"ok": true})
}

type userBody struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Project     string `json:"project"`
	Disabled    bool   `json:"disabled"`
}

// createUser mints the new subject's first credential and returns it ONCE.
// There is no route that reads it back: only the hash is stored.
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var b userBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, 400, err)
		return
	}
	if b.Name == "" {
		writeErr(w, 400, errors.New("`name` is required"))
		return
	}
	if !globalRoleOK(b.Role) {
		writeErr(w, 400, fmt.Errorf("role %q: the only global role is superadmin (or none); grant the others with `wfx member add`", b.Role))
		return
	}
	u := &store.User{Name: b.Name, DisplayName: b.DisplayName, Role: b.Role, Project: b.Project}
	if err := s.store.CreateUser(r.Context(), u); err != nil {
		writeErr(w, 400, err)
		return
	}
	// No credential is minted here: the person signs in (password reset,
	// a login provider, or the device grant) and gets a JWT (ADR 0028).
	writeJSON(w, 201, map[string]any{"user": u})
}

// updateUser refuses the change that would leave the host with no
// administrator. Demotion and disabling are both that change.
func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	var b userBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, 400, err)
		return
	}
	if !globalRoleOK(b.Role) {
		writeErr(w, 400, fmt.Errorf("role %q: the only global role is superadmin (or none)", b.Role))
		return
	}
	cur, err := s.store.UserByName(r.Context(), name)
	if err != nil {
		notFound(w)
		return
	}
	losingAdmin := cur.Role == RoleSuperadmin && !cur.Disabled && (b.Role != RoleSuperadmin || b.Disabled)
	if losingAdmin {
		if ok, err := s.anotherAdminRemains(r, name); err != nil {
			writeErr(w, 500, err)
			return
		} else if !ok {
			writeErr(w, 409, errors.New("refused: this is the last administrator"))
			return
		}
	}
	u, err := s.store.UpdateUser(r.Context(), name, b.DisplayName, b.Role, b.Project, b.Disabled)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	cur, err := s.store.UserByName(r.Context(), name)
	if err != nil {
		notFound(w)
		return
	}
	if cur.Role == RoleSuperadmin && !cur.Disabled {
		if ok, err := s.anotherAdminRemains(r, name); err != nil {
			writeErr(w, 500, err)
			return
		} else if !ok {
			writeErr(w, 409, errors.New("refused: this is the last administrator"))
			return
		}
	}
	if err := s.store.DeleteUser(r.Context(), name); err != nil {
		notFound(w)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) anotherAdminRemains(r *http.Request, name string) (bool, error) {
	n, err := s.store.CountAdminsExcept(r.Context(), name)
	return n > 0, err
}

// globalRoleOK: users.role is superadmin or nothing (ADR 0028).
func globalRoleOK(role string) bool { return role == "" || role == RoleSuperadmin }

// listRoles names the four fixed roles. They are not editable.
func (s *Server) listRoles(w http.ResponseWriter, r *http.Request) {
	out := []*store.Role{}
	for _, n := range []string{RoleSuperadmin, store.ProjectAdmin, store.WorkflowAdmin, store.WorkflowRunner} {
		out = append(out, &store.Role{Name: n, Permissions: DefaultRoles()[n]})
	}
	writeJSON(w, 200, out)
}

// saveRole and deleteRole: the roles are fixed (ADR 0028).
func (s *Server) saveRole(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusGone, errors.New("roles are fixed: superadmin, project_admin, workflow_admin, workflow_runner"))
}

func (s *Server) deleteRole(w http.ResponseWriter, r *http.Request) { s.saveRole(w, r) }

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
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
	if b.Name == "" || b.Role == "" {
		writeErr(w, 400, errors.New("`name` and `role` are required"))
		return
	}
	if _, err := s.store.Role(r.Context(), b.Role); err != nil {
		writeErr(w, 400, fmt.Errorf("no such role %q", b.Role))
		return
	}
	u := &store.User{Name: b.Name, DisplayName: b.DisplayName, Role: b.Role, Project: b.Project}
	if err := s.store.CreateUser(r.Context(), u); err != nil {
		writeErr(w, 400, err)
		return
	}
	value := auth.NewToken()
	if _, err := s.store.IssueUserToken(r.Context(), u.ID, auth.HashToken(value), "created", b.Project); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 201, map[string]any{"user": u, "token": value})
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
	cur, err := s.store.UserByName(r.Context(), name)
	if err != nil {
		notFound(w)
		return
	}
	losingAdmin := cur.Role == "admin" && !cur.Disabled && (b.Role != "admin" || b.Disabled)
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
	if cur.Role == "admin" && !cur.Disabled {
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

func (s *Server) listRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.store.ListRoles(r.Context())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, roles)
}

func (s *Server) saveRole(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Permissions []string `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, 400, err)
		return
	}
	name := urlName(r, "name")
	if err := s.store.UpsertRole(r.Context(), name, b.Permissions); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, &store.Role{Name: name, Permissions: b.Permissions})
}

// deleteRole removes a role NO SUBJECT HOLDS. Deleting one somebody holds
// would leave every one of their requests refused for a reason nobody chose.
func (s *Server) deleteRole(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	n, err := s.store.CountUsersWithRole(r.Context(), name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if n > 0 {
		writeErr(w, 409, fmt.Errorf("refused: %d user(s) still hold the role %q", n, name))
		return
	}
	if err := s.store.DeleteRole(r.Context(), name); err != nil {
		notFound(w)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

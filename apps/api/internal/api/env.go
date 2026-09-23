package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// The env store's surface. A secret goes IN and is never served back out: the
// listing returns names, and the only path a value takes out of the database is
// into the process about to run a step.

type envBody struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Secret defaults to true. Ordinary configuration must opt out, which is
	// the safe direction to be wrong in.
	Secret *bool `json:"secret,omitempty"`
}

func (b envBody) isSecret() bool { return b.Secret == nil || *b.Secret }

func (s *Server) listSystemEnv(w http.ResponseWriter, r *http.Request) {
	s.writeEnv(w, r, store.ScopeSystem, "")
}

func (s *Server) setSystemEnv(w http.ResponseWriter, r *http.Request) {
	s.putEnv(w, r, store.ScopeSystem, "")
}

func (s *Server) deleteSystemEnv(w http.ResponseWriter, r *http.Request) {
	s.removeEnv(w, r, store.ScopeSystem, "", chi.URLParam(r, "key"))
}

func (s *Server) listProjectEnv(w http.ResponseWriter, r *http.Request) {
	s.writeEnv(w, r, store.ScopeProject, urlName(r, "name"))
}

func (s *Server) setProjectEnv(w http.ResponseWriter, r *http.Request) {
	s.putEnv(w, r, store.ScopeProject, urlName(r, "name"))
}

func (s *Server) deleteProjectEnv(w http.ResponseWriter, r *http.Request) {
	s.removeEnv(w, r, store.ScopeProject, urlName(r, "name"), chi.URLParam(r, "key"))
}

func (s *Server) writeEnv(w http.ResponseWriter, r *http.Request, scope, name string) {
	vars, err := s.eng.ListEnvVars(r.Context(), scope, name)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope": scope, "scopeName": name, "vars": vars,
		// Where the key came from — a variable name or a path, never the key.
		"keySource": s.eng.SecretsSource(),
	})
}

func (s *Server) putEnv(w http.ResponseWriter, r *http.Request, scope, name string) {
	var b envBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.eng.SetEnvVar(r.Context(), scope, name, b.Key, b.Value, b.isSecret()); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Deliberately not echoing the value back. A write confirmation carrying
	// the secret is how it ends up in a browser's network log.
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key": b.Key, "secret": b.isSecret()})
}

func (s *Server) removeEnv(w http.ResponseWriter, r *http.Request, scope, name, key string) {
	if key == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("which variable?"))
		return
	}
	if err := s.eng.DeleteEnvVar(r.Context(), scope, name, key); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// The state store's surface — what a workflow remembers between runs.
//
// TWO DOORS, on purpose.
//
//   - /api/state is the OPERATOR's door: it names a scope and the scope's owner
//     outright, because a person at a terminal or the UI is allowed to look at
//     and fix any namespace.
//   - /api/runs/{id}/state is the RUNNING STEP's door: it names a scope and
//     nothing else. The workflow name and the project are read from the run row
//     here on the server, so a step cannot reach another repository's state by
//     claiming to belong to it.
//
// Unlike env, a value comes straight back out again: state is plaintext by
// design and there is nothing to withhold. It is not a secret store.

type stateBody struct {
	Scope  string `json:"scope"`
	Name   string `json:"name,omitempty"`
	StepID string `json:"stepId,omitempty"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

func (s *Server) listState(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")
	vars, err := s.eng.ListState(r.Context(), scope, r.URL.Query().Get("name"))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "scopeName": r.URL.Query().Get("name"), "vars": vars})
}

func (s *Server) setState(w http.ResponseWriter, r *http.Request) {
	var b stateBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.eng.SetState(r.Context(), b.Scope, b.Name, b.Key, b.Value); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scope": b.Scope, "key": b.Key})
}

func (s *Server) deleteState(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if key == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("which key?"))
		return
	}
	if err := s.eng.DeleteState(r.Context(), r.URL.Query().Get("scope"), r.URL.Query().Get("name"), key); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// workflowState is what the workflow page shows: this workflow's memory, each
// of its steps', and the global namespace.
func (s *Server) workflowState(w http.ResponseWriter, r *http.Request) {
	vars, err := s.eng.WorkflowState(r.Context(), urlName(r, "name"))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"vars": vars})
}

// ---- the running step's door ----

func (s *Server) runStateAddress(r *http.Request, scope, stepID string) (string, string, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return "", "", err
	}
	name, err := s.eng.RunStateAddress(r.Context(), id, scope, stepID)
	return scope, name, err
}

func (s *Server) listRunState(w http.ResponseWriter, r *http.Request) {
	scope, name, err := s.runStateAddress(r, r.URL.Query().Get("scope"), r.URL.Query().Get("stepId"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	vars, err := s.eng.ListState(r.Context(), scope, name)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": scope, "scopeName": name, "vars": vars})
}

func (s *Server) setRunState(w http.ResponseWriter, r *http.Request) {
	var b stateBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	scope, name, err := s.runStateAddress(r, b.Scope, b.StepID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.eng.SetState(r.Context(), scope, name, b.Key, b.Value); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scope": scope, "scopeName": name, "key": b.Key})
}

func (s *Server) deleteRunState(w http.ResponseWriter, r *http.Request) {
	scope, name, err := s.runStateAddress(r, r.URL.Query().Get("scope"), r.URL.Query().Get("stepId"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.eng.DeleteState(r.Context(), scope, name, chi.URLParam(r, "key")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

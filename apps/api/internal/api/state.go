package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
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

// orgStateWrite is, for a person in an organization, the check authorize
// leaves to the operator's door because the body names the namespace (#189):
// another tenant's (or the global) namespace is a 404, and a write needs an
// admin of the workflow or project it belongs to. Anyone else got here
// through authorize exactly as before, and it says yes.
func (s *Server) orgStateWrite(w http.ResponseWriter, r *http.Request, scope, name string) bool {
	if !orgMember(r) {
		return true
	}
	project := s.stateProject(scope, name)
	if project == "" || !s.tenantVisible(r, project) {
		notFound(w)
		return false
	}
	if !s.tenantWritable(r, project) {
		writeErr(w, http.StatusForbidden, fmt.Errorf("forbidden: this subject does not hold %s", PermStateWrite))
		return false
	}
	ok := s.canAdminProject(r, project)
	switch scope {
	case model.StateScopeWorkflow:
		ok = ok || s.canAdminWorkflow(r, name)
	case model.StateScopeStep:
		if i := strings.LastIndexByte(name, '/'); i > 0 {
			ok = ok || s.canAdminWorkflow(r, name[:i])
		}
	}
	if !ok {
		writeErr(w, http.StatusForbidden, fmt.Errorf("forbidden: this subject does not hold %s", PermStateWrite))
		return false
	}
	return true
}

func (s *Server) setState(w http.ResponseWriter, r *http.Request) {
	var b stateBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !s.orgStateWrite(w, r, b.Scope, b.Name) {
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
	if !s.orgStateWrite(w, r, r.URL.Query().Get("scope"), r.URL.Query().Get("name")) {
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
	all, err := s.eng.WorkflowState(r.Context(), urlName(r, "name"))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	// The global namespace is the server's, not a tenant's (#189): it is
	// shown only to whoever sees the default tenant.
	vars := []model.StateVar{}
	for _, v := range all {
		if v.Scope != model.StateScopeGlobal || s.tenantVisible(r, "") {
			vars = append(vars, v)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"vars": vars})
}

// workflowSchedule answers "when does this run next?" for the workflow page:
// the next cron fire (UTC), or null for a workflow nobody schedules.
func (s *Server) workflowSchedule(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	def := s.eng.Definitions()[name]
	crons := []string{}
	if def != nil {
		for _, sc := range def.On.Schedule {
			crons = append(crons, sc.Cron)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"nextRunAt": s.eng.NextRunAt(name, time.Now()), "crons": crons})
}

// ---- the running step's door ----

// runStateAddress resolves the namespace against the run, after the run
// check every other run-addressed route makes (runID): a run the caller may
// not see is a 404 here too. ok is false when it has already answered.
func (s *Server) runStateAddress(w http.ResponseWriter, r *http.Request, scope, stepID string) (string, string, bool) {
	id, ok := s.runID(w, r)
	if !ok {
		return "", "", false
	}
	name, err := s.eng.RunStateAddress(r.Context(), id, scope, stepID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return "", "", false
	}
	// The global namespace is the server's (#189): a run of an
	// organization's does not reach it through this door either.
	if !s.tenantVisible(r, s.stateProject(scope, name)) {
		notFound(w)
		return "", "", false
	}
	return scope, name, true
}

func (s *Server) listRunState(w http.ResponseWriter, r *http.Request) {
	scope, name, ok := s.runStateAddress(w, r, r.URL.Query().Get("scope"), r.URL.Query().Get("stepId"))
	if !ok {
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
	scope, name, ok := s.runStateAddress(w, r, b.Scope, b.StepID)
	if !ok {
		return
	}
	if err := s.eng.SetState(r.Context(), scope, name, b.Key, b.Value); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "scope": scope, "scopeName": name, "key": b.Key})
}

func (s *Server) deleteRunState(w http.ResponseWriter, r *http.Request) {
	scope, name, ok := s.runStateAddress(w, r, r.URL.Query().Get("scope"), r.URL.Query().Get("stepId"))
	if !ok {
		return
	}
	if err := s.eng.DeleteState(r.Context(), scope, name, chi.URLParam(r, "key")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

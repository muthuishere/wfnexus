package api

import (
	"encoding/json"
	"net/http"
)

// Workflow memory (engine/memory.go): the workflow's toolnexus memory home —
// MEMORY.md index, memory/<slug>.md notes, the dream log. Reached through the
// workflow; `?project=` picks the project (default local); `?target=` is
// index | dream | note:<slug>.

func (s *Server) workflowMemory(w http.ResponseWriter, r *http.Request) {
	v, err := s.eng.WorkflowMemory(r.Context(), urlName(r, "name"), r.URL.Query().Get("project"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) forgetWorkflowMemory(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DeleteMemory(urlName(r, "name"), r.URL.Query().Get("project"), ""); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) readMemoryFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	body, err := s.eng.ReadMemory(urlName(r, "name"), q.Get("project"), q.Get("target"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"target": q.Get("target"), "body": body})
}

func (s *Server) writeMemoryFile(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	q := r.URL.Query()
	if err := s.eng.WriteMemory(r.Context(), urlName(r, "name"), q.Get("project"), q.Get("target"), b.Body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) deleteMemoryFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("target") == "" {
		writeErr(w, http.StatusBadRequest, errString("which file? ?target=index|dream|note:<slug>"))
		return
	}
	if err := s.eng.DeleteMemory(urlName(r, "name"), q.Get("project"), q.Get("target")); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// dreamWorkflowMemory runs one dream pass now (synchronously).
func (s *Server) dreamWorkflowMemory(w http.ResponseWriter, r *http.Request) {
	res, err := s.eng.Dream(r.Context(), urlName(r, "name"), r.URL.Query().Get("project"))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "result": res})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type errString string

func (e errString) Error() string { return string(e) }

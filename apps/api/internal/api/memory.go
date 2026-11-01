package api

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Workflow memory (engine/memory.go): what previous runs of a workflow left
// behind. Reached through the workflow — there is no global list, matching the
// project → workflow drill-down. `?project=` narrows to one project.

func (s *Server) workflowMemory(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	entries, err := s.eng.WorkflowMemory(r.Context(), name, r.URL.Query().Get("project"))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	resp := map[string]any{"workflow": name, "entries": entries}
	if def := s.eng.Definitions()[name]; def != nil {
		resp["preview"] = s.eng.PreviewMemory(r.Context(), def)
		resp["enabled"] = def.Memory == nil || !def.Memory.Off
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) forgetWorkflowMemory(w http.ResponseWriter, r *http.Request) {
	n, err := s.eng.ForgetMemory(r.Context(), urlName(r, "name"), r.URL.Query().Get("project"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": n})
}

func (s *Server) deleteMemoryEntry(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("which entry?"))
		return
	}
	if err := s.eng.DeleteMemoryEntry(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

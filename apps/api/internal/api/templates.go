package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The gallery. A template is an ordinary workflow carrying `template:`, so
// there is no separate store, no template language and no second validator —
// which is why a template that would not run cannot reach the gallery.

func (s *Server) listTemplates(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.eng.Templates())
}

// getTemplate returns the whole definition, which is what the builder opens.
func (s *Server) getTemplate(w http.ResponseWriter, r *http.Request) {
	def, err := s.eng.Template(urlName(r, "name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, def)
}

// copyTemplate writes the author's own copy. It goes through the same save
// path as any other workflow, so the copy is validated against THIS machine
// before it exists — a template naming a skill you do not have is refused now,
// with the name of the skill, rather than at the next reload.
func (s *Server) copyTemplate(w http.ResponseWriter, r *http.Request) {
	s.copy(w, r, s.eng.CopyTemplate)
}

// copyWorkflow is the same act without the template requirement: copying an
// ordinary workflow out of another repository into your own. The gallery route
// stays exactly as it was — it is the same code with one extra check.
func (s *Server) copyWorkflow(w http.ResponseWriter, r *http.Request) {
	s.copy(w, r, s.eng.CopyWorkflow)
}

type copyFn func(ctx context.Context, name, as string) (*workflow.Definition, error)

func (s *Server) copy(w http.ResponseWriter, r *http.Request, take copyFn) {
	var body struct {
		As string `json:"as"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	def, err := take(r.Context(), urlName(r, "name"), body.As)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	path, err := s.eng.SaveWorkflow(def)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("the copy does not load here: %w", err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true, "name": def.Name, "path": path,
		"definition": s.eng.Definitions()[def.Name],
	})
}

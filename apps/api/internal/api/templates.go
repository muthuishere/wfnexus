package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The gallery. A template is an ordinary workflow carrying `template:`, so
// there is no separate store, no template language and no second validator —
// which is why a template that would not run cannot reach the gallery.

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	// The shipped templates and the caller's tenant's (#189).
	out := []engine.Template{}
	for _, t := range s.eng.Templates() {
		src := t.Source
		if src == "" {
			src = "local"
		}
		if s.tenantVisible(r, src) {
			out = append(out, t)
		}
	}
	writeJSON(w, http.StatusOK, out)
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
		// The project the copy goes into — the one the gallery was opened
		// from. Empty means the platform's own directory, as before.
		Project string `json:"project"`
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
	into := s.resolveProject(r, body.Project)
	if into == "" {
		into = "local"
	}
	// Another tenant's project is a 404 (#189), and a person in an
	// organization writes only where they may save.
	if !s.inScope(r, into) {
		notFound(w)
		return
	}
	if orgMember(r) && !s.canSaveInto(r, into) {
		writeErr(w, http.StatusForbidden, errors.New("forbidden: you may not save workflows into this project"))
		return
	}
	if err := s.orgGuardSave(r, def); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	path, err := s.eng.SaveWorkflowIn(into, def)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("the copy does not load here: %w", err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true, "name": def.Name, "path": path,
		"definition": s.eng.Definitions()[def.Name],
	})
}

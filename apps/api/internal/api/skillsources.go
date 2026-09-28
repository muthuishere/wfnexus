package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
)

// Skill sources: git repositories of skills imported into the registry at a
// branch, tag or commit (engine/skillsources.go). Reads need registry:read,
// everything that changes the registry needs registry:write (authz.go).

func (s *Server) skillSourceRoutes(r chi.Router) {
	r.Get("/skill-sources", s.listSkillSources)
	r.Post("/skill-sources", s.importSkillSource)
	r.Get("/skill-sources/{name}/skills", s.skillSourceSkills)
	r.Get("/skill-sources/{name}/refs", s.skillSourceRefs)
	r.Put("/skill-sources/{name}/ref", s.setSkillSourceRef)
	r.Post("/skill-sources/{name}/refresh", s.refreshSkillSource)
	r.Delete("/skill-sources/{name}", s.removeSkillSource)
}

func (s *Server) listSkillSources(w http.ResponseWriter, _ *http.Request) {
	list, err := s.eng.SkillSources()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, list)
}

func (s *Server) importSkillSource(w http.ResponseWriter, r *http.Request) {
	var body struct {
		engine.SkillSourceSpec
		// Branch is accepted as another spelling of Ref.
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Ref == "" {
		body.Ref = body.Branch
	}
	if body.URL == "" {
		writeErr(w, 400, fmt.Errorf("`url` is required — the git repository to import skills from"))
		return
	}
	rep, err := s.eng.ImportSkillSource(r.Context(), body.SkillSourceSpec)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 201, rep)
}

func (s *Server) setSkillSourceRef(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref    string `json:"ref"`
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Ref == "" {
		body.Ref = body.Branch
	}
	if body.Ref == "" {
		writeErr(w, 400, fmt.Errorf("`ref` is required — a branch, tag or commit"))
		return
	}
	rep, err := s.eng.SetSkillSourceRef(r.Context(), urlName(r, "name"), body.Ref)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, rep)
}

func (s *Server) refreshSkillSource(w http.ResponseWriter, r *http.Request) {
	rep, err := s.eng.RefreshSkillSource(r.Context(), urlName(r, "name"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, rep)
}

func (s *Server) removeSkillSource(w http.ResponseWriter, r *http.Request) {
	rep, err := s.eng.RemoveSkillSource(urlName(r, "name"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, rep)
}

func (s *Server) skillSourceSkills(w http.ResponseWriter, r *http.Request) {
	files, err := s.eng.SkillSourceSkills(urlName(r, "name"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, files)
}

func (s *Server) skillSourceRefs(w http.ResponseWriter, r *http.Request) {
	refs, err := s.eng.SkillSourceRefs(r.Context(), urlName(r, "name"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, refs)
}

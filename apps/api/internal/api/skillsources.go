package api

import (
	"encoding/json"
	"errors"
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
	r.Get("/modules", s.listModules)
	r.Post("/modules/{name}/services", s.startService)
	r.Delete("/modules/{name}/services/{account}", s.stopService)
	r.Get("/modules/{name}/services/{account}/watches", s.listServiceWatches)
	r.Post("/modules/{name}/services/{account}/watches", s.putServiceWatch)
	r.Delete("/modules/{name}/services/{account}/watches/{watch}", s.deleteServiceWatch)
	// The running service's own door: its scoped token, checked here.
	r.Get("/service-self/{name}/{account}/watches", s.selfWatches)
	r.Post("/service-self/{name}/{account}/heartbeat", s.selfHeartbeat)
}

func serviceKey(r *http.Request) string {
	return chi.URLParam(r, "name") + "/" + chi.URLParam(r, "account")
}

func (s *Server) listServiceWatches(w http.ResponseWriter, r *http.Request) {
	ws, err := s.eng.ServiceWatches(r.Context(), serviceKey(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"watches": ws})
}

// putServiceWatch registers (or replaces, by name) one watch. The running
// service reads it on its next tick; no restart.
func (s *Server) putServiceWatch(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	name, err := s.eng.PutServiceWatch(r.Context(), serviceKey(r), raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"name": name})
}

func (s *Server) deleteServiceWatch(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DeleteServiceWatch(r.Context(), serviceKey(r), chi.URLParam(r, "watch")); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) authSelf(w http.ResponseWriter, r *http.Request) bool {
	if err := s.eng.AuthService(r.Context(), serviceKey(r), bearer(r)); err != nil {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthenticated"))
		return false
	}
	return true
}

func (s *Server) selfWatches(w http.ResponseWriter, r *http.Request) {
	if s.authSelf(w, r) {
		s.listServiceWatches(w, r)
	}
}

func (s *Server) selfHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !s.authSelf(w, r) {
		return
	}
	if err := s.eng.ServiceSelfBeat(r.Context(), serviceKey(r)); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// startService records one supervised instance of a service module (#103).
func (s *Server) startService(w http.ResponseWriter, r *http.Request) {
	var spec engine.ServiceSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	spec.Module = chi.URLParam(r, "name")
	got, err := s.eng.EnableService(r.Context(), spec)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, got)
}

func (s *Server) stopService(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DisableService(r.Context(), chi.URLParam(r, "name"), chi.URLParam(r, "account")); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// listModules is every module.yaml in the imported sources, with what each
// still needs configured (ADR 0028). Install/pin/Sync are the source routes.
func (s *Server) listModules(w http.ResponseWriter, r *http.Request) {
	mods, errs := s.eng.ModuleViews(r.Context())
	writeJSON(w, 200, map[string]any{"modules": mods, "errors": errs})
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
	s.versionPackages(r.Context())
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
	s.versionPackages(r.Context())
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

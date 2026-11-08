package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
)

// Module calls, profiles and the outbox (#109; engine/modulecalls.go).
//
//	POST /api/modules/{name}/call                    {inputs, wait}  run <module>.<command>
//	GET  /api/modules/{name}/profiles                list a module's profiles
//	PUT  /api/modules/{name}/profiles/{profile}      {account, inputs, default}
//	POST /api/module-self/{module}/outbox            module token: queue a send on its service
//	GET  /api/module-self/{module}/outbox?service=&id=
//	GET  /api/service-self/{name}/{account}/outbox   service token: what to send
//	POST /api/service-self/{name}/{account}/outbox/{id}  {status, result}
func (s *Server) moduleCallRoutes(r chi.Router) {
	r.Post("/modules/{name}/call", s.callModule)
	r.Get("/modules/{name}/profiles", s.listProfiles)
	r.Put("/modules/{name}/profiles/{profile}", s.putProfile)
	r.Post("/module-self/{module}/outbox", s.moduleEnqueue)
	r.Get("/module-self/{module}/outbox", s.moduleOutboxGet)
	r.Get("/service-self/{name}/{account}/outbox", s.selfOutbox)
	r.Post("/service-self/{name}/{account}/outbox/{id}", s.selfOutboxReport)
}

func (s *Server) callModule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Inputs map[string]any `json:"inputs"`
		Wait   bool           `json:"wait"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	wait := time.Duration(0)
	if body.Wait {
		wait = 10 * time.Minute
	}
	by := actorOf(r, stepBody{Actor: r.Header.Get("X-WFX-Actor")})
	run, out, err := s.eng.CallModuleCommand(r.Context(), chi.URLParam(r, "name"), body.Inputs, by.ID, wait)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"run": run, "output": out})
}

func (s *Server) listProfiles(w http.ResponseWriter, r *http.Request) {
	ps, err := s.eng.Profiles(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": ps})
}

func (s *Server) putProfile(w http.ResponseWriter, r *http.Request) {
	var p engine.Profile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p.Name = chi.URLParam(r, "profile")
	got, err := s.eng.SetProfile(r.Context(), chi.URLParam(r, "name"), p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, got)
}

func (s *Server) authModule(w http.ResponseWriter, r *http.Request) bool {
	if err := s.eng.AuthModule(r.Context(), chi.URLParam(r, "module"), bearer(r)); err != nil {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthenticated"))
		return false
	}
	return true
}

func (s *Server) moduleEnqueue(w http.ResponseWriter, r *http.Request) {
	if !s.authModule(w, r) {
		return
	}
	var body struct {
		Service string          `json:"service"`
		Profile string          `json:"profile"`
		ID      string          `json:"id"`
		Request json.RawMessage `json:"request"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	it, err := s.eng.Enqueue(r.Context(), chi.URLParam(r, "module"), body.Service, body.Profile, body.ID, body.Request)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) moduleOutboxGet(w http.ResponseWriter, r *http.Request) {
	if !s.authModule(w, r) {
		return
	}
	key := r.URL.Query().Get("service")
	if !strings.HasPrefix(key, chi.URLParam(r, "module")+".") {
		writeErr(w, http.StatusForbidden, errors.New("not this module's service"))
		return
	}
	it, err := s.eng.OutboxGet(r.Context(), key, r.URL.Query().Get("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, it)
}

func (s *Server) selfOutbox(w http.ResponseWriter, r *http.Request) {
	if !s.authSelf(w, r) {
		return
	}
	items, err := s.eng.Outbox(r.Context(), serviceKey(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) selfOutboxReport(w http.ResponseWriter, r *http.Request) {
	if !s.authSelf(w, r) {
		return
	}
	var body struct {
		Status string          `json:"status"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.eng.OutboxReport(r.Context(), serviceKey(r), chi.URLParam(r, "id"), body.Status, body.Result); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

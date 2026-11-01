package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/muthuishere/wfnexus/apps/api/internal/carryops"
)

// The carry surface: `wfx tool` and `wfx config` against a context. The server
// drives its own carry binary with its own bucket credentials, so a client
// needs only its wfx token — never S3 keys or the state's encryption key.
//
// A state is the server's (empty → CARRY_STATE, else "wfnexus") or a worker's
// (its name). Every route takes it as ?state= or a body field.
//
// Env values are NOT secrets by doctrine (keys live in the env store), but they
// still never come back in a listing: GET /carry/env returns names, and only
// ?values=1 — the move path `wfx sync` uses — returns the map.

func (s *Server) carryRoutes(r chi.Router) {
	r.Get("/carry", s.carryStatus)
	r.Get("/carry/states", s.carryStates)
	r.Get("/carry/tools", s.carryToolCatalog)
	r.Post("/carry/tools", s.carryToolAdd)
	r.Delete("/carry/tools/{name}", s.carryToolRm)
	r.Get("/carry/files", s.carryLs)
	r.Get("/carry/file", s.carryGet)
	r.Put("/carry/file", s.carryPut)
	r.Delete("/carry/file", s.carryRm)
	r.Get("/carry/env", s.carryEnv)
	r.Put("/carry/env", s.carryEnvSet)
	r.Delete("/carry/env/{key}", s.carryEnvUnset)
	r.Get("/carry/log", s.carryLog)
	r.Post("/carry/rollback", s.carryRollback)
}

// carryFail maps carry's outcomes to HTTP: off is 409 (a state of the server,
// not a bug), a usage/not-found answer is 400/404, a store problem is 502.
func carryFail(w http.ResponseWriter, err error) {
	var ce *carryops.Error
	switch {
	case errors.Is(err, carryops.ErrNotConfigured), errors.Is(err, carryops.ErrNotInstalled):
		writeErr(w, http.StatusConflict, err)
	case errors.As(err, &ce):
		code := http.StatusBadGateway
		switch ce.Code {
		case "not_found":
			code = http.StatusNotFound
		case "usage", "readonly", "invalid":
			code = http.StatusBadRequest
		case "conflict":
			code = http.StatusConflict
		}
		writeErr(w, code, err)
	default:
		writeErr(w, http.StatusBadRequest, err)
	}
}

func carryOK(w http.ResponseWriter, v map[string]any, err error) {
	if err != nil {
		carryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) carryStatus(w http.ResponseWriter, _ *http.Request) {
	c := carryops.FromEnv()
	out := map[string]any{"configured": c.Configured, "state": c.DefaultState, "ready": true}
	if err := c.Ready(); err != nil {
		out["ready"] = false
		out["reason"] = err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) carryStates(w http.ResponseWriter, r *http.Request) {
	v, err := carryops.FromEnv().States(r.Context())
	carryOK(w, v, err)
}

func (s *Server) carryToolCatalog(w http.ResponseWriter, r *http.Request) {
	v, err := carryops.FromEnv().ToolCatalog(r.Context(), r.URL.Query().Get("name"))
	carryOK(w, v, err)
}

type carryToolBody struct {
	State   string `json:"state"`
	Tool    string `json:"tool"`
	Channel string `json:"channel"`
}

func (s *Server) carryToolAdd(w http.ResponseWriter, r *http.Request) {
	var b carryToolBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Tool == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("body needs {\"tool\": \"name[@version]\"}"))
		return
	}
	v, err := carryops.FromEnv().ToolAdd(r.Context(), b.State, b.Tool, b.Channel)
	carryOK(w, v, err)
}

func (s *Server) carryToolRm(w http.ResponseWriter, r *http.Request) {
	v, err := carryops.FromEnv().ToolRm(r.Context(), r.URL.Query().Get("state"), chi.URLParam(r, "name"))
	carryOK(w, v, err)
}

func (s *Server) carryLs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := carryops.FromEnv().Ls(r.Context(), q.Get("state"), q.Get("dir"))
	carryOK(w, v, err)
}

func (s *Server) carryGet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	data, err := carryops.FromEnv().Get(r.Context(), q.Get("state"), q.Get("path"))
	if err != nil {
		carryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": q.Get("path"), "size": len(data), "content_base64": base64.StdEncoding.EncodeToString(data)})
}

type carryPutBody struct {
	State         string `json:"state"`
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
	Mode          int    `json:"mode"`
}

func (s *Server) carryPut(w http.ResponseWriter, r *http.Request) {
	var b carryPutBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Path == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("body needs {\"path\", \"content_base64\"}"))
		return
	}
	data, err := base64.StdEncoding.DecodeString(b.ContentBase64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("content_base64: %w", err))
		return
	}
	v, err := carryops.FromEnv().Put(r.Context(), b.State, b.Path, data, b.Mode)
	carryOK(w, v, err)
}

func (s *Server) carryRm(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	v, err := carryops.FromEnv().Rm(r.Context(), q.Get("state"), q.Get("path"))
	carryOK(w, v, err)
}

func (s *Server) carryEnv(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c := carryops.FromEnv()
	state := c.State(q.Get("state"))
	if q.Get("values") == "1" {
		vals, err := c.EnvValues(r.Context(), state)
		if err != nil {
			carryFail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"state": state, "env": vals})
		return
	}
	keys, err := c.EnvKeys(r.Context(), state)
	if err != nil {
		carryFail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": state, "keys": keys})
}

type carryEnvBody struct {
	State string            `json:"state"`
	Vars  map[string]string `json:"vars"`
}

func (s *Server) carryEnvSet(w http.ResponseWriter, r *http.Request) {
	var b carryEnvBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || len(b.Vars) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("body needs {\"vars\": {\"KEY\": \"value\"}}"))
		return
	}
	v, err := carryops.FromEnv().EnvSet(r.Context(), b.State, b.Vars)
	carryOK(w, v, err)
}

func (s *Server) carryEnvUnset(w http.ResponseWriter, r *http.Request) {
	v, err := carryops.FromEnv().EnvUnset(r.Context(), r.URL.Query().Get("state"), chi.URLParam(r, "key"))
	carryOK(w, v, err)
}

func (s *Server) carryLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	v, err := carryops.FromEnv().Log(r.Context(), q.Get("state"), limit)
	carryOK(w, v, err)
}

type carryRollbackBody struct {
	State   string `json:"state"`
	Version int    `json:"version"`
}

func (s *Server) carryRollback(w http.ResponseWriter, r *http.Request) {
	var b carryRollbackBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Version <= 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("body needs {\"version\": n}"))
		return
	}
	v, err := carryops.FromEnv().Rollback(r.Context(), b.State, b.Version)
	if err != nil {
		carryFail(w, err)
		return
	}
	// The server's own state may hold its skills and MCP servers (packages.go):
	// put them back on disk too, so the rollback is live, not just recorded.
	restored, rerr := s.restorePackages(r.Context(), b.State)
	if v == nil {
		v = map[string]any{}
	}
	v["packagesRestored"] = restored
	if rerr != nil {
		v["packagesError"] = rerr.Error()
	}
	writeJSON(w, http.StatusOK, v)
}

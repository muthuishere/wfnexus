// Package api exposes the REST + SSE surface consumed by the React UI.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"

	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/blob"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/engine"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/skills"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/store"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/workflow"
)

type Server struct {
	eng   *engine.Engine
	store *store.Store
	blob  *blob.Blob
	uiDir string
}

func New(eng *engine.Engine, st *store.Store, bl *blob.Blob, uiDir string) http.Handler {
	s := &Server{eng: eng, store: st, blob: bl, uiDir: uiDir}
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Logger, middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{AllowedOrigins: []string{"*"}, AllowedMethods: []string{"GET", "POST", "DELETE"}, AllowedHeaders: []string{"*"}}))

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
		r.Get("/skills", s.listSkills)
		r.Get("/tools", s.listTools)
		r.Get("/workflows", s.listWorkflows)
		r.Post("/workflows/reload", s.reloadWorkflows)
		r.Get("/workflows/{name}", s.getWorkflow)
		r.Put("/workflows/{name}", s.saveWorkflow)
		r.Delete("/workflows/{name}", s.deleteWorkflow)
		r.Post("/workflows/{name}/runs", s.createRun)
		r.Get("/runs", s.listRuns)
		r.Get("/runs/{id}", s.getRun)
		r.Get("/runs/{id}/events", s.runEvents)
		r.Post("/runs/{id}/approve", s.approve)
		r.Post("/runs/{id}/reject", s.reject)
		r.Post("/runs/{id}/input", s.provideInput)
		r.Post("/runs/{id}/answer", s.answer)
		r.Post("/runs/{id}/retry", s.retry)
		r.Post("/runs/{id}/cancel", s.cancel)
		r.Get("/runs/{id}/artifacts/{artifactId}", s.artifact)
	})
	r.Get("/*", s.ui)
	return r
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"error": err.Error()})
}

// listSkills is the skill registry the workflow author picks from.
func (s *Server) listSkills(w http.ResponseWriter, _ *http.Request) {
	reg := s.eng.Skills()
	writeJSON(w, 200, map[string]any{"roots": reg.Roots(), "skills": reg.List(), "skipped": reg.Skipped()})
}

// listTools is the built-in (Claude-style shell/file/search) tool catalog.
func (s *Server) listTools(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, skills.Builtins())
}

func (s *Server) listWorkflows(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, workflow.Sorted(s.eng.Definitions()))
}

func (s *Server) reloadWorkflows(w http.ResponseWriter, _ *http.Request) {
	if err := s.eng.ReloadDefinitions(); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, workflow.Sorted(s.eng.Definitions()))
}

func (s *Server) getWorkflow(w http.ResponseWriter, r *http.Request) {
	d := s.eng.Definitions()[chi.URLParam(r, "name")]
	if d == nil {
		writeErr(w, 404, fmt.Errorf("workflow not found"))
		return
	}
	writeJSON(w, 200, d)
}

// saveWorkflow validates an authored definition and writes it only if it
// survives a round trip through the real loader.
func (s *Server) saveWorkflow(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var body struct {
		Definition *workflow.Definition `json:"definition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Definition == nil {
		writeErr(w, 400, fmt.Errorf("body needs a `definition`"))
		return
	}
	if body.Definition.Name == "" {
		body.Definition.Name = name
	}
	if body.Definition.Name != name {
		writeErr(w, 400, fmt.Errorf("definition name %q does not match the url %q", body.Definition.Name, name))
		return
	}
	path, err := s.eng.SaveWorkflow(body.Definition)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": path, "definition": s.eng.Definitions()[name]})
}

func (s *Server) deleteWorkflow(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DeleteWorkflow(chi.URLParam(r, "name")); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if s.eng.Definitions()[name] == nil {
		writeErr(w, 404, fmt.Errorf("workflow not found"))
		return
	}
	var input json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeErr(w, 400, err)
		return
	}
	run, err := s.store.CreateRun(r.Context(), name, input)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	s.eng.Start(run.ID)
	writeJSON(w, 201, run)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.ListRuns(r.Context(), 100)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if runs == nil {
		runs = []*store.Run{}
	}
	writeJSON(w, 200, runs)
}

func (s *Server) runID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, 400, fmt.Errorf("bad run id"))
		return id, false
	}
	return id, true
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id, ok := s.runID(w, r)
	if !ok {
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	steps, _ := s.store.ListSteps(r.Context(), id)
	arts, _ := s.store.ListArtifacts(r.Context(), id)
	if steps == nil {
		steps = []*store.StepRun{}
	}
	if arts == nil {
		arts = []*store.Artifact{}
	}
	writeJSON(w, 200, map[string]any{"run": run, "steps": steps, "artifacts": arts, "definition": s.eng.Definitions()[run.Workflow]})
}

// runEvents replays persisted events from ?after=<id> then streams live ones (SSE).
func (s *Server) runEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := s.runID(w, r)
	if !ok {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)

	send := func(ev *store.Event) {
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Kind, b)
		flusher.Flush()
	}
	live, unsub := s.eng.Subscribe(id)
	defer unsub()

	// replay backlog
	last := after
	for {
		batch, err := s.store.ListEvents(r.Context(), id, last, 500)
		if err != nil || len(batch) == 0 {
			break
		}
		for _, ev := range batch {
			send(ev)
			last = ev.ID
		}
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-live:
			if ev.ID <= last {
				continue
			}
			last = ev.ID
			send(ev)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

type stepBody struct {
	StepID string         `json:"stepId"`
	Reason string         `json:"reason"`
	Answer string         `json:"answer"`
	Input  map[string]any `json:"input"`
}

func (s *Server) decodeStep(w http.ResponseWriter, r *http.Request) (uuid.UUID, stepBody, bool) {
	id, ok := s.runID(w, r)
	if !ok {
		return id, stepBody{}, false
	}
	var b stepBody
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&b)
	}
	if b.StepID == "" {
		if run, err := s.store.GetRun(r.Context(), id); err == nil {
			b.StepID = run.CurrentStep
		}
	}
	return id, b, true
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	id, b, ok := s.decodeStep(w, r)
	if !ok {
		return
	}
	if err := s.eng.Approve(r.Context(), id, b.StepID); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) reject(w http.ResponseWriter, r *http.Request) {
	id, b, ok := s.decodeStep(w, r)
	if !ok {
		return
	}
	if err := s.eng.Reject(r.Context(), id, b.StepID, b.Reason); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) provideInput(w http.ResponseWriter, r *http.Request) {
	id, b, ok := s.decodeStep(w, r)
	if !ok {
		return
	}
	if err := s.eng.ProvideInput(r.Context(), id, b.Input); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// answer resolves an ask_human suspension.
func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	id, b, ok := s.decodeStep(w, r)
	if !ok {
		return
	}
	if err := s.eng.AnswerQuestion(r.Context(), id, b.StepID, b.Answer); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) retry(w http.ResponseWriter, r *http.Request) {
	id, b, ok := s.decodeStep(w, r)
	if !ok {
		return
	}
	if err := s.eng.Retry(r.Context(), id, b.StepID); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := s.runID(w, r)
	if !ok {
		return
	}
	s.eng.Cancel(r.Context(), id)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// artifact redirects to a short-lived presigned S3 URL, or streams the object when ?inline=1.
func (s *Server) artifact(w http.ResponseWriter, r *http.Request) {
	id, ok := s.runID(w, r)
	if !ok {
		return
	}
	arts, err := s.store.ListArtifacts(r.Context(), id)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	want := chi.URLParam(r, "artifactId")
	for _, a := range arts {
		if a.ID.String() != want {
			continue
		}
		if r.URL.Query().Get("inline") == "1" {
			rc, err := s.blob.Get(r.Context(), a.ObjectKey)
			if err != nil {
				writeErr(w, 500, err)
				return
			}
			defer rc.Close()
			w.Header().Set("Content-Type", a.ContentType)
			buf := make([]byte, 32*1024)
			for {
				n, err := rc.Read(buf)
				if n > 0 {
					_, _ = w.Write(buf[:n])
				}
				if err != nil {
					return
				}
			}
		}
		url, err := s.blob.PresignedGet(r.Context(), a.ObjectKey, 15*time.Minute)
		if err != nil {
			writeErr(w, 500, err)
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
		return
	}
	writeErr(w, 404, fmt.Errorf("artifact not found"))
}

// ui serves the built React app with SPA fallback (dev uses Vite's proxy instead).
func (s *Server) ui(w http.ResponseWriter, r *http.Request) {
	if s.uiDir == "" {
		http.NotFound(w, r)
		return
	}
	p := filepath.Join(s.uiDir, filepath.Clean("/"+r.URL.Path))
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		http.ServeFile(w, r, p)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.uiDir, "index.html"))
}

var _ = context.Background

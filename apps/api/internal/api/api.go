// Package api exposes the REST + SSE surface consumed by the React UI.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/blob"
	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
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
	// Route on the ESCAPED path so an encoded slash survives routing.
	//
	// A workflow from an imported source is addressed as `source/name`, and chi
	// unescapes before matching — so `demo%2Fchecks` became the path segments
	// `demo` and `checks`, matched nothing, and answered "workflow not found"
	// for a workflow that was loaded. Handlers take the name through
	// urlName(), which unescapes it again.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if rctx := chi.RouteContext(req.Context()); rctx != nil {
				rctx.RoutePath = req.URL.EscapedPath()
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Use(cors.Handler(cors.Options{AllowedOrigins: []string{"*"}, AllowedMethods: []string{"GET", "POST", "PUT", "DELETE"}, AllowedHeaders: []string{"*"}}))

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
		r.Get("/skills", s.listSkills)
		r.Get("/tools", s.listTools)
		r.Get("/workflows", s.listWorkflows)
		r.Post("/workflows/reload", s.reloadWorkflows)
		r.Get("/workflows/{name}", s.getWorkflow)
		r.Post("/workflows/validate", s.validateWorkflow)
		r.Get("/mcp", s.listMcp)
		r.Get("/providers", s.listProviders)
		r.Get("/classifiers", s.listClassifiers)
		r.Get("/registries", s.listRegistries)
		r.Get("/models", s.listModels)
		r.Get("/doctor", s.doctor)
		r.Get("/sources", s.listSources)
		r.Post("/sources", s.importSource)
		r.Delete("/sources/{name}", s.forgetSource)
		r.Put("/providers/{name}", s.saveProvider)
		r.Delete("/providers/{name}", s.deleteProvider)
		r.Put("/classifiers/{name}", s.saveClassifier)
		r.Delete("/classifiers/{name}", s.deleteClassifier)
		r.Put("/workflows/{name}", s.saveWorkflow)
		r.Delete("/workflows/{name}", s.deleteWorkflow)
		r.Post("/workflows/{name}/runs", s.createRun)
		r.Post("/workflows/{name}/dispatches", s.repositoryDispatch)
		r.Post("/workflows/{name}/dryrun", s.dryRun)
		r.Post("/dryrun", s.dryRunDraft)
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

// urlName reads a path parameter that may carry an encoded slash, which every
// qualified workflow name (`source/name`) does.
func urlName(r *http.Request, key string) string {
	v := chi.URLParam(r, key)
	if decoded, err := url.PathUnescape(v); err == nil {
		return decoded
	}
	return v
}

// definitionFromBody reads {"definition": {...}} from a request.
func definitionFromBody(r *http.Request) (*workflow.Definition, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	return definitionFrom(raw)
}

func definitionFrom(raw []byte) (*workflow.Definition, error) {
	var envelope struct {
		Definition json.RawMessage `json:"definition"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Definition) == 0 {
		return nil, fmt.Errorf("body needs a `definition`")
	}
	return workflow.DecodeDefinition(envelope.Definition)
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
	d := s.eng.Definitions()[urlName(r, "name")]
	if d == nil {
		writeErr(w, 404, fmt.Errorf("workflow not found"))
		return
	}
	writeJSON(w, 200, d)
}

// validateWorkflow is the authoritative verdict with no side effect, so the
// builder never has to write a file to find out whether it is legal.
func (s *Server) validateWorkflow(w http.ResponseWriter, r *http.Request) {
	def, err := definitionFromBody(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.eng.CheckWorkflow(def); err != nil {
		writeJSON(w, 200, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"valid": true, "definition": def})
}

func (s *Server) listMcp(w http.ResponseWriter, _ *http.Request) {
	c := s.eng.Catalog()
	writeJSON(w, 200, map[string]any{"entries": c.Mcp.List(), "skipped": c.Mcp.Skips()})
}

func (s *Server) listProviders(w http.ResponseWriter, _ *http.Request) {
	c := s.eng.Catalog()
	writeJSON(w, 200, map[string]any{"entries": c.Providers.List(), "skipped": c.Providers.Skips()})
}

func (s *Server) listClassifiers(w http.ResponseWriter, _ *http.Request) {
	c := s.eng.Catalog()
	writeJSON(w, 200, map[string]any{"entries": c.Classifiers.List(), "skipped": c.Classifiers.Skips()})
}

// listRegistries is every registry in one call — what an authoring UI needs to
// populate every picker on a step.
func (s *Server) listRegistries(w http.ResponseWriter, _ *http.Request) {
	c := s.eng.Catalog()
	reg := s.eng.Skills()
	writeJSON(w, 200, map[string]any{
		"skills":      map[string]any{"entries": reg.List(), "skipped": reg.Skipped(), "roots": reg.Roots()},
		"tools":       map[string]any{"entries": skills.Builtins()},
		"providers":   map[string]any{"entries": c.Providers.List(), "skipped": c.Providers.Skips()},
		"classifiers": map[string]any{"entries": c.Classifiers.List(), "skipped": c.Classifiers.Skips()},
		"mcp":         map[string]any{"entries": c.Mcp.List(), "skipped": c.Mcp.Skips()},
	})
}

// saveProvider and saveClassifier write one registry entry through the loader's
// own validation and reload, so the UI cannot create something that silently
// never loads. The name in the URL is authoritative: the body cannot rename an
// entry out from under the caller.
func (s *Server) saveProvider(w http.ResponseWriter, r *http.Request) {
	var p catalog.Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, 400, err)
		return
	}
	p.Name = urlName(r, "name")
	if err := s.eng.SaveProvider(p); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, p)
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DeleteProvider(urlName(r, "name")); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) saveClassifier(w http.ResponseWriter, r *http.Request) {
	var c catalog.Classifier
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeErr(w, 400, err)
		return
	}
	c.Name = urlName(r, "name")
	if err := s.eng.SaveClassifier(c); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, c)
}

func (s *Server) deleteClassifier(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DeleteClassifier(urlName(r, "name")); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// dryRun answers "would this workflow actually work here?" without calling a
// model, cloning a repository or writing a file.
func (s *Server) dryRun(w http.ResponseWriter, r *http.Request) {
	var input map[string]any
	_ = json.NewDecoder(r.Body).Decode(&input)
	out, err := s.eng.DryRunWorkflow(urlName(r, "name"), input)
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, out)
}

// dryRunDraft checks a definition that is not saved anywhere — which is how the
// builder, and the authoring agent, check work before proposing it.
func (s *Server) dryRunDraft(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var envelope struct {
		Input map[string]any `json:"input"`
	}
	_ = json.Unmarshal(raw, &envelope)
	def, err := definitionFrom(raw)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, s.eng.DryRunDraft(def, envelope.Input))
}

// listSources returns every place workflows are loaded from, and anything that
// failed to load — a broken file in one imported repository is recorded rather
// than fatal, so it needs somewhere to be seen.
func (s *Server) listSources(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"sources": workflow.SortedSources(s.eng.Sources()),
		"skipped": s.eng.SourceSkips(),
	})
}

// importSource clones (or points at) a repository and loads the workflows in
// its `.wfx/workflows/` directory.
func (s *Server) importSource(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Repo   string `json:"repo"`
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Repo == "" {
		writeErr(w, 400, fmt.Errorf("`repo` is required — a URL to clone or a local path to use in place"))
		return
	}
	src, err := s.eng.ImportRepo(r.Context(), body.Name, body.Repo, body.Branch)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 201, src)
}

func (s *Server) forgetSource(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.ForgetRepo(urlName(r, "name")); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// doctor reports what is actually wired on this machine: the default model,
// every provider and classifier, and whether each could run right now.
func (s *Server) doctor(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, s.eng.Doctor())
}

func (s *Server) listModels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, s.eng.Models())
}

// saveWorkflow validates an authored definition and writes it only if it
// survives a round trip through the real loader.
func (s *Server) saveWorkflow(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	// Decoded in a dialect-tolerant way: a definition written as a FILE says
	// `output_schema`, while the UI says `outputSchema`, and dropping the one
	// we did not expect reported the step as missing a field it had (decode.go).
	def, err := definitionFromBody(r)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if def.Name == "" {
		def.Name = name
	}
	if def.Name != name {
		writeErr(w, 400, fmt.Errorf("definition name %q does not match the url %q", def.Name, name))
		return
	}
	path, err := s.eng.SaveWorkflow(def)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "path": path, "definition": s.eng.Definitions()[name]})
}

func (s *Server) deleteWorkflow(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DeleteWorkflow(urlName(r, "name")); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if s.eng.Definitions()[name] == nil {
		writeErr(w, 404, fmt.Errorf("workflow not found"))
		return
	}
	var input json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeErr(w, 400, err)
		return
	}
	// Fill declared defaults and check the input against input_schema BEFORE a
	// run exists. A bad input used to become a run that failed several turns in,
	// and a declared default was never applied at all.
	input, err := s.eng.PrepareInput(name, input)
	if err != nil {
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

// repositoryDispatch is the inbound trigger: another system POSTs and a run
// starts. The body is Actions' own shape, so a caller that already knows how to
// fire a repository_dispatch needs nothing new:
//
//	POST /api/workflows/{name}/dispatches
//	{"event_type": "bug_reported", "client_payload": {...}}
//
// The payload becomes the run's input. A workflow that does not declare
// `on: repository_dispatch` is refused, and one that declares `types:` is
// refused for an event type it does not list — both by PrepareRun, so the
// check cannot be skipped by adding another caller.
//
// This endpoint is UNAUTHENTICATED, like every other endpoint here. That is a
// blocking problem for exposing this server anywhere but localhost, and it is
// the one hard gate in docs/not-now.md — recorded there rather than papered
// over with a per-workflow secret field Actions does not have.
func (s *Server) repositoryDispatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EventType     string         `json:"event_type"`
		ClientPayload map[string]any `json:"client_payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	name := urlName(r, "name")
	def := s.eng.Definitions()[name]
	if def == nil {
		writeErr(w, 404, fmt.Errorf("workflow not found"))
		return
	}
	if def.On.RepositoryDispatch != nil && !def.On.RepositoryDispatch.Accepts(body.EventType) {
		writeErr(w, 400, fmt.Errorf("%s does not answer event_type %q; it lists %v",
			name, body.EventType, def.On.RepositoryDispatch.Types))
		return
	}
	if err := s.eng.StartTriggered(r.Context(), name, workflow.TriggerRepositoryDispatch, body.ClientPayload); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "workflow": name, "eventType": body.EventType})
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

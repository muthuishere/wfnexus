package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// The worker surface is small on purpose. A machine joins, asks for work,
// reports a result, and says it is still there. setup asks one more question
// — which providers exist, and of what kind — and that is a list of names,
// not a command to run. Everything else about placing work is the platform's
// business, so the thing you install on somebody's Windows box stays small
// enough to read in one sitting.

// bearer pulls the worker's own token off the request.
func bearer(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
}

// authWorker resolves the caller, writing the refusal itself when it cannot.
func (s *Server) authWorker(w http.ResponseWriter, r *http.Request) *store.Worker {
	wk, err := s.eng.AuthWorker(r.Context(), bearer(r))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return nil
	}
	return wk
}

// joinWorker is the endpoint behind the one command a machine runs to join.
func (s *Server) joinWorker(w http.ResponseWriter, r *http.Request) {
	var req engine.JoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.eng.Join(r.Context(), req)
	if err != nil {
		code := http.StatusBadRequest
		if err == engine.ErrBadToken {
			code = http.StatusUnauthorized
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, http.StatusCreated, res)
}

// claimJob long-polls for work carrying one of this worker's labels.
func (s *Server) claimJob(w http.ResponseWriter, r *http.Request) {
	wk := s.authWorker(w, r)
	if wk == nil {
		return
	}
	wait := 25 * time.Second
	if v := r.URL.Query().Get("wait"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 && d <= time.Minute {
			wait = d
		}
	}
	job, err := s.eng.Claim(r.Context(), wk, wait)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if job == nil {
		// Nothing to do is not an error, and a body would only be discarded.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// finishJob records the worker's report for one job.
func (s *Server) finishJob(w http.ResponseWriter, r *http.Request) {
	wk := s.authWorker(w, r)
	if wk == nil {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "jobId"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var res engine.JobResult
	if err := json.NewDecoder(r.Body).Decode(&res); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	raw, _ := json.Marshal(res)
	if err := s.store.FinishJob(r.Context(), id, wk.ID, raw); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// workerEvents takes a worker's live activity for a job it holds and appends it
// to the run's log. Without it an agent step on another machine would be a
// blank screen until it finished — the one thing that makes a remote step feel
// different from a local one.
func (s *Server) workerEvents(w http.ResponseWriter, r *http.Request) {
	wk := s.authWorker(w, r)
	if wk == nil {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "jobId"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	// Only the worker holding the job may write to that run's log.
	if job.WorkerID == nil || *job.WorkerID != wk.ID {
		writeErr(w, http.StatusForbidden, fmt.Errorf("this job is not leased to this worker"))
		return
	}
	var body struct {
		Events []engine.WorkerEvent `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.eng.IngestWorkerEvents(r.Context(), job.RunID, body.Events)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "accepted": len(body.Events)})
}

// heartbeat is how a worker with nothing to do stays counted as online.
func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	if wk := s.authWorker(w, r); wk != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": wk.ID})
	}
}

// workerProviders is what `wfx-runner setup` asks: which providers this
// platform has, and their kinds. A worker token or the registration token
// answers it. The body is names and kinds — the catalog's command argv is
// not copied, because that array is how published content would otherwise
// become a command the runner executes.
func (s *Server) workerProviders(w http.ResponseWriter, r *http.Request) {
	tok := bearer(r)
	if tok == "" {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthenticated"))
		return
	}
	if _, err := s.eng.AuthWorker(r.Context(), tok); err != nil {
		want, werr := s.eng.RegistrationToken(r.Context())
		if werr != nil || tok != want {
			writeErr(w, http.StatusUnauthorized, errors.New("unauthenticated"))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": s.eng.SetupNeeds()})
}

// ---- the admin side ----

// workerView is a worker plus what an operator actually wants to see.
type workerView struct {
	*store.Worker
	Status string `json:"status"`
}

// listWorkers is the Workers page: who has joined, what labels they serve, and
// the one command that adds another.
func (s *Server) listWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := s.store.ListWorkers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	views := make([]workerView, 0, len(workers))
	for _, wk := range workers {
		views = append(views, workerView{Worker: wk, Status: wk.Status()})
	}
	token, err := s.eng.RegistrationToken(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workers": views,
		// The labels this process serves itself. They need no worker, which is
		// why a fresh install has an empty Workers page and still runs.
		"localLabels": s.eng.LocalLabels(),
		"joinCommand": joinCommand(s.publicURL(r), token),
		"url":         s.publicURL(r),
		"token":       token,
	})
}

// rotateToken invalidates the join command without disturbing the machines that
// already joined — each of those holds its own token.
func (s *Server) rotateToken(w http.ResponseWriter, r *http.Request) {
	token, err := s.eng.RotateRegistrationToken(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": token, "joinCommand": joinCommand(s.publicURL(r), token),
	})
}

// removeWorker forgets a machine. It stops being sent work; if it is still
// polling it will be refused and can simply join again.
func (s *Server) removeWorker(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.DeleteWorker(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// publicURL is the address a worker can reach this server on. Configured
// (WFX_PUBLIC_URL) when the server sits behind an ingress, inferred otherwise —
// so the join command shown in the dashboard is one that actually works.
func (s *Server) publicURL(r *http.Request) string {
	if u := s.eng.PublicURL(); u != "" {
		return u
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

// joinCommand is the line an operator copies onto a machine. One command, a URL
// and a token: everything else the runner works out for itself.
func joinCommand(url, token string) string {
	return fmt.Sprintf("wfx-runner join --url %s --token %s --labels self-hosted", url, token)
}

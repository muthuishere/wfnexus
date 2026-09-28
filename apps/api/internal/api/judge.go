package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/judge"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// judgeRequest is `wfx judge` over the wire: the questions file, the items,
// and which of this server's classifiers to use (empty = the default).
type judgeRequest struct {
	Classifier string                       `json:"classifier"`
	Questions  map[string]workflow.Question `json:"questions"`
	Items      []engine.JudgeItem           `json:"items"`
	Bands      *judge.Bands                 `json:"bands,omitempty"`
	Parallel   int                          `json:"parallel,omitempty"`
}

// maxJudgeItems bounds one request; the CLI batches larger inputs.
const maxJudgeItems = 500

// judgeItems runs the classifier on the server, where the registry and its key
// live. The key is never returned; errors are scrubbed by the engine.
func (s *Server) judgeItems(w http.ResponseWriter, r *http.Request) {
	var req judgeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
		writeErr(w, 400, err)
		return
	}
	if len(req.Questions) == 0 {
		writeErr(w, 400, errors.New("no questions"))
		return
	}
	if len(req.Items) == 0 {
		writeErr(w, 400, errors.New("no items to judge"))
		return
	}
	if len(req.Items) > maxJudgeItems {
		writeErr(w, 400, errors.New("too many items in one request (max 500); send them in batches"))
		return
	}
	bands := judge.DefaultBands
	if req.Bands != nil {
		bands = *req.Bands
	}
	if req.Parallel <= 0 || req.Parallel > 16 {
		req.Parallel = 8
	}
	results, cost, err := s.eng.Judge(r.Context(), req.Classifier, req.Questions, req.Items, bands, req.Parallel)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, map[string]any{"results": results, "costUsd": cost})
}

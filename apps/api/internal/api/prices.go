package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
)

// The price table (ADR 0020): what each model family costs per million tokens.
// The model id rides in the query or body, never the path — a family key may
// carry a `/` and the fallback row is `*`.

type priceBody struct {
	Model string  `json:"model"`
	In    float64 `json:"in"`
	Out   float64 `json:"out"`
}

func (s *Server) listPrices(w http.ResponseWriter, r *http.Request) {
	rows, err := s.eng.ListPrices(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prices": rows, "fallbackKey": catalog.FallbackKey})
}

func (s *Server) setPrice(w http.ResponseWriter, r *http.Request) {
	var b priceBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.eng.SetPrice(r.Context(), b.Model, b.In, b.Out); err != nil {
		writeErr(w, priceStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": b.Model})
}

// deletePrice removes a family; one the binary ships returns at its default.
func (s *Server) deletePrice(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DeletePrice(r.Context(), r.URL.Query().Get("model")); err != nil {
		writeErr(w, priceStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func priceStatus(err error) int {
	if errors.Is(err, engine.ErrNoPriceStore) {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

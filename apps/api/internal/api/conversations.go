package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// Conversations (ADR 0026 decision 4): list them, and move one's state.
//
//	GET  /api/conversations?limit=N
//	POST /api/conversations/state   {"channel","chat","state"}
func (s *Server) conversationRoutes(r interface {
	Get(string, http.HandlerFunc)
	Post(string, http.HandlerFunc)
}) {
	r.Get("/conversations", s.listConversations)
	r.Post("/conversations/state", s.setConversationState)
}

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.eng.Conversations(r.Context(), limit)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"conversations": list})
}

func (s *Server) setConversationState(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Channel string `json:"channel"`
		Chat    string `json:"chat"`
		State   string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	err := s.eng.SetConversationState(r.Context(), engine.ConversationKey{Channel: body.Channel, Chat: body.Chat}, body.State)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, 404, err)
	case err != nil:
		writeErr(w, 400, err)
	default:
		writeJSON(w, 200, map[string]any{"channel": body.Channel, "chat": body.Chat, "state": body.State})
	}
}

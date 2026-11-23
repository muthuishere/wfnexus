package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/model"
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
	all, err := s.eng.Conversations(r.Context(), limit)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	// The conversations of the caller's tenant's workflows (#189).
	list := []model.Conversation{}
	for _, c := range all {
		if s.tenantVisible(r, s.conversationProject(c)) {
			list = append(list, c)
		}
	}
	writeJSON(w, 200, map[string]any{"conversations": list})
}

// conversationProject is the project a conversation belongs to: its own, or
// its workflow's. One with neither is the server's (the default tenant's).
func (s *Server) conversationProject(c model.Conversation) string {
	if c.Project != "" {
		return c.Project
	}
	if c.Workflow != "" {
		return s.eng.ProjectFor(c.Workflow)
	}
	return ""
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
	// authorize lets a person in an organization through to here (#189):
	// another tenant's conversation is a 404, and moving one of the
	// tenant's needs someone who may run its workflow.
	if orgMember(r) {
		c, err := s.store.GetConversation(r.Context(), body.Channel, body.Chat)
		if err != nil || !s.tenantVisible(r, s.conversationProject(c)) {
			notFound(w)
			return
		}
		if !s.canRunWorkflow(r, c.Workflow) {
			writeErr(w, http.StatusForbidden, errors.New("forbidden: this subject does not hold runs:write"))
			return
		}
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

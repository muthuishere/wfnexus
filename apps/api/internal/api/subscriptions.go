package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
)

// Subscriptions (ADR 0026): which messages on a channel start which workflow.
//
//	GET    /subscriptions                  every subscription, with its live check
//	GET    /subscriptions/{name}           one, plus what the router did lately
//	PUT    /subscriptions/{name}           add or replace (validated on the way in)
//	DELETE /subscriptions/{name}
//	POST   /subscriptions/{name}/pause|resume|validate
func (s *Server) subscriptionRoutes(r interface {
	Get(string, http.HandlerFunc)
	Put(string, http.HandlerFunc)
	Post(string, http.HandlerFunc)
	Delete(string, http.HandlerFunc)
}) {
	r.Get("/subscriptions", s.listSubscriptions)
	r.Get("/subscriptions/{name}", s.getSubscription)
	r.Put("/subscriptions/{name}", s.saveSubscription)
	r.Delete("/subscriptions/{name}", s.deleteSubscription)
	r.Post("/subscriptions/{name}/{action}", s.subscriptionAction)
}

func (s *Server) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs := s.eng.Subscriptions()
	// ?project= narrows to the subscriptions whose target workflow is in
	// that project — the project drill-down's view.
	if p := r.URL.Query().Get("project"); p != "" {
		kept := subs[:0]
		for _, sub := range subs {
			if s.eng.ProjectFor(sub.Workflow) == p {
				kept = append(kept, sub)
			}
		}
		subs = kept
	}
	writeJSON(w, 200, subs)
}

func (s *Server) getSubscription(w http.ResponseWriter, r *http.Request) {
	info, ok := s.eng.GetSubscription(urlName(r, "name"))
	if !ok {
		writeErr(w, 404, errors.New("no such subscription"))
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ds, err := s.eng.SubscriptionDeliveries(r.Context(), info.Name, limit)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"subscription": info, "deliveries": ds})
}

func (s *Server) saveSubscription(w http.ResponseWriter, r *http.Request) {
	var sub catalog.Subscription
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&sub); err != nil {
		writeErr(w, 400, err)
		return
	}
	sub.Name = urlName(r, "name")
	// Status, timestamps and the error are the server's to write.
	sub.Status, sub.Error, sub.StartedAt, sub.CreatedAt, sub.UpdatedAt = "", "", nil, nil, nil
	info, err := s.eng.SaveSubscription(sub, actorOf(r, stepBody{}).ID)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, info)
}

func (s *Server) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DeleteSubscription(urlName(r, "name")); err != nil {
		code := 400
		if strings.HasPrefix(err.Error(), "no subscription") {
			code = 404
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) subscriptionAction(w http.ResponseWriter, r *http.Request) {
	info, err := s.eng.SetSubscriptionState(urlName(r, "name"), urlName(r, "action"))
	if err != nil {
		code := 400
		if strings.HasPrefix(err.Error(), "no subscription") {
			code = 404
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, 200, info)
}

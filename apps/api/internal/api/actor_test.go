package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// task 8.1/8.2 — the resolver is the AUTHENTICATED SUBJECT, and an actor in the
// request body is ignored. Approve, reject and answer all read this one
// function, so one test covers each resolution kind's source of truth.
func TestAForgedBodyActorIsIgnoredInFavourOfTheSubject(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/runs/x/approve", nil)
	req = req.WithContext(withSubject(req.Context(), &Subject{
		Kind: SubjectUser, Name: "ana", User: &store.User{Name: "ana"},
	}))
	got := actorOf(req, stepBody{Actor: "somebody-else"})
	if got.ID != "ana" {
		t.Fatalf("a body actor won over the authenticated subject: %+v", got)
	}
}

// task 8.4 — the CHANNEL is recorded, so the unauthenticated loopback era and
// the authenticated era are distinguishable to a later audit.
func TestTheChannelIsRecordedAndUnknownChannelsFallBackToApi(t *testing.T) {
	for in, want := range map[string]string{"cli": "cli", "ui": "ui", "api": "api", "": "api", "made-up": "api"} {
		req := httptest.NewRequest("POST", "/api/runs/x/approve", nil)
		if in != "" {
			req.Header.Set("X-WFX-Via", in)
		}
		if got := actorOf(req, stepBody{Actor: "ada"}); got.Via != want {
			t.Fatalf("X-WFX-Via %q recorded as %q, want %q", in, got.Via, want)
		}
	}
	// With no subject the claim is read — that IS the loopback era, recorded
	// as an unverified claim rather than refused.
	req := httptest.NewRequest("POST", "/api/runs/x/approve", nil)
	if got := actorOf(req, stepBody{Actor: "ada"}); got.ID != "ada" {
		t.Fatalf("the loopback claim was dropped: %+v", got)
	}
}

// task 8.3 — an empty actor with no authenticated subject is REFUSED, and the
// step's state is unchanged because the engine is never reached.
func TestAnEmptyActorWithNoSubjectIsRefused(t *testing.T) {
	h, _ := testServer(t, "127.0.0.1:8090")
	for _, verb := range []string{"approve", "reject", "answer"} {
		w := do(h, "POST", "/api/runs/2f1c9a3e-0000-4000-8000-000000000000/"+verb, "", map[string]any{})
		if w.Code != 400 {
			t.Fatalf("%s with no actor answered %d: %s", verb, w.Code, w.Body.String())
		}
		if body := w.Body.String(); !strings.Contains(body, "no actor") {
			t.Fatalf("%s: wrong refusal: %s", verb, body)
		}
	}
}

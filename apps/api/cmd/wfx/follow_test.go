package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Found rehearsing the compose stack: `wfx logs` and `wfx run -f` sent no
// credential, got a 401 from the authenticated server, and printed nothing with
// exit 0. The stream now carries the context's token, and a refusal is an error.
func TestFollowPresentsTheCredentialAndReportsARefusal(t *testing.T) {
	tempContexts(t)
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if auth == "" {
			http.Error(w, `{"error":"unauthenticated"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"stepId\":\"s\",\"kind\":\"log\",\"payload\":{\"text\":\"hello\"}}\n\n")
	}))
	defer srv.Close()
	t.Setenv("WFX_API", srv.URL)

	t.Setenv("WFX_API_TOKEN", "")
	if err := follow("r1", true); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("a refused stream must be an error, got %v", err)
	}
	t.Setenv("WFX_API_TOKEN", "wfx_follow_test")
	if err := follow("r1", true); err != nil {
		t.Fatalf("follow with a credential: %v", err)
	}
	if auth != "Bearer wfx_follow_test" {
		t.Fatalf("the stream did not carry the credential (set=%v)", auth != "")
	}
}

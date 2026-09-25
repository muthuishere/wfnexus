package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// A registration token, and a worker token, can ask which providers exist.
// A missing token and a wrong token cannot. The body is names and kinds.
func TestWorkerProvidersAcceptsAJoinTokenAndAWorkerToken(t *testing.T) {
	h, _ := testServer(t, "127.0.0.1:8090")
	if w := do(h, "GET", "/api/workers/providers", "", nil); w.Code != 401 {
		t.Fatalf("no token: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/workers/providers", "wfx_not-a-token", nil); w.Code != 401 {
		t.Fatalf("wrong token: %d %s", w.Code, w.Body)
	}
	pool := do(h, "GET", "/api/workers", "", nil)
	if pool.Code != 200 {
		t.Fatalf("workers page: %d %s", pool.Code, pool.Body)
	}
	var listed struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(pool.Body.Bytes(), &listed); err != nil || listed.Token == "" {
		t.Fatalf("no registration token in the workers page: %v %s", err, pool.Body)
	}
	got := do(h, "GET", "/api/workers/providers", listed.Token, nil)
	if got.Code != 200 {
		t.Fatalf("registration token: %d %s", got.Code, got.Body)
	}
	if strings.Contains(got.Body.String(), listed.Token) {
		t.Fatal("the registration token was echoed back")
	}
	var body struct {
		Providers []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, p := range body.Providers {
		if p.Name == "" || p.Kind == "" {
			t.Fatalf("a provider without a name or kind: %+v", p)
		}
	}

	joined := do(h, "POST", "/api/workers/join", "", map[string]any{
		"token": listed.Token, "name": "box", "labels": []string{"self-hosted"},
	})
	if joined.Code != 201 {
		t.Fatalf("join: %d %s", joined.Code, joined.Body)
	}
	var res struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(joined.Body.Bytes(), &res); err != nil || res.Token == "" {
		t.Fatalf("join did not return a worker token: %v %s", err, joined.Body)
	}
	if w := do(h, "GET", "/api/workers/providers", res.Token, nil); w.Code != 200 {
		t.Fatalf("worker token: %d %s", w.Code, w.Body)
	}
}

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMemberCommandHitsTheScopedRoutes(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.EscapedPath()+" "+string(b))
		_ = json.NewEncoder(w).Encode(map[string]any{"scope": "workflow", "name": "w", "user": "ana", "role": "workflow_runner"})
	}))
	defer srv.Close()
	tempContexts(t)
	t.Setenv("WFX_API", srv.URL)

	if err := memberCmd([]string{"add", "ana", "--workflow", "src/w", "--role", "workflow-admin"}); err != nil {
		t.Fatal(err)
	}
	if err := memberCmd([]string{"rm", "ana", "--project", "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := memberCmd([]string{"add", "ana", "--project", "a", "--workflow", "w"}); err == nil {
		t.Fatal("both scopes were accepted")
	}
	want := []string{
		`PUT /api/workflows/src%2Fw/members/ana {"role":"workflow-admin"}`,
		`DELETE /api/projects/acme/members/ana `,
	}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("requests:\n%q\nwant\n%q", got, want)
	}
}

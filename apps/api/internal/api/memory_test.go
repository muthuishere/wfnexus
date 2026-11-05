package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Memory is editable over the API: a hand-written note and an edited entry are
// what the next run of the workflow loads, and the routes carry the same
// permissions as the rest of memory.
func TestMemoryAddAndEditOverTheAPI(t *testing.T) {
	f := proposalServer(t, false)
	def := f.eng.Definitions()["demo/hello"]
	if def == nil {
		t.Fatal("no demo/hello workflow")
	}
	t.Cleanup(func() { _, _ = f.eng.ForgetMemory(context.Background(), def.Name, "") })
	base := "/api/workflows/" + url.PathEscape(def.Name) + "/memory?project=demo"

	code, added := f.do(t, "POST", base, map[string]any{"body": "check the staging db-api-77 first"}, "")
	if code != http.StatusCreated || added["kind"] != "note" || added["project"] != "demo" {
		t.Fatalf("add = %d %v", code, added)
	}
	if p := f.eng.PreviewMemory(context.Background(), def); !strings.Contains(p, "check the staging db-api-77 first") {
		t.Fatalf("the added note is not in what the next run loads:\n%s", p)
	}

	id := added["id"].(string)
	code, edited := f.do(t, "PATCH", "/api/memory/"+id, map[string]any{"body": "check the prod db-api-88 first"}, "")
	if code != http.StatusOK || edited["body"] != "check the prod db-api-88 first" {
		t.Fatalf("edit = %d %v", code, edited)
	}
	p := f.eng.PreviewMemory(context.Background(), def)
	if !strings.Contains(p, "check the prod db-api-88 first") || strings.Contains(p, "db-api-77") {
		t.Fatalf("the edit is not what the next run loads:\n%s", p)
	}

	if code, _ := f.do(t, "PATCH", "/api/memory/no-such-id", map[string]any{"body": "x"}, ""); code != http.StatusNotFound {
		t.Fatalf("edit of a missing entry = %d, want 404", code)
	}
	if code, _ := f.do(t, "POST", base, map[string]any{"body": "  "}, ""); code != http.StatusBadRequest {
		t.Fatalf("empty note = %d, want 400", code)
	}

	if got := PermissionFor(http.MethodPatch, "/api/memory/x"); got != PermStateWrite {
		t.Fatalf("PATCH /api/memory/{id} needs %q, want %q", got, PermStateWrite)
	}
	if got, want := PermissionFor(http.MethodPost, "/api/workflows/x/memory"), PermissionFor(http.MethodDelete, "/api/workflows/x/memory"); got != want {
		t.Fatalf("POST memory needs %q, DELETE memory needs %q — they must match", got, want)
	}
}

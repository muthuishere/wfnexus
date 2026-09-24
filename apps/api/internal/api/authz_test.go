package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// user creates a subject and returns the one value of its credential. The value
// exists here and in the request header, and nowhere else.
func user(t *testing.T, st *store.Store, name, role, project string) string {
	t.Helper()
	ctx := context.Background()
	u := &store.User{Name: name, Role: role, Project: project}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	value := auth.NewToken()
	if _, err := st.IssueUserToken(ctx, u.ID, auth.HashToken(value), "test", project); err != nil {
		t.Fatalf("issue token for %s: %v", name, err)
	}
	return value
}

// task 7.2 — every /api route maps to a NAMED permission. The test walks the
// real router, so a route added without a permission fails here rather than
// being refused in production by the fail-closed branch.
func TestEveryApiRouteHasANamedPermission(t *testing.T) {
	h, _ := testServer(t, ":8090")
	r, ok := h.(chi.Routes)
	if !ok {
		t.Fatal("the handler is not a chi router")
	}
	var missing []string
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/") {
			return nil
		}
		if PermissionFor(method, route) == "" {
			missing = append(missing, method+" "+route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) > 0 {
		t.Fatalf("routes with no named permission: %v", missing)
	}
}

// task 7.3 — a role edit takes effect on the NEXT authorization decision.
func TestARoleEditFlipsTheNextRequest(t *testing.T) {
	h, st := testServer(t, ":8090")
	tok := user(t, st, "ana", "viewer", "")
	if got := do(h, "GET", "/api/workflows", tok, nil).Code; got != 200 {
		t.Fatalf("viewer could not read workflows: %d", got)
	}
	if err := st.UpsertRole(context.Background(), "viewer", []string{"runs:read"}); err != nil {
		t.Fatal(err)
	}
	if got := do(h, "GET", "/api/workflows", tok, nil).Code; got != 403 {
		t.Fatalf("after revoking workflows:read the next request was %d, want 403", got)
	}
}

// [SEC-TEST] task 7.4 — the last administrator cannot be deleted or demoted.
func TestTheLastAdministratorCannotBeRemoved(t *testing.T) {
	h, st := testServer(t, ":8090")
	admin := user(t, st, "root", "admin", "")

	if w := do(h, "PUT", "/api/users/root", admin, map[string]any{"role": "viewer"}); w.Code != 409 {
		t.Fatalf("demoting the last admin answered %d: %s", w.Code, w.Body.String())
	}
	if w := do(h, "DELETE", "/api/users/root", admin, nil); w.Code != 409 {
		t.Fatalf("deleting the last admin answered %d: %s", w.Code, w.Body.String())
	}
	if u, err := st.UserByName(context.Background(), "root"); err != nil || u.Role != "admin" {
		t.Fatalf("the host lost its administrator: %+v %v", u, err)
	}
	// With a second admin, the first may go.
	user(t, st, "second", "admin", "")
	if w := do(h, "DELETE", "/api/users/root", admin, nil); w.Code != 200 {
		t.Fatalf("deleting one of two admins answered %d: %s", w.Code, w.Body.String())
	}
}

// [SEC-TEST] task 7.5 — a token scoped to one project cannot reach another's,
// and the refusal is INDISTINGUISHABLE from not-found.
func TestProjectScopeIsIndistinguishableFromNotFound(t *testing.T) {
	h, st := testServer(t, ":8090")
	tok := user(t, st, "ana", "publisher", "acme")

	// The fixture's engine holds no workflows, so every workflow is "local":
	// a subject scoped to acme is out of scope for all of them.
	outOfScope := do(h, "GET", "/api/workflows/whatever", tok, nil)
	neverExisted := do(h, "GET", "/api/runs/2f1c9a3e-0000-4000-8000-000000000000", tok, nil)
	if outOfScope.Code != http.StatusNotFound {
		t.Fatalf("out of scope answered %d, want 404: %s", outOfScope.Code, outOfScope.Body.String())
	}
	if neverExisted.Code != http.StatusNotFound {
		t.Fatalf("a run that does not exist answered %d, want 404", neverExisted.Code)
	}
	if outOfScope.Body.String() != neverExisted.Body.String() {
		t.Fatalf("the two answers differ:\n out of scope: %s\n not found:   %s",
			outOfScope.Body.String(), neverExisted.Body.String())
	}
	// Cancel and the pause resolutions are the same answer, and change nothing.
	for _, p := range []string{"/cancel", "/approve", "/reject", "/answer"} {
		w := do(h, "POST", "/api/runs/2f1c9a3e-0000-4000-8000-000000000000"+p, tok, map[string]any{})
		if w.Code != http.StatusNotFound || w.Body.String() != neverExisted.Body.String() {
			t.Fatalf("%s answered %d %s", p, w.Code, w.Body.String())
		}
	}
}

// [SEC-TEST] task 7.6 — listing is FILTERED, not merely checked on fetch.
func TestListingIsFilteredByProject(t *testing.T) {
	h, st := testServer(t, ":8090")
	ctx := context.Background()
	for _, p := range []string{"acme", "other"} {
		if _, err := st.CreateRun(ctx, p, "w-"+p, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	scoped := user(t, st, "ana", "viewer", "acme")
	unscoped := user(t, st, "root", "admin", "")

	var runs []*store.Run
	w := do(h, "GET", "/api/runs", scoped, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	if len(runs) != 1 || runs[0].Project != "acme" {
		t.Fatalf("a scoped listing showed %d run(s): %+v", len(runs), runs)
	}
	// Asking for the other project explicitly does not widen the scope.
	w = do(h, "GET", "/api/runs?project=other", scoped, nil)
	_ = json.Unmarshal(w.Body.Bytes(), &runs)
	if len(runs) != 1 || runs[0].Project != "acme" {
		t.Fatalf("?project= widened a scoped listing: %+v", runs)
	}
	w = do(h, "GET", "/api/runs", unscoped, nil)
	_ = json.Unmarshal(w.Body.Bytes(), &runs)
	if len(runs) != 2 {
		t.Fatalf("an unscoped subject saw %d run(s), want 2", len(runs))
	}
}

// [SEC-TEST] task 7.7 — authorization is never delegated. A provider assertion
// carrying role claims has no effect; the local table decides.
func TestProviderAssertedRolesHaveNoEffect(t *testing.T) {
	h, st := testServer(t, ":8090")
	tok := user(t, st, "ana", "viewer", "")
	req := func(hdrs map[string]string) int {
		w := doWithHeaders(h, "POST", "/api/users", tok, map[string]any{"name": "x", "role": "admin"}, hdrs)
		return w.Code
	}
	if got := req(nil); got != 403 {
		t.Fatalf("a viewer created a user: %d", got)
	}
	// Every shape an external provider might assert a role in.
	claims := []map[string]string{
		{"X-Auth-Role": "admin"},
		{"X-Forwarded-Groups": "admin,publisher"},
		{"X-Auth-Request-Groups": "admin"},
		{"X-Wfx-Role": "admin"},
	}
	for _, c := range claims {
		if got := req(c); got != 403 {
			t.Fatalf("an asserted claim %v changed the outcome: %d", c, got)
		}
	}
	// ...and a role granted LOCALLY does, immediately.
	if err := st.UpsertRole(context.Background(), "viewer", []string{"admin:write", "admin:read"}); err != nil {
		t.Fatal(err)
	}
	if got := req(nil); got != 201 {
		t.Fatalf("a locally granted permission did not take effect: %d", got)
	}
}

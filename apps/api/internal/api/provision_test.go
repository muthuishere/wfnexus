package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

func bearerHdr(tok string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + tok}
}

// provisionTwo creates acme and globex in one call each (project + token),
// with a same-named workflow in both. Off loopback, so every request needs
// a subject.
func provisionTwo(t *testing.T) (f hostedFixture, admin, acmeTok, globexTok string) {
	t.Helper()
	f = hostedServer(t, ":8090")
	admin = user(t, f.st, "root", RoleSuperadmin, "")
	toks := map[string]string{}
	for _, p := range []string{"acme", "globex"} {
		code, out := f.do(t, "POST", "/api/projects",
			map[string]any{"name": p, "create": true, "serviceToken": true, "tokenLabel": "liveconv"}, bearerHdr(admin))
		if code != 201 {
			t.Fatalf("provision %s = %d %v", p, code, out)
		}
		st, _ := out["serviceToken"].(map[string]any)
		tok, _ := st["token"].(string)
		if out["name"] != p || len(tok) < 20 || tok[:5] != store.ProjectTokenPrefix || st["label"] != "liveconv" {
			t.Fatalf("one call did not return project + token: %v", out)
		}
		toks[p] = tok
		f.addWorkflow(t, p, "deploy.yaml", sameNamed(p))
	}
	return f, admin, toks["acme"], toks["globex"]
}

// W2 done-when: a project and its token in one call; the token gets 404 on
// any other project — on every project-scoped route.
func TestProvisionedTokenIsConfinedToItsProject(t *testing.T) {
	f, admin, acme, globex := provisionTwo(t)

	// Stored hashed: the value appears nowhere in the table.
	var n int
	if err := f.st.DB().QueryRow(`SELECT count(*) FROM project_tokens WHERE token_hash = ? OR prefix = ?`, acme, acme).Scan(&n); err != nil || n != 0 {
		t.Fatalf("token stored in the clear (%d, %v)", n, err)
	}

	// A globex run, started by globex's own token.
	code, out := f.do(t, "POST", "/api/workflows/globex%2Fdeploy/runs", map[string]any{}, bearerHdr(globex))
	if code != 201 {
		t.Fatalf("globex runs its own workflow = %d %v", code, out)
	}
	globexRun := out["run"].(map[string]any)["id"].(string)
	f.waitRun(t, globexRun)
	_, out = f.do(t, "POST", "/api/projects/globex/tokens", map[string]any{"label": "x"}, bearerHdr(admin))
	globexTokID, _ := out["id"].(string)

	// acme's own routes work.
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/whoami"},
		{"GET", "/api/projects/acme"},
		{"GET", "/api/projects/acme/tokens"},
		{"GET", "/api/workflows/acme%2Fdeploy"},
		{"GET", "/api/runs"},
	} {
		if code, out := f.do(t, c.method, c.path, nil, bearerHdr(acme)); code != 200 {
			t.Fatalf("acme token on its own %s %s = %d %v", c.method, c.path, code, out)
		}
	}
	code, out = f.do(t, "POST", "/api/workflows/acme%2Fdeploy/runs", map[string]any{}, bearerHdr(acme))
	if code != 201 {
		t.Fatalf("acme runs its own workflow = %d %v", code, out)
	}
	acmeRun := out["run"].(map[string]any)["id"].(string)
	f.waitRun(t, acmeRun)
	if code, _ := f.do(t, "GET", "/api/runs/"+acmeRun, nil, bearerHdr(acme)); code != 200 {
		t.Fatalf("acme reads its own run = %d", code)
	}

	// Every globex-scoped route answers acme's token 404, the same body as a
	// route that never existed.
	_, absent := f.do(t, "GET", "/api/projects/no-such-project", nil, bearerHdr(acme))
	routes := []struct{ method, path string }{
		{"GET", "/api/projects/globex"},
		{"DELETE", "/api/projects/globex"},
		{"GET", "/api/projects/globex/tokens"},
		{"POST", "/api/projects/globex/tokens"},
		{"DELETE", "/api/projects/globex/tokens/" + globexTokID},
		{"GET", "/api/projects/globex/env"},
		{"PUT", "/api/projects/globex/env"},
		{"GET", "/api/projects/globex/members"},
		{"GET", "/api/projects/globex/expose"},
		{"POST", "/api/projects/globex/expose/mcp"},
		{"GET", "/api/workflows/globex%2Fdeploy"},
		{"PUT", "/api/workflows/globex%2Fdeploy"},
		{"DELETE", "/api/workflows/globex%2Fdeploy"},
		{"POST", "/api/workflows/globex%2Fdeploy/runs"},
		{"POST", "/api/workflows/globex%2Fdeploy/dispatches"},
		{"GET", "/api/workflows/globex%2Fdeploy/state"},
		{"GET", "/api/workflows/globex%2Fdeploy/expose"},
		{"GET", "/api/runs/" + globexRun},
		{"GET", "/api/runs/" + globexRun + "/events"},
		{"GET", "/api/runs/" + globexRun + "/state"},
		{"POST", "/api/runs/" + globexRun + "/cancel"},
		{"POST", "/api/runs/" + globexRun + "/retry"},
		{"POST", "/api/runs/" + globexRun + "/approve"},
		// Not addressed to acme at all: the catalogue and the operator routes.
		{"GET", "/api/workflows"},
		{"GET", "/api/projects"},
		{"POST", "/api/projects"},
		{"GET", "/api/env"},
		{"GET", "/api/workers"},
		{"GET", "/api/users"},
	}
	for _, c := range routes {
		code, out := f.do(t, c.method, c.path, map[string]any{}, bearerHdr(acme))
		if code != http.StatusNotFound || out["error"] != absent["error"] {
			t.Errorf("acme token on %s %s = %d %v, want 404 like an absent project", c.method, c.path, code, out)
		}
	}
	// The run listing shows acme's run only.
	req := httptest.NewRequest("GET", "/api/runs", nil)
	req.Header.Set("Authorization", "Bearer "+acme)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0]["project"] != "acme" {
		t.Fatalf("acme's run list = %s", rec.Body.String())
	}
	// globex is untouched: its token still works, its run still exists.
	if code, _ := f.do(t, "GET", "/api/runs/"+globexRun, nil, bearerHdr(globex)); code != 200 {
		t.Fatalf("globex's run gone after acme's attempts: %d", code)
	}
}

func TestProvisionMintRevokeDelete(t *testing.T) {
	f, admin, acme, _ := provisionTwo(t)
	// A service token cannot mint more of itself.
	if code, _ := f.do(t, "POST", "/api/projects/acme/tokens", map[string]any{}, bearerHdr(acme)); code != http.StatusForbidden {
		t.Fatalf("service token minted a token: %d", code)
	}
	code, out := f.do(t, "POST", "/api/projects/acme/tokens", map[string]any{"label": "second"}, bearerHdr(admin))
	if code != 201 {
		t.Fatalf("mint = %d %v", code, out)
	}
	second, id := out["token"].(string), out["id"].(string)
	if code, _ := f.do(t, "GET", "/api/projects/acme", nil, bearerHdr(second)); code != 200 {
		t.Fatalf("minted token refused: %d", code)
	}
	_, listed := f.do(t, "GET", "/api/projects/acme/tokens", nil, bearerHdr(admin))
	_ = listed
	if code, _ := f.do(t, "DELETE", "/api/projects/acme/tokens/"+id, nil, bearerHdr(admin)); code != 200 {
		t.Fatalf("revoke = %d", code)
	}
	if code, _ := f.do(t, "GET", "/api/projects/acme", nil, bearerHdr(second)); code != http.StatusUnauthorized {
		t.Fatalf("revoked token still works: %d", code)
	}
	// Delete the project: its remaining token dies with it.
	if code, out := f.do(t, "DELETE", "/api/projects/acme", nil, bearerHdr(admin)); code != 200 {
		t.Fatalf("delete = %d %v", code, out)
	}
	if code, _ := f.do(t, "GET", "/api/whoami", nil, bearerHdr(acme)); code != http.StatusUnauthorized {
		t.Fatalf("a deleted project's token still authenticates: %d", code)
	}
	if ok, _ := f.st.ProjectRegistered(context.Background(), "acme"); ok {
		t.Fatal("deleted project still registered")
	}
}

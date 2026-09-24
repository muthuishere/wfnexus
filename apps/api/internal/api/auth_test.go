package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A server on a real SQLite file, bound (notionally) to addr. Nothing listens —
// the handler is exercised directly, which is the point: the decision is made
// from the bind address, not from where a packet came from.
func testServer(t *testing.T, addr string) (http.Handler, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wfnexus.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(st.Close)
	// A host that requires authentication has its roles SEEDED at boot
	// (identity_boot.go). The fixture does the same, because authorization
	// reads the table on every decision and an unknown role is fail-closed.
	if !isLoopback(addr) {
		for name, perms := range DefaultRoles() {
			if err := st.UpsertRole(context.Background(), name, perms); err != nil {
				t.Fatalf("seed role %s: %v", name, err)
			}
		}
	}
	cfg := config.Config{Addr: addr}
	eng := engine.New(cfg, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	return New(eng, st, nil, addr, "", nil), st
}

func do(h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	var rdr *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// [SEC-TEST] task 5.3 — the loopback exemption, both halves.
func TestLoopbackExemption(t *testing.T) {
	h, st := testServer(t, "127.0.0.1:8090")

	res := do(h, "GET", "/api/workflows", "", nil)
	if res.Code != 200 {
		t.Fatalf("an unauthenticated request on loopback was refused: %d %s", res.Code, res.Body)
	}
	// ...and serving it created nothing. Absent, not permissive.
	if n, err := st.CountUsers(context.Background()); err != nil || n != 0 {
		t.Fatalf("serving on loopback created %d user rows (err %v)", n, err)
	}
	if n, err := st.CountRoles(context.Background()); err != nil || n != 0 {
		t.Fatalf("serving on loopback created %d role rows (err %v)", n, err)
	}
}

func TestOffLoopbackRefusesAnUnauthenticatedRequest(t *testing.T) {
	h, _ := testServer(t, "0.0.0.0:8090")

	res := do(h, "GET", "/api/workflows", "", nil)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("off loopback an unauthenticated request got %d, want 401: %s", res.Code, res.Body)
	}
	// The handler must not have run: a listing would have come back as JSON
	// with a workflows key.
	if strings.Contains(res.Body.String(), "workflows") {
		t.Fatalf("the handler was reached: %s", res.Body)
	}
}

func TestAnUnknownOrRevokedCredentialIsRefused(t *testing.T) {
	h, st := testServer(t, "0.0.0.0:8090")
	ctx := context.Background()

	if res := do(h, "GET", "/api/workflows", "wfx_"+strings.Repeat("0", 48), nil); res.Code != 401 {
		t.Fatalf("an unknown token got %d", res.Code)
	}

	u := &store.User{Name: "muthu", Role: "admin"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	value := auth.NewToken()
	if _, err := st.IssueUserToken(ctx, u.ID, auth.HashToken(value), "cli", ""); err != nil {
		t.Fatal(err)
	}
	if res := do(h, "GET", "/api/workflows", value, nil); res.Code != 200 {
		t.Fatalf("a valid user token was refused: %d %s", res.Code, res.Body)
	}
	if err := st.RevokeUserTokenByHash(ctx, auth.HashToken(value)); err != nil {
		t.Fatal(err)
	}
	if res := do(h, "GET", "/api/workflows", value, nil); res.Code != 401 {
		t.Fatalf("a revoked token still authenticates: %d", res.Code)
	}
}

// task 5.6 — both subject kinds resolve through the same middleware.
func TestBothSubjectKindsResolveOnOneMiddleware(t *testing.T) {
	h, st := testServer(t, "0.0.0.0:8090")
	ctx := context.Background()

	u := &store.User{Name: "muthu", Role: "admin"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	userTok := auth.NewToken()
	if _, err := st.IssueUserToken(ctx, u.ID, auth.HashToken(userTok), "cli", ""); err != nil {
		t.Fatal(err)
	}
	workerTok := auth.NewToken()
	if err := st.RegisterWorker(ctx, &store.Worker{Name: "box-1", Labels: []string{"linux"}}, auth.HashToken(workerTok)); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, token, kind string }{
		{"user", userTok, "user"},
		{"worker", workerTok, "worker"},
	} {
		res := do(h, "GET", "/api/whoami", tc.token, nil)
		if res.Code != 200 {
			t.Fatalf("%s: %d %s", tc.name, res.Code, res.Body)
		}
		var got map[string]any
		if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got["kind"] != tc.kind {
			t.Fatalf("%s resolved as %v", tc.name, got["kind"])
		}
	}
}

// [SEC-TEST] task 5.4 — there is no off switch. Asserted twice: by source, so a
// future flag cannot be added quietly, and by behaviour, so an environment
// variable someone tries cannot change the answer.
func TestThereIsNoWayToDisableAuthOffLoopback(t *testing.T) {
	// The whole server module, excluding this test's own words for the things
	// it is asserting do not exist.
	root := "../.."
	out, err := exec.Command("grep", "-rIn", "-e", "WFX_AUTH", "-e", "no-auth", "-e", "noauth",
		"-e", "DisableAuth", "-e", "AuthDisabled", "-e", "SkipAuth",
		"--include=*.go", "--exclude=*_test.go", root).CombinedOutput()
	// grep exits 1 with no output when there is no match, which is the pass.
	if err == nil && len(bytes.TrimSpace(out)) > 0 {
		t.Fatalf("a setting that could disable authentication appears in the source:\n%s", out)
	}

	for _, kv := range [][2]string{{"WFX_AUTH", "off"}, {"WFX_NO_AUTH", "1"}, {"WFX_DISABLE_AUTH", "true"}} {
		t.Setenv(kv[0], kv[1])
	}
	h, _ := testServer(t, "0.0.0.0:8090")
	if res := do(h, "GET", "/api/workflows", "", nil); res.Code != http.StatusUnauthorized {
		t.Fatalf("an environment variable turned authentication off: %d", res.Code)
	}
}

// [SEC-TEST] task 5.5 — the boot refusal. The binary is built and run against a
// non-loopback bind with an empty store; it must print the credential and exit
// without ever accepting a connection.
func TestBootRefusesNonLoopbackWithNoUsers(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the server binary")
	}
	bin := filepath.Join(t.TempDir(), "wfnexus")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	home := t.TempDir()
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"WFX_ADDR=0.0.0.0:0",
		"WFX_STORAGE_DRIVER=sqlite",
		"DATABASE_URL="+filepath.Join(home, "wfnexus.db"),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the server served on a non-loopback bind with no users:\n%s", out)
	}
	if !strings.Contains(string(out), "REFUSING TO SERVE") {
		t.Fatalf("no boot refusal in the output:\n%s", out)
	}
	if !strings.Contains(string(out), "admin token: wfx_") {
		t.Fatalf("the refusal did not print a bootstrap credential:\n%s", out)
	}
	// And the value it printed is not in any row: only its hash is.
	value := ""
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, "wfx_"); i >= 0 {
			value = strings.TrimSpace(line[i:])
		}
	}
	if value == "" {
		t.Fatal("could not read the printed credential back")
	}
	db, err := os.ReadFile(filepath.Join(home, "wfnexus.db"))
	if err != nil {
		t.Fatalf("no store was created: %v", err)
	}
	if bytes.Contains(db, []byte(value)) {
		t.Fatal("the bootstrap credential value is stored in the database")
	}
}

// doWithHeaders is do() plus whatever an external provider might be asserting.
func doWithHeaders(h http.Handler, method, path, token string, body any, hdrs map[string]string) *httptest.ResponseRecorder {
	var rdr *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdrs {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

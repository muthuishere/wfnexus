package api

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The wrong person bound a legacy account (a GitHub login reused after its
// owner renamed): an admin rejects the binding — the account is unbound and
// signed out, that subject cannot bind it again — and the real owner then
// binds it.
func TestARejectedBindingCanBeTakenByTheRealOwner(t *testing.T) {
	op := newMockOP(t, "gh-secret")
	op.noIDToken = true
	t.Setenv("TEST_GH_SECRET", "gh-secret")
	h, st := testServerPublic(t, config.Auth{Providers: []config.AuthProvider{{
		Name: "gh", Type: "github", ClientID: "wfx", ClientSecretEnv: "TEST_GH_SECRET",
		AuthURL: op.URL() + "/authorize", TokenURL: op.URL() + "/token", UserInfoURL: op.URL() + "/userinfo",
	}}}, testPublicURL)
	ctx := context.Background()
	markLegacy(t, st, "octo@gh", "")
	signIn := func(id int) map[string]any {
		t.Helper()
		op.userinfo = map[string]any{"id": id, "login": "octo"}
		w, _ := oauthFlow(t, h, "gh", nil)
		if w.Code != 303 {
			t.Fatalf("sign-in %d: %d %s", id, w.Code, w.Body)
		}
		return whoami(t, h, w)
	}
	// Someone else, now holding the login "octo", signs in first.
	if who := signIn(2002); who["name"] != "octo@gh" {
		t.Fatalf("impostor: %v", who)
	}
	links, _ := st.PendingLinks(ctx)
	if len(links) != 1 || links[0].Claim != "octo" || links[0].Subject != "2002" || links[0].State != store.LinkLinked {
		t.Fatalf("review list = %+v", links)
	}
	octo, _ := st.UserByName(ctx, "octo@gh")
	if err := st.RejectLink(ctx, "octo@gh"); err != nil {
		t.Fatal(err)
	}
	if toks, _ := st.ListUserTokens(ctx, octo.ID); len(toks) != 0 {
		t.Fatalf("the rejected binding kept %d tokens", len(toks))
	}
	// The rejected subject does not get it back.
	if who := signIn(2002); who["name"] == "octo@gh" {
		t.Fatalf("the rejected subject bound it again: %v", who)
	}
	// A second wrong subject binds and is rejected too: both stay rejected.
	if who := signIn(3003); who["name"] != "octo@gh" {
		t.Fatalf("second impostor: %v", who)
	}
	if err := st.RejectLink(ctx, "octo@gh"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int{2002, 3003} {
		if who := signIn(id); who["name"] == "octo@gh" {
			t.Fatalf("rejected subject %d bound it again: %v", id, who)
		}
	}
	// The real owner does.
	if who := signIn(1001); who["name"] != "octo@gh" {
		t.Fatalf("the real owner: %v", who)
	}
	if u, err := st.UserBySubject(ctx, "gh", "1001"); err != nil || u.Name != "octo@gh" {
		t.Fatalf("bound = %+v %v", u, err)
	}
}

// ldapServer is a server on st whose mock-ldap directory is the given YAML.
func ldapServer(t *testing.T, st *store.Store, directory string) http.Handler {
	t.Helper()
	p := filepath.Join(t.TempDir(), "directory.yaml")
	if err := os.WriteFile(p, []byte(directory), 0o600); err != nil {
		t.Fatal(err)
	}
	a := config.Auth{Providers: []config.AuthProvider{{Name: "corp", Type: "mock-ldap", File: p}}}
	eng := engine.New(config.Config{Addr: "127.0.0.1:8090", Auth: a}, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	return New(eng, st, nil, "127.0.0.1:8090", "", nil)
}

// LDAP accounts are keyed on the entry's stable id: a binding made under the
// typed username is re-keyed on the next sign-in; a case variant of the
// username is the same person; a username reassigned to another entry is not.
func TestLDAPAccountsAreKeyedOnTheEntrysStableID(t *testing.T) {
	_, st := testServerAuth(t, "127.0.0.1:8090", config.Auth{})
	ctx := context.Background()
	// A binding from before stable ids: subject = the typed username.
	ana := &store.User{Name: "ana@corp", Role: RoleSuperadmin}
	if err := st.CreateUser(ctx, ana); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.SetSubject(ctx, ana.ID, "corp", "ana"); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	h := ldapServer(t, st, "users:\n  - username: ana\n    password: ana-long-password\n    id: A1\n")
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	for _, typed := range []string{"ana", "ANA", "Ana"} {
		if w := providerSignIn(h, "corp", typed, "ana-long-password"); w.Code != 200 {
			t.Fatalf("%s: %d %s", typed, w.Code, w.Body)
		}
	}
	u, err := st.UserBySubject(ctx, "corp", "entryUUID:a1")
	if err != nil || u.ID != ana.ID || u.Role != RoleSuperadmin {
		t.Fatalf("re-keyed = %+v %v", u, err)
	}
	if !strings.Contains(logs.String(), "AUDIT account ana@corp re-keyed to directory id entryUUID:a1 on sign-in as ana") ||
		strings.Count(logs.String(), "re-keyed") != 1 {
		t.Fatalf("re-key audit line: %s", logs.String())
	}
	if users, _ := st.ListUsers(ctx); len(users) != 1 {
		t.Fatalf("a case variant made another account: %d users", len(users))
	}
	// The username "ana" now belongs to a different directory entry.
	h2 := ldapServer(t, st, "users:\n  - username: ana\n    password: new-ana-password\n    id: B2\n")
	if w := providerSignIn(h2, "corp", "ana", "new-ana-password"); w.Code != 200 {
		t.Fatalf("new ana: %d %s", w.Code, w.Body)
	}
	newAna, err := st.UserBySubject(ctx, "corp", "entryUUID:b2")
	if err != nil || newAna.ID == ana.ID || newAna.Role == RoleSuperadmin {
		t.Fatalf("the reassigned username got the old account: %+v %v", newAna, err)
	}
}

// Every role-sync grant and revoke that changes something is in the audit
// log; a sign-in that changes nothing writes none.
func TestRoleSyncChangesAreAudited(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	h, st := oidcServerStore(t, op)
	if err := st.CreateUser(context.Background(), &store.User{Name: "root", Role: RoleSuperadmin}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	oauthFlow(t, h, "sso", nil)
	if !strings.Contains(logs.String(), "AUDIT role sync (sso): granted superadmin to bob@sso") {
		t.Fatalf("no grant line: %s", logs.String())
	}
	logs.Reset()
	oauthFlow(t, h, "sso", nil)
	if strings.Contains(logs.String(), "AUDIT role sync") {
		t.Fatalf("an unchanged sign-in was audited: %s", logs.String())
	}
	op.userinfo = map[string]any{"preferred_username": "bob", "groups": []string{}}
	oauthFlow(t, h, "sso", nil)
	if !strings.Contains(logs.String(), "AUDIT role sync (sso): revoked superadmin from bob@sso") {
		t.Fatalf("no revoke line: %s", logs.String())
	}
}

// Addresses handed out (worker join, hook and MCP URLs, device links) come
// from the public URL, never from the request's Host or X-Forwarded-*.
func TestHandedOutAddressesUseThePublicURL(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	path := filepath.Join(t.TempDir(), "w.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for public, want := range map[string]string{testPublicURL: testPublicURL, "": "http://" + remoteAddr} {
		eng := engine.New(config.Config{Addr: remoteAddr, Auth: ssoConfig(op), PublicURL: public}, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
		s := &Server{eng: eng, addr: remoteAddr}
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = "evil.example"
		r.Header.Set("X-Forwarded-Host", "evil.example")
		r.Header.Set("X-Forwarded-Proto", "https")
		if got := s.publicURL(r); got != want {
			t.Errorf("public %q: worker address %q, want %q", public, got, want)
		}
		if got := s.externalBase(r); got != want {
			t.Errorf("public %q: external base %q, want %q", public, got, want)
		}
	}
}

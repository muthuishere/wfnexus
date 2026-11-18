package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/issuer"
)

const testIss, testAud = "https://issuer.example.test", "product-a"

type accountMinter struct{ key *ecdsa.PrivateKey }

func (m *accountMinter) token(t *testing.T, mut func(map[string]any)) string {
	t.Helper()
	now := time.Now()
	c := map[string]any{"iss": testIss, "aud": testAud, "sub": "acct-1", "sid": "s-1", "tid": "ext-1",
		"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(8 * time.Minute).Unix(), "auth_time": now.Add(-10 * time.Minute).Unix()}
	if mut != nil {
		mut(c)
	}
	enc := func(v any) string { raw, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(raw) }
	in := enc(map[string]any{"alg": "ES256", "kid": "k1", "typ": "at+jwt"}) + "." + enc(c)
	d := sha256.Sum256([]byte(in))
	r, s, err := ecdsa.Sign(rand.Reader, m.key, d[:])
	if err != nil {
		t.Fatal(err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// withIssuer installs a test verifier for the servers this test builds.
func withIssuer(t *testing.T) *accountMinter {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keys := issuer.KeySet{"k1": &k.PublicKey}
	issuerForTest = &issuer.Verifier{Issuer: testIss, Audience: testAud,
		Keys: func(string) (issuer.KeySet, error) { return keys, nil }}
	t.Cleanup(func() { issuerForTest = nil })
	return &accountMinter{key: k}
}

func TestAnIssuerAccountReadsOnlyItsLinkedTenant(t *testing.T) {
	m := withIssuer(t)
	f, call := newTenantFixture(t)
	acct := m.token(t, nil)
	// Not linked yet: no subject.
	if c, _ := call("GET", "/api/svc/self", acct, nil); c != 401 {
		t.Fatalf("an unlinked tid resolved: %d", c)
	}
	if c, b := call("PUT", "/api/tenants/acme/issuer-links", f.admin, map[string]string{"externalTenant": "ext-1"}); c != 200 {
		t.Fatalf("link: %d %s", c, b)
	}
	if c, _ := call("PUT", "/api/tenants/globex/issuer-links", f.admin, map[string]string{"externalTenant": "ext-1"}); c != 409 {
		t.Fatalf("linking one external tenant to two tenants: %d", c)
	}
	c, b := call("GET", "/api/svc/self", acct, nil)
	if c != 200 || !strings.Contains(b, `"tenant":"acme"`) || !strings.Contains(b, `"kind":"account"`) {
		t.Fatalf("self: %d %s", c, b)
	}
	c, b = call("GET", "/api/svc/runs?tenant=globex", acct, nil)
	if c != 200 || !strings.Contains(b, f.runA) || strings.Contains(b, f.runB) {
		t.Fatalf("runs: %d %s", c, b)
	}
	cx, bx := call("GET", "/api/svc/runs/"+f.runB, acct, nil)
	ca, ba := call("GET", "/api/svc/runs/00000000-0000-4000-8000-000000000000", acct, nil)
	if cx != 404 || cx != ca || bx != ba {
		t.Fatalf("cross-tenant %d %q vs absent %d %q", cx, bx, ca, ba)
	}
	// A token whose tid is linked to nothing, or that has no tid: refused.
	for _, tok := range []string{
		m.token(t, func(c map[string]any) { c["tid"] = "ext-2" }),
		m.token(t, func(c map[string]any) { delete(c, "tid") }),
	} {
		if c, _ := call("GET", "/api/svc/self", tok, nil); c != 401 {
			t.Fatalf("token resolved without a linked tid: %d", c)
		}
	}
}

// Whatever the token claims, an account is confined to the /api/svc/ reads:
// a role claim grants nothing and it starts no run.
func TestAnIssuerAccountGetsNothingFromItsClaims(t *testing.T) {
	m := withIssuer(t)
	serviceRunsForTest = true
	t.Cleanup(func() { serviceRunsForTest = false })
	f, call := tenantRunServer(t)
	if c, _ := call("PUT", "/api/tenants/acme/issuer-links", f.admin, map[string]string{"externalTenant": "ext-1"}); c != 200 {
		t.Fatal("link")
	}
	acct := m.token(t, func(c map[string]any) { c["role"] = "superadmin" })
	for _, rt := range [][2]string{
		{"GET", "/api/users"}, {"GET", "/api/runs"}, {"GET", "/api/whoami"}, {"GET", "/api/tenants"},
		{"GET", "/api/workflows"}, {"POST", "/api/workflows/wf-a/runs"},
	} {
		if c, _ := call(rt[0], rt[1], acct, map[string]any{}); c != 403 && c != 401 && c != 404 {
			t.Fatalf("%s %s served an account: %d", rt[0], rt[1], c)
		}
	}
	if c, _ := call("POST", "/api/svc/workflows/wf-a/runs", acct, map[string]any{}); c != 404 {
		t.Fatalf("an account started a run: %d", c)
	}
	if n := runsOf(t, f.st, "wf-a"); n != 0 {
		t.Fatalf("%d runs started by an account", n)
	}
}

// A revocation refuses every token from a sign-in before it, and is scoped
// to an external tenant linked to the tenant revoking.
func TestARevokedAccountIsRefused(t *testing.T) {
	m := withIssuer(t)
	f, call := newTenantFixture(t)
	call("PUT", "/api/tenants/acme/issuer-links", f.admin, map[string]string{"externalTenant": "ext-1"})
	acct := m.token(t, nil)
	if c, _ := call("GET", "/api/svc/self", acct, nil); c != 200 {
		t.Fatalf("before revoke: %d", c)
	}
	if c, _ := call("POST", "/api/tenants/globex/issuer-revocations", f.admin, map[string]string{"externalTenant": "ext-1", "subject": "acct-1"}); c != 404 {
		t.Fatalf("revoking another tenant's account: %d", c)
	}
	if c, b := call("POST", "/api/tenants/acme/issuer-revocations", f.admin, map[string]string{"externalTenant": "ext-1", "subject": "acct-1"}); c != 200 {
		t.Fatalf("revoke: %d %s", c, b)
	}
	if c, _ := call("GET", "/api/svc/self", acct, nil); c != 401 {
		t.Fatalf("a revoked sign-in still works: %d", c)
	}
	// A NEW sign-in after the revocation works again.
	time.Sleep(1100 * time.Millisecond)
	fresh := m.token(t, func(c map[string]any) { c["auth_time"] = time.Now().Unix() })
	if c, _ := call("GET", "/api/svc/self", fresh, nil); c != 200 {
		t.Fatalf("a sign-in after the revocation: %d", c)
	}
	// Another account of the same tenant was never affected.
	other := m.token(t, func(c map[string]any) { c["sub"] = "acct-2" })
	if c, _ := call("GET", "/api/svc/self", other, nil); c != 200 {
		t.Fatalf("another account: %d", c)
	}
}

func TestATokenIssuerIsAllOrNothing(t *testing.T) {
	for _, c := range []config.TokenIssuer{
		{Issuer: testIss},
		{Issuer: testIss, Audience: testAud},
		{Audience: testAud, JWKSURL: "https://issuer.example.test/jwks"},
		{Issuer: testIss, Audience: testAud, JWKSURL: "http://issuer.example.test/jwks"},
	} {
		if err := CheckTokenIssuer(c); err == nil {
			t.Errorf("%+v accepted", c)
		}
		if newIssuerVerifier(c) != nil {
			t.Errorf("%+v built a verifier", c)
		}
	}
	if err := CheckTokenIssuer(config.TokenIssuer{}); err != nil {
		t.Errorf("none: %v", err)
	}
	// Off by default: with nothing configured there is no verifier, so no
	// ES256 token ever reaches the issuer path.
	if newIssuerVerifier(config.TokenIssuer{}) != nil {
		t.Error("no configuration built a verifier: external issuer tokens must be off by default")
	}
	ok := config.TokenIssuer{Issuer: testIss, Audience: testAud, JWKSURL: "https://issuer.example.test/jwks"}
	if err := CheckTokenIssuer(ok); err != nil || newIssuerVerifier(ok) == nil {
		t.Errorf("complete config: %v", err)
	}
}

// A revocation that cannot be READ revokes: with the revocation table gone
// (the link still readable), a valid token is refused.
func TestAnUnreadableRevocationFailsClosed(t *testing.T) {
	m := withIssuer(t)
	f, call := newTenantFixture(t)
	call("PUT", "/api/tenants/acme/issuer-links", f.admin, map[string]string{"externalTenant": "ext-1"})
	acct := m.token(t, nil)
	if c, _ := call("GET", "/api/svc/self", acct, nil); c != 200 {
		t.Fatalf("before the fault: %d", c)
	}
	if _, err := f.st.DB().Exec(`DROP TABLE issuer_revocations`); err != nil {
		t.Fatal(err)
	}
	if c, _ := call("GET", "/api/svc/self", acct, nil); c != 401 {
		t.Fatalf("a token was accepted while revocations could not be read: %d", c)
	}
}

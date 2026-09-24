package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

func decode(t *testing.T, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %s", res.Body)
	}
	return m
}

var userCodeShape = regexp.MustCompile(`^[BCDFGHJKLMNPQRSTVWXZ]{4}-[BCDFGHJKLMNPQRSTVWXZ]{4}$`)

// task 4.1 — RFC 8628 §3.1-3.2, the shape of the authorization response.
func TestDeviceAuthorizationResponse(t *testing.T) {
	h, _ := testServer(t, "127.0.0.1:8090")

	res := do(h, "POST", "/api/device/code", "", map[string]string{"client_id": "wfx-cli", "hostname": "laptop"})
	if res.Code != 200 {
		t.Fatalf("%d %s", res.Code, res.Body)
	}
	got := decode(t, res)
	for _, k := range []string{"device_code", "user_code", "verification_uri", "verification_uri_complete", "expires_in", "interval"} {
		if got[k] == nil {
			t.Fatalf("§3.2 response is missing %q: %s", k, res.Body)
		}
	}
	if !userCodeShape.MatchString(got["user_code"].(string)) {
		t.Fatalf("user_code is not 8 characters of the §6.1 alphabet as XXXX-XXXX: %v", got["user_code"])
	}
	if got["interval"].(float64) != 5 || got["expires_in"].(float64) != 900 {
		t.Fatalf("pacing: %v %v", got["interval"], got["expires_in"])
	}
	if !strings.HasSuffix(got["verification_uri"].(string), "/device") {
		t.Fatalf("verification_uri: %v", got["verification_uri"])
	}
}

// task 4.2 — pending → approved → token, and then the code is gone (§3.5).
func TestDeviceGrantWalksPendingApprovedIssued(t *testing.T) {
	h, st := testServer(t, "127.0.0.1:8090")
	ctx := context.Background()

	start := decode(t, do(h, "POST", "/api/device/code", "", map[string]string{"client_id": "wfx-cli"}))
	deviceCode := start["device_code"].(string)
	userCode := start["user_code"].(string)

	poll := func() map[string]any {
		return decode(t, do(h, "POST", "/api/device/token", "", map[string]string{
			"grant_type": deviceGrantType, "device_code": deviceCode}))
	}
	if e := poll()["error"]; e != "authorization_pending" {
		t.Fatalf("first poll: %v", e)
	}
	// §3.5: a poll inside the interval is told to slow down, not refused.
	if e := poll()["error"]; e != "slow_down" {
		t.Fatalf("an impatient poll got %v, want slow_down", e)
	}

	// The human approves. On loopback there is no subject, so the approval
	// names the identity it is creating.
	res := do(h, "POST", "/api/device/verify", "", map[string]string{"user_code": userCode, "user": "muthu"})
	if res.Code != 200 {
		t.Fatalf("approve: %d %s", res.Code, res.Body)
	}

	// Wait out the interval so the next poll is not slow_down.
	time.Sleep(0)
	if _, err := st.TouchDevicePoll(ctx, normalizeUserCode(userCode), 0); err != nil {
		t.Fatal(err)
	}
	issued := poll()
	value, _ := issued["access_token"].(string)
	if !strings.HasPrefix(value, "wfx_") || issued["token_type"] != "Bearer" {
		t.Fatalf("no token issued: %v", issued)
	}
	// It authenticates.
	if r := do(h, "GET", "/api/whoami", value, nil); r.Code != 200 || decode(t, r)["name"] != "muthu" {
		t.Fatalf("the issued token does not authenticate: %d %s", r.Code, r.Body)
	}
	// And the device code is single use: a second poll gets nothing.
	if e := poll()["error"]; e != "expired_token" {
		t.Fatalf("a device code was reusable: %v", e)
	}
	if _, err := st.DeviceCodeByUserCode(ctx, normalizeUserCode(userCode)); err != store.ErrNotFound {
		t.Fatalf("the device code row survived the issue: %v", err)
	}
}

func TestDeviceGrantRefusesAnExpiredCode(t *testing.T) {
	h, st := testServer(t, "127.0.0.1:8090")
	value := auth.NewToken()
	if err := st.CreateDeviceCode(context.Background(), auth.HashToken(value), "BCDFGHJK", "wfx-cli", "", "",
		time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	got := decode(t, do(h, "POST", "/api/device/token", "", map[string]string{"device_code": value}))
	if got["error"] != "expired_token" {
		t.Fatalf("an expired code answered %v", got["error"])
	}
}

func TestDeviceGrantRefusesADeniedCode(t *testing.T) {
	h, _ := testServer(t, "127.0.0.1:8090")
	start := decode(t, do(h, "POST", "/api/device/code", "", map[string]string{}))
	do(h, "POST", "/api/device/verify", "", map[string]string{"user_code": start["user_code"].(string), "action": "deny"})
	got := decode(t, do(h, "POST", "/api/device/token", "", map[string]string{"device_code": start["device_code"].(string)}))
	if got["error"] != "access_denied" {
		t.Fatalf("a denied code answered %v", got["error"])
	}
}

// [SEC-TEST] task 4.3 — RFC 8628 §5.2: the verification endpoint is rate
// limited per source, and a code submitted and refused five times is burned.
func TestVerificationEndpointIsRateLimitedAndBurnsTheCode(t *testing.T) {
	h, st := testServer(t, "0.0.0.0:8090")
	ctx := context.Background()

	start := decode(t, do(h, "POST", "/api/device/code", "", map[string]string{}))
	userCode := start["user_code"].(string)

	// Off loopback an unauthenticated approval cannot resolve an approver, so
	// each of these is a refused submission against a REAL code.
	for i := 1; i <= verifyMaxFails; i++ {
		res := do(h, "POST", "/api/device/verify", "", map[string]string{"user_code": userCode})
		if res.Code == 200 {
			t.Fatalf("attempt %d approved without an approver", i)
		}
	}
	// Five failures burn it.
	if _, err := st.DeviceCodeByUserCode(ctx, normalizeUserCode(userCode)); err != store.ErrNotFound {
		t.Fatalf("the code survived %d failures: %v", verifyMaxFails, err)
	}
	// And the next attempt from the same source is refused before the code is
	// even looked at.
	res := do(h, "POST", "/api/device/verify", "", map[string]string{"user_code": userCode})
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("attempt %d got %d, want 429", verifyPerMinute+1, res.Code)
	}
}

// task 4.4 — the verification page is served, and the SPA fallback still wins
// everywhere else.
func TestDevicePageIsServedOutsideTheAPI(t *testing.T) {
	h, _ := testServer(t, "127.0.0.1:8090")
	res := do(h, "GET", "/device?user_code=BCDF-GHJK", "", nil)
	if res.Code != 200 || !strings.Contains(res.Body.String(), "Approve a sign-in") {
		t.Fatalf("/device: %d %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "BCDF-GHJK") {
		t.Fatal("§3.3.1's verification_uri_complete did not prefill the code")
	}
	// No UI bundle is configured in this test, so every other path 404s —
	// which is the SPA fallback still being the SPA fallback.
	if res := do(h, "GET", "/runs/123", "", nil); res.Code != 404 {
		t.Fatalf("the SPA route stopped resolving: %d", res.Code)
	}
}

// [SEC-TEST] the issued value is shown once and is nowhere else. It is not in
// the store, and it is not in anything the server said about it afterwards.
func TestAnIssuedTokenValueIsNotRecoverable(t *testing.T) {
	h, st := testServer(t, "127.0.0.1:8090")
	ctx := context.Background()

	start := decode(t, do(h, "POST", "/api/device/code", "", map[string]string{}))
	userCode := start["user_code"].(string)
	do(h, "POST", "/api/device/verify", "", map[string]string{"user_code": userCode, "user": "muthu"})
	if _, err := st.TouchDevicePoll(ctx, normalizeUserCode(userCode), 0); err != nil {
		t.Fatal(err)
	}
	issued := decode(t, do(h, "POST", "/api/device/token", "", map[string]string{
		"device_code": start["device_code"].(string)}))
	value := issued["access_token"].(string)

	u, err := st.UserByName(ctx, "muthu")
	if err != nil {
		t.Fatal(err)
	}
	list, err := st.ListUserTokens(ctx, u.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("tokens: %v %v", list, err)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), value) {
		t.Fatalf("listing a token returned its value: %s", raw)
	}
	// whoami is the one route that speaks about the caller; it must not echo
	// the credential either.
	who := do(h, "GET", "/api/whoami", value, nil)
	if strings.Contains(who.Body.String(), value) {
		t.Fatalf("whoami echoed the credential: %s", who.Body)
	}
}

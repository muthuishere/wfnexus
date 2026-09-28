package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
)

// Browser sessions (ADR 0022).
//
// A browser cannot hold a bearer the way the CLI's contexts file does, so it
// holds the SAME kind of user token in an HttpOnly cookie instead: minted
// here, stored as its hash like every other token, resolved by the one
// middleware, revoked by the one revoke. There are exactly two ways to get
// one, and both are the device grant, with the human's approval coming from a
// terminal that is already signed in:
//
//   - The UI's sign-in card starts a grant (client_id wfx-ui), shows the user
//     code, and polls /api/auth/session. The person runs
//     `wfx login --approve <code>` (or approves at /device from any signed-in
//     browser); the next poll is answered with the cookie.
//   - `wfx ui` asks /api/auth/link for a code that is ALREADY approved by the
//     caller, single use and 60 seconds long, and opens /auth/browser?code=….
//     That request is answered with the cookie and a redirect to /.
//
// Neither ever puts a token value in a URL, a page, or a log. The code in the
// link is not a token: it can be redeemed once, within a minute, for a cookie,
// and a device code of either client is refused by /api/device/token so it
// can never be turned into a bearer handed to a script.

const (
	sessionCookieName = "wfx_session"
	// uiClient is a grant started by the UI's sign-in card; browserLinkClient
	// is a pre-approved one-time link from `wfx ui`.
	uiClient          = "wfx-ui"
	browserLinkClient = "wfx-browser-link"
	browserLinkSec    = 60
	// A browser session is a token like any other; the cookie lives this long
	// and the row until logout or an admin revokes it.
	sessionMaxAge = 30 * 24 * time.Hour
)

func sessionCookie(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, value string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: value, Path: "/",
		MaxAge: int(sessionMaxAge / time.Second), HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: secureRequest(r),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secureRequest(r),
	})
}

// sameOrigin is the CSRF check for cookie-authenticated requests. SameSite=Strict
// already keeps the cookie off cross-site requests in every current browser;
// this is the second lock, for the cases SameSite does not cover (a sibling
// subdomain is same-SITE, and older browsers). Safe methods pass; anything that
// changes state must carry an Origin naming this host, or — when a browser sent
// no Origin — Sec-Fetch-Site: same-origin.
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" {
			return false
		}
		if strings.EqualFold(u.Host, r.Host) {
			return true
		}
		// Behind a proxy that rewrites Host, the host the browser used is
		// the forwarded one.
		fh := r.Header.Get("X-Forwarded-Host")
		return fh != "" && strings.EqualFold(u.Host, fh)
	}
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// startSession mints a user token for a browser and hands it over as a cookie.
// The value exists only in the Set-Cookie header; the row holds its hash.
func (s *Server) startSession(ctx context.Context, w http.ResponseWriter, r *http.Request, userID uuid.UUID) error {
	value := auth.NewToken()
	if _, err := s.store.IssueUserToken(ctx, userID, auth.HashToken(value), "browser", ""); err != nil {
		return err
	}
	setSessionCookie(w, r, value)
	return nil
}

// browserSession is /api/device/token for a browser: the UI polls it with the
// device code of a grant it started (client_id wfx-ui), and an approved grant
// is answered with a session cookie rather than a bearer value.
func (s *Server) browserSession(w http.ResponseWriter, r *http.Request) {
	p := deviceParams(r)
	code := p["device_code"]
	if code == "" {
		deviceError(w, http.StatusBadRequest, "invalid_request", "device_code is required")
		return
	}
	ctx := r.Context()
	dc, err := s.store.DeviceCodeByHash(ctx, auth.HashToken(code))
	if err != nil || dc.ClientID != uiClient {
		deviceError(w, http.StatusBadRequest, "expired_token", "this code is no longer valid")
		return
	}
	if time.Now().UTC().After(dc.ExpiresAt) {
		_ = s.store.DeleteDeviceCodeByUserCode(ctx, dc.UserCode)
		deviceError(w, http.StatusBadRequest, "expired_token", "the code expired before it was approved")
		return
	}
	switch dc.Status {
	case "denied":
		_ = s.store.DeleteDeviceCodeByUserCode(ctx, dc.UserCode)
		deviceError(w, http.StatusBadRequest, "access_denied", "the sign-in was denied")
		return
	case "approved":
		if dc.ApprovedBy == nil {
			deviceError(w, http.StatusBadRequest, "access_denied", "the approval named no user")
			return
		}
		if won, err := s.store.ClaimDeviceCode(ctx, dc.UserCode); err != nil || !won {
			deviceError(w, http.StatusBadRequest, "expired_token", "this code is no longer valid")
			return
		}
		if err := s.startSession(ctx, w, r, *dc.ApprovedBy); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "signed_in"})
		return
	}
	if tooFast, err := s.store.TouchDevicePoll(ctx, dc.UserCode, deviceInterval); err == nil && tooFast {
		deviceError(w, http.StatusBadRequest, "slow_down", "polling faster than the interval")
		return
	}
	deviceError(w, http.StatusBadRequest, "authorization_pending", "waiting for the code to be approved")
}

// browserLink is what `wfx ui` calls: a signed-in PERSON asks for a one-time
// link that signs a browser in as themselves. The code is created already
// approved by the caller, lives 60 seconds, and is deleted on first use.
//
// On a loopback server with no credential there is nothing to sign in to, so
// the answer is the plain UI address.
func (s *Server) browserLink(w http.ResponseWriter, r *http.Request) {
	sub, ok := SubjectFrom(r.Context())
	if !ok {
		if s.loopbackOnly {
			writeJSON(w, http.StatusOK, map[string]any{"url": s.externalBase(r) + "/", "expires_in": 0})
			return
		}
		writeErr(w, http.StatusUnauthorized, errors.New("unauthenticated"))
		return
	}
	if sub.Kind != SubjectUser || sub.User == nil {
		writeErr(w, http.StatusForbidden, errors.New("a machine identity cannot open a browser session"))
		return
	}
	ctx := r.Context()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	code := hex.EncodeToString(raw)
	userCode := newUserCode()
	expires := time.Now().UTC().Add(browserLinkSec * time.Second)
	if err := s.store.CreateDeviceCode(ctx, auth.HashToken(code), userCode, browserLinkClient, "", "", expires); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.ApproveDeviceCode(ctx, userCode, sub.User.ID); err != nil {
		_ = s.store.DeleteDeviceCodeByUserCode(ctx, userCode)
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"url":        s.externalBase(r) + "/auth/browser?code=" + code,
		"expires_in": browserLinkSec,
	})
}

// browserRedeem is where the one-time link lands. Success is a cookie and a
// redirect to /, so the code never stays in the address bar or history entry
// the person looks at; anything else is a short page saying to run it again.
func (s *Server) browserRedeem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	ctx := r.Context()
	code := r.URL.Query().Get("code")
	fail := func() {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(browserLinkGoneHTML))
	}
	if code == "" {
		fail()
		return
	}
	dc, err := s.store.DeviceCodeByHash(ctx, auth.HashToken(code))
	if err != nil || dc.ClientID != browserLinkClient || dc.Status != "approved" || dc.ApprovedBy == nil {
		fail()
		return
	}
	// Claimed BEFORE the expiry check: an expired link is burned too.
	won, err := s.store.ClaimDeviceCode(ctx, dc.UserCode)
	if err != nil || !won || time.Now().UTC().After(dc.ExpiresAt) {
		fail()
		return
	}
	if err := s.startSession(ctx, w, r, *dc.ApprovedBy); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

const browserLinkGoneHTML = `<!doctype html>
<meta charset="utf-8"><title>wfx — link expired</title>
<style>body{font:15px/1.5 ui-sans-serif,system-ui,sans-serif;max-width:32rem;margin:6rem auto;padding:0 1rem}</style>
<h1>This sign-in link has expired</h1>
<p>A link from <code>wfx ui</code> works once, within a minute. Run
<code>wfx ui</code> again in your terminal for a fresh one.</p>
`

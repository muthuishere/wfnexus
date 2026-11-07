package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
)

// Username and password sign-in (ADR 0025).
//
// A password is one more way to reach the SAME browser session ADR 0022
// defines: success is startSession, so the cookie, the token row, revocation
// and the CSRF rule are all the ones that already exist. The device grant is
// untouched beside it.
//
// The rules this file defends, each with a test:
//   - an unknown username, a disabled user, a user with no password and a
//     wrong password get the SAME status and the SAME body;
//   - attempts are limited per source address AND per username, with the
//     device grant's limiter (device.go), so neither spraying one account from
//     many addresses nor many accounts from one address gets far;
//   - a password is at least auth.MinPasswordLen characters;
//   - a user marked must_change can do nothing but change it;
//   - no password is ever logged, echoed or returned.

// errBadCredentials is the ONE refusal a sign-in gets.
var errBadCredentials = errors.New("invalid username or password")

// passwordLimiter is two keyspaces in the device grant's fixed-window limiter.
type passwordLimiter struct{ byIP, byUser *verifyLimiter }

func newPasswordLimiter() *passwordLimiter {
	return &passwordLimiter{byIP: newVerifyLimiter(), byUser: newVerifyLimiter()}
}

// allow counts every attempt against the source address, and refuses a
// username that has already failed its window's worth. The per-username key
// counts FAILURES only (fail), so a person who signs in correctly a few times
// a minute is never locked out, while a spray across many addresses at one
// account is.
func (l *passwordLimiter) allow(ip, user string, now time.Time) bool {
	okIP := l.byIP.allow(ip, now)
	return okIP && !l.byUser.full(strings.ToLower(user), now)
}

// fail records a refused attempt against the username.
func (l *passwordLimiter) fail(user string, now time.Time) {
	l.byUser.allow(strings.ToLower(user), now)
}

type passwordBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// Client "cli" asks for the tokens in the body (`wfx login --user`)
	// instead of cookies (ADR 0028). A browser never sets it, so its refresh
	// value never reaches page script.
	Client string `json:"client"`
}

// passwordSignIn is POST /api/auth/password.
func (s *Server) passwordSignIn(w http.ResponseWriter, r *http.Request) {
	// The form is offered only where auth is required; a loopback install
	// has nothing to sign in to (ADR 0017).
	if s.loopbackOnly {
		writeErr(w, http.StatusNotFound, errors.New("sign-in is not used on a loopback install"))
		return
	}
	var b passwordBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("expected {username, password}"))
		return
	}
	b.Username = strings.TrimSpace(b.Username)
	if !s.pwLimit.allow(sourceOf(r), b.Username, time.Now()) {
		writeErr(w, http.StatusTooManyRequests, errors.New("too many attempts; wait a minute"))
		return
	}
	ctx := r.Context()
	u, hash, err := s.store.PasswordHashByName(ctx, b.Username)
	if err != nil {
		hash = "" // unknown or disabled: verified against nothing, at full cost
	}
	if !auth.VerifyPassword(hash, b.Password) || u == nil {
		s.pwLimit.fail(b.Username, time.Now())
		writeErr(w, http.StatusUnauthorized, errBadCredentials)
		return
	}
	if b.Client == "cli" {
		sess, err := s.issueSession(ctx, u.ID, "password")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out := tokenResponse(sess)
		out["status"], out["mustChange"] = "signed_in", u.MustChange
		writeJSON(w, http.StatusOK, out)
		return
	}
	if err := s.startSession(ctx, w, r, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "signed_in", "mustChange": u.MustChange})
}

// changeOwnPassword is POST /api/auth/password/change: the signed-in person
// replaces their own password, proving the current one. It is the only thing
// a must_change user may do besides whoami and sign out.
func (s *Server) changeOwnPassword(w http.ResponseWriter, r *http.Request) {
	sub, ok := SubjectFrom(r.Context())
	if !ok || sub.Kind != SubjectUser || sub.User == nil {
		writeErr(w, http.StatusUnauthorized, errors.New("sign in first"))
		return
	}
	var b struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("expected {current, new}"))
		return
	}
	if !s.pwLimit.allow(sourceOf(r), sub.User.Name, time.Now()) {
		writeErr(w, http.StatusTooManyRequests, errors.New("too many attempts; wait a minute"))
		return
	}
	ctx := r.Context()
	_, hash, err := s.store.PasswordHashByName(ctx, sub.User.Name)
	if err != nil || !auth.VerifyPassword(hash, b.Current) {
		s.pwLimit.fail(sub.User.Name, time.Now())
		writeErr(w, http.StatusForbidden, errors.New("the current password is wrong"))
		return
	}
	if err := auth.ValidatePassword(b.New); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if b.New == b.Current {
		writeErr(w, http.StatusBadRequest, errors.New("the new password must differ from the current one"))
		return
	}
	h, err := auth.HashPassword(b.New)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.SetPassword(ctx, sub.User.Name, h, false); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": true})
}

// setUserPassword is POST /api/users/{name}/password, an admin's reset. With
// no password in the body one is generated and returned ONCE. Either way the
// user must change it at the next sign-in.
func (s *Server) setUserPassword(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	var b struct {
		Password string `json:"password"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&b); err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("expected {password}"))
			return
		}
	}
	generated := b.Password == ""
	if generated {
		b.Password = auth.NewPassword()
	} else if err := auth.ValidatePassword(b.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	h, err := auth.HashPassword(b.Password)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.SetPassword(r.Context(), name, h, true); err != nil {
		notFound(w)
		return
	}
	out := map[string]any{"user": name, "mustChange": true}
	if generated {
		out["password"] = b.Password
	}
	writeJSON(w, http.StatusOK, out)
}

// mustChangeAllowed is everything a user who must change their password may
// still reach.
var mustChangeAllowed = map[string]bool{
	"/api/whoami":               true,
	"/api/tokens/self":          true,
	"/api/auth/password/change": true,
	"/api/health":               true,
}

// passwordChangeRequired reports whether this request is refused because the
// subject has a password they must replace first.
func passwordChangeRequired(r *http.Request) bool {
	sub, ok := SubjectFrom(r.Context())
	if !ok || sub.Kind != SubjectUser || sub.User == nil || !sub.User.MustChange {
		return false
	}
	return !mustChangeAllowed[r.URL.Path]
}

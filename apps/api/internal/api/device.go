package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// The OAuth 2.0 Device Authorization Grant, RFC 8628, adopted whole.
//
// Not a redirect flow, because authoring happens inside the author's own agent
// session: over SSH, in a container, on somebody else's machine. There is no
// browser to redirect and no port that can be called back (RFC 8628 §1). The
// client prints a code and polls; the human approves from wherever they do
// have a browser. That property is the entire reason for the flow.

const (
	// RFC 8628 §6.1 recommends a base-20 alphabet with no vowels (so no code
	// spells a word) and no digits that collide with letters (0/O, 1/I).
	// 20^8 ≈ 2.56e10 for the 8 characters below.
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	userCodeLen      = 8

	// §3.2's default polling interval, in seconds, and the lifetime of a code.
	// The RFC gives no lifetime; 15 minutes is long enough to walk to another
	// machine and short enough that a code abandoned on a shared terminal is
	// worthless.
	deviceInterval  = 5
	deviceExpirySec = 900

	deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

	// §5.2: the verification endpoint is rate limited, and a code that has
	// been submitted and refused this many times is burned.
	verifyPerMinute = 5
	verifyMaxFails  = 5
)

// verifyLimiter is the §5.2 rate limit on the verification endpoint: a fixed
// window per source. It is in memory because it guards a 15-minute flow on one
// process; nothing here needs to survive a restart.
type verifyLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newVerifyLimiter() *verifyLimiter { return &verifyLimiter{hits: map[string][]time.Time{}} }

// allow records an attempt and reports whether it is within the window.
func (l *verifyLimiter) allow(source string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-time.Minute)
	kept := l.hits[source][:0]
	for _, t := range l.hits[source] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.hits[source] = append(kept, now)
	return len(l.hits[source]) <= verifyPerMinute
}

func sourceOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// newUserCode mints the code a human types. crypto/rand, because a predictable
// code is a login anyone can complete.
func newUserCode() string {
	b := make([]byte, 0, userCodeLen)
	max := big.NewInt(int64(len(userCodeAlphabet)))
	for i := 0; i < userCodeLen; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			// A failure here would otherwise silently produce a biased code.
			panic("device: no entropy for a user code: " + err.Error())
		}
		b = append(b, userCodeAlphabet[n.Int64()])
	}
	return string(b)
}

// formatUserCode is the display form, §6.1's readability note: the user types
// eight characters, so they are shown in two groups.
func formatUserCode(c string) string {
	if len(c) != userCodeLen {
		return c
	}
	return c[:4] + "-" + c[4:]
}

// normalizeUserCode accepts what a human actually types: any case, with or
// without the dash or spaces.
func normalizeUserCode(c string) string {
	c = strings.ToUpper(strings.TrimSpace(c))
	return strings.NewReplacer("-", "", " ", "", "\t", "").Replace(c)
}

// deviceParams reads either an RFC 8628 form body or the JSON one this
// codebase's own client finds natural. The RFC specifies form encoding; a
// server that also accepts JSON breaks nothing and saves the client a second
// encoder.
func deviceParams(r *http.Request) map[string]string {
	out := map[string]string{}
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) == nil {
			for k, v := range body {
				if s, ok := v.(string); ok {
					out[k] = s
				}
			}
		}
		return out
	}
	_ = r.ParseForm()
	for k := range r.Form {
		out[k] = r.Form.Get(k)
	}
	return out
}

// deviceAuthorize is RFC 8628 §3.1–3.2: the client asks, the server answers
// with the pair of codes and the pacing.
func (s *Server) deviceAuthorize(w http.ResponseWriter, r *http.Request) {
	p := deviceParams(r)
	clientID := p["client_id"]
	if clientID == "" {
		clientID = "wfx-cli"
	}
	userCode := newUserCode()
	deviceCode := auth.NewToken()
	expires := time.Now().UTC().Add(deviceExpirySec * time.Second)

	// Only the HASH is stored. For the length of the flow the device code is a
	// bearer credential, so it obeys the token storage rule.
	if err := s.store.CreateDeviceCode(r.Context(), auth.HashToken(deviceCode), userCode,
		clientID, p["hostname"], p["scope"], expires); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	verifyURI := s.externalBase(r) + "/device"
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               deviceCode,
		"user_code":                 formatUserCode(userCode),
		"verification_uri":          verifyURI,
		"verification_uri_complete": verifyURI + "?user_code=" + formatUserCode(userCode), // §3.3.1
		"expires_in":                deviceExpirySec,
		"interval":                  deviceInterval,
	})
}

// externalBase is the URL a human should open. The Host header is what the
// client reached us on, which is the only address we can know is routable from
// where the caller is.
func (s *Server) externalBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	host := r.Host
	if host == "" {
		host = s.addr
	}
	return scheme + "://" + host
}

// deviceToken is §3.4–3.5: the client polls here until a human decides.
func (s *Server) deviceToken(w http.ResponseWriter, r *http.Request) {
	p := deviceParams(r)
	if g := p["grant_type"]; g != "" && g != deviceGrantType {
		deviceError(w, http.StatusBadRequest, "unsupported_grant_type", "this endpoint implements RFC 8628 only")
		return
	}
	code := p["device_code"]
	if code == "" {
		deviceError(w, http.StatusBadRequest, "invalid_request", "device_code is required")
		return
	}
	ctx := r.Context()
	hash := auth.HashToken(code)
	dc, err := s.store.DeviceCodeByHash(ctx, hash)
	if err != nil {
		// Unknown or already consumed. §3.5: the device code is single use, so
		// a second poll after success is indistinguishable from a bad one.
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
		deviceError(w, http.StatusBadRequest, "access_denied", "the login was denied")
		return
	case "approved":
		if dc.ApprovedBy == nil {
			deviceError(w, http.StatusBadRequest, "access_denied", "the approval named no user")
			return
		}
		value := auth.NewToken()
		if _, err := s.store.IssueUserToken(ctx, *dc.ApprovedBy, auth.HashToken(value), "wfx-cli", ""); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		// §3.5: the device code MUST NOT be reused. The row is deleted rather
		// than marked, so reuse is impossible rather than merely refused.
		_ = s.store.DeleteDeviceCodeByUserCode(ctx, dc.UserCode)

		// The ONLY time the value exists outside the client. It is not logged
		// here and there is no route that can read it back.
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": value,
			"token_type":   "Bearer",
		})
		return
	}

	// Pending. §3.5: a client that polls faster than the interval is told to
	// slow down rather than refused.
	if tooFast, err := s.store.TouchDevicePoll(ctx, dc.UserCode, deviceInterval); err == nil && tooFast {
		deviceError(w, http.StatusBadRequest, "slow_down", "polling faster than the interval")
		return
	}
	deviceError(w, http.StatusBadRequest, "authorization_pending", "waiting for the code to be approved")
}

// deviceError is §3.5's error shape: an OAuth error object, never a bare
// message, so the client can branch on `error` rather than on prose.
func deviceError(w http.ResponseWriter, code int, kind, desc string) {
	writeJSON(w, code, map[string]any{"error": kind, "error_description": desc})
}

// deviceVerify is what the human's browser posts: approve or deny a code.
//
// It resolves its own subject rather than leaning on requireSubject, so that
// every submission — including one with no credential at all — is counted
// against the §5.2 limit before anything is decided.
func (s *Server) deviceVerify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.verify.allow(sourceOf(r), time.Now()) {
		// §5.2. Answered before the code is even looked at, so the limit costs
		// an attacker a request and tells them nothing.
		writeErr(w, http.StatusTooManyRequests, errors.New("too many attempts; wait a minute"))
		return
	}
	p := deviceParams(r)
	userCode := normalizeUserCode(p["user_code"])
	action := p["action"]
	if action == "" {
		action = "approve"
	}
	if _, err := s.store.DeviceCodeByUserCode(ctx, userCode); err != nil {
		// No row: nothing to burn. The per-source limit above is what stops a
		// guessing run, which is exactly what §5.2 asks for.
		writeErr(w, http.StatusBadRequest, errors.New("that code is not valid"))
		return
	}
	if action == "deny" {
		_ = s.store.DenyDeviceCode(ctx, userCode)
		writeJSON(w, 200, map[string]any{"status": "denied"})
		return
	}
	user, err := s.approvingUser(ctx, s.subjectOf(r), p["user"])
	if err != nil {
		s.burnOnFailure(ctx, userCode)
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	if err := s.store.ApproveDeviceCode(ctx, userCode, user.ID); err != nil {
		s.burnOnFailure(ctx, userCode)
		writeErr(w, http.StatusBadRequest, errors.New("that code can no longer be approved"))
		return
	}
	writeJSON(w, 200, map[string]any{"status": "approved", "user": user.Name})
}

// burnOnFailure counts a refused submission against the code and destroys it at
// the limit (§5.2, "burns the code"). A code that has been fumbled five times
// is a code somebody is guessing at.
func (s *Server) burnOnFailure(ctx context.Context, userCode string) {
	n, err := s.store.BumpDeviceAttempts(ctx, userCode)
	if err == nil && n >= verifyMaxFails {
		_ = s.store.DeleteDeviceCodeByUserCode(ctx, userCode)
	}
}

// approvingUser resolves who is granting the login.
//
// Off loopback it is the authenticated subject and nothing else — approval is
// the one place a person authorizes, and requireSubject has already refused an
// unauthenticated caller. On loopback there is no subject to have, so the
// approval names the user it is creating an identity for: the machine is the
// trust boundary there, and an explicit approval is not the "implicit user" the
// spec forbids (that rule is about requests being SERVED, not about a human
// deliberately creating the first account).
func (s *Server) approvingUser(ctx context.Context, sub *Subject, named string) (*store.User, error) {
	if sub != nil {
		if sub.Kind != SubjectUser {
			return nil, errors.New("a machine identity cannot approve a person's login")
		}
		return sub.User, nil
	}
	if !s.loopbackOnly {
		return nil, errors.New("unauthenticated")
	}
	name := strings.TrimSpace(named)
	if name == "" {
		name = "local"
	}
	if u, err := s.store.UserByName(ctx, name); err == nil {
		return u, nil
	}
	u := &store.User{Name: name, DisplayName: name, Role: "admin"}
	if err := s.store.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// subjectOf resolves a credential presented on a route the middleware lets
// through. It returns nil when there is none, which only the loopback case
// goes on to accept.
func (s *Server) subjectOf(r *http.Request) *Subject {
	if sub, ok := SubjectFrom(r.Context()); ok {
		return sub
	}
	tok := bearer(r)
	if tok == "" {
		return nil
	}
	sub, err := s.resolveSubject(r.Context(), tok)
	if err != nil {
		return nil
	}
	return sub
}

// devicePage is §3.3's verification page, served by the API rather than the
// SPA: it is a form with no state and no build step, and it must work on a
// machine where the UI bundle was never built.
func (s *Server) devicePage(w http.ResponseWriter, r *http.Request) {
	code := html.EscapeString(r.URL.Query().Get("user_code"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, devicePageHTML, code)
}

const devicePageHTML = `<!doctype html>
<meta charset="utf-8"><title>wfx — approve a sign-in</title>
<style>
 body{font:15px/1.5 ui-sans-serif,system-ui,sans-serif;max-width:32rem;margin:6rem auto;padding:0 1rem}
 input,button{font:inherit;padding:.5rem .7rem}
 input[name=user_code]{letter-spacing:.2em;text-transform:uppercase}
 .row{margin:.8rem 0}
 #out{margin-top:1rem;white-space:pre-wrap}
</style>
<h1>Approve a sign-in</h1>
<p>A <code>wfx-cli</code> client is asking to sign in. Check the code it printed
matches the one below, then approve.</p>
<form id="f">
  <div class="row"><input name="user_code" value="%s" placeholder="XXXX-XXXX" size="12" required></div>
  <div class="row"><input name="user" placeholder="user (local server only)" size="24"></div>
  <div class="row"><input name="token" type="password" placeholder="your wfx token (remote server)" size="34"></div>
  <div class="row"><button value="approve">Approve</button>
  <button value="deny" formnovalidate id="deny">Deny</button></div>
</form>
<div id="out"></div>
<script>
const f=document.getElementById('f'),out=document.getElementById('out');
let action='approve';
f.addEventListener('click',e=>{if(e.target.tagName==='BUTTON')action=e.target.value;});
f.addEventListener('submit',async e=>{
  e.preventDefault();
  const d=new FormData(f), h={'Content-Type':'application/json'};
  if(d.get('token'))h['Authorization']='Bearer '+d.get('token');
  const res=await fetch('/api/device/verify',{method:'POST',headers:h,
    body:JSON.stringify({user_code:d.get('user_code'),user:d.get('user'),action})});
  const j=await res.json().catch(()=>({}));
  out.textContent=res.ok?(j.status==='approved'?'Approved as '+j.user+'. Return to your terminal.':'Denied.')
                        :('Refused: '+(j.error||res.status));
});
</script>
`

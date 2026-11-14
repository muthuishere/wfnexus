package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/login"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// Login providers (ADR 0028): password (local), oidc, google, github, ldap,
// mock-ldap. They authenticate only; every one ends in issueSession.

// One sign-in's single-use values — state, PKCE verifier, nonce, the
// provider and an expiry — travel in ONE cookie, signed with a key only this
// server holds, so a cookie set by anything else (another subdomain, a
// tossed cookie) is refused. Over https it is a __Host- cookie (Path=/,
// Secure, no Domain); over plain http, which only a local install uses, it is
// scoped to /api/auth/oauth/.
const (
	oauthCookieBase   = "wfx_oauth"
	oauthCookiePath   = "/api/auth/oauth/"
	oauthCookieMaxAge = 600
	oauthKeyLabel     = "wfx-oauth-cookie-v1"
)

// errLinkPending: the account was linked to this sign-in on its first
// sign-in after the upgrade, and held roles, so an admin confirms it first.
var errLinkPending = errors.New("your account link is pending confirmation by an administrator")

// buildProviders reads the server config. A provider that cannot be built —
// mock-ldap off loopback without force, a missing clientId — is NOT enabled,
// and says why in the log. A redirect provider (oidc, google, github) also
// needs the server's public URL: its callback address is built from that,
// never from the request.
func buildProviders(s *Server) {
	ps, errs := login.Build(s.eng.AuthConfig().Providers, s.loopbackOnly)
	for _, err := range errs {
		log.Printf("auth: provider disabled: %v", err)
	}
	if s.configuredPublicURL() == "" {
		for name, p := range ps {
			if p.Redirect != nil {
				log.Printf("auth: provider disabled: %s: set WFX_PUBLIC_URL (publicUrl) — the sign-in callback address is built from it", name)
				delete(ps, name)
			}
		}
	}
	s.providers = ps
}

func (s *Server) configuredPublicURL() string { return strings.TrimRight(s.eng.PublicURL(), "/") }

// listLoginProviders is GET /api/auth/providers, for the sign-in page.
func (s *Server) listLoginProviders(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]string{{"name": "password", "type": "password", "kind": "password"}}
	names := make([]string, 0, len(s.providers))
	for n := range s.providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := s.providers[n]
		out = append(out, map[string]string{"name": n, "type": p.Config.Type, "kind": p.Kind()})
	}
	writeJSON(w, 200, out)
}

// externalUser finds or creates the local user an external identity maps to,
// and brings its provider-mapped roles up to date.
//
// The match is on (provider, subject) only. A name or email never finds or
// links a user, with one exception made once per row: an account created by
// an external sign-in before subjects were kept (link state "legacy") is bound
// to the subject that next signs in with its name. That is written to the
// log for review, and if the account held roles it signs in only after an
// admin confirms the link (`wfx-server link confirm NAME`). Any other subject
// presenting that name later gets an account of its own.
//
// The local name is "<username>@<provider>", so no provider can ever sign in
// as a local account.
func (s *Server) externalUser(ctx context.Context, p *login.Provider, id *login.Identity) (*store.User, error) {
	if id.Subject == "" {
		return nil, errors.New("the provider gave no stable id for the person")
	}
	provider := p.Config.Name
	u, err := s.store.UserBySubject(ctx, provider, id.Subject)
	if errors.Is(err, store.ErrNotFound) && id.LegacySubject != "" && id.LegacySubject != id.Subject {
		// The same person bound under the subject an older version used (an
		// LDAP username): moved to the stable one, once.
		if ok, rerr := s.store.ReKeySubject(ctx, provider, id.LegacySubject, id.Subject); rerr == nil && ok {
			log.Printf("auth: AUDIT %s binding %q re-keyed to %s", provider, id.LegacySubject, id.Subject)
			u, err = s.store.UserBySubject(ctx, provider, id.Subject)
		}
	}
	switch {
	case err == nil:
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	default:
		if u, err = s.bindOrCreate(ctx, p, id); err != nil {
			return nil, err
		}
	}
	if u.Disabled {
		return nil, errBadCredentials
	}
	if st, err := s.store.LinkState(ctx, u.ID); err != nil {
		return nil, err
	} else if st == store.LinkConfirm {
		return nil, errLinkPending
	}
	s.syncRoles(ctx, p, u, id.Groups)
	return s.store.UserByName(ctx, u.Name)
}

// bindOrCreate handles a (provider, subject) seen for the first time.
//
// A legacy row named <user>@<provider> (one that existed before subjects were
// kept) is bound according to the provider that carries its name:
//   - an LDAP provider binds it directly: its subject is the LDAP username,
//     which is what it always matched on;
//   - a redirect provider (oidc, google, github) binds it once, to the
//     subject that signs in with that name, with an audit line; if it held
//     roles its tokens are revoked and it waits for an admin's confirmation.
func (s *Server) bindOrCreate(ctx context.Context, p *login.Provider, id *login.Identity) (*store.User, error) {
	provider := p.Config.Name
	name := id.Username + "@" + provider
	if legacy, err := s.store.UserByName(ctx, name); err == nil {
		if st, _ := s.store.LinkState(ctx, legacy.ID); st == store.LinkLegacy {
			if p.Redirect == nil {
				if ok, err := s.store.BindLegacy(ctx, legacy.ID, provider, id.Subject, id.Username, ""); err != nil {
					return nil, err
				} else if ok {
					return legacy, nil
				}
			} else {
				grants, err := s.store.HasGrants(ctx, legacy.ID)
				if err != nil {
					return nil, err
				}
				state := store.LinkLinked
				if grants {
					state = store.LinkConfirm
				}
				if ok, err := s.store.BindLegacy(ctx, legacy.ID, provider, id.Subject, id.Username, state); err != nil {
					return nil, err
				} else if ok {
					note := "review with `wfx-server link list`; `wfx-server link reject " + name + "` undoes it"
					if grants {
						// Nothing it held before keeps it signed in meanwhile.
						if err := s.store.RevokeAllUserTokens(ctx, legacy.ID); err != nil {
							return nil, err
						}
						note = "it held roles, so it signs in only after `wfx-server link confirm " + name + "`"
					}
					log.Printf("auth: AUDIT account %s linked to %s subject %s on first sign-in after upgrade; %s", name, provider, id.Subject, note)
					return legacy, nil
				}
			}
		}
		// The name is taken by an account that is not this person's: this
		// identity gets its own.
		sum := sha256.Sum256([]byte(provider + "\x00" + id.Subject))
		name += "~" + hex.EncodeToString(sum[:4])
	}
	u := &store.User{Name: name, DisplayName: id.Username}
	if err := s.store.CreateUser(ctx, u); err != nil {
		// The same person signing in twice at once: the other request made it.
		if linked, err2 := s.store.UserBySubject(ctx, provider, id.Subject); err2 == nil {
			return linked, nil
		}
		return nil, err
	}
	if ok, err := s.store.SetSubject(ctx, u.ID, provider, id.Subject); err != nil || !ok {
		_ = s.store.DeleteUser(ctx, u.Name)
		if linked, err2 := s.store.UserBySubject(ctx, provider, id.Subject); err2 == nil {
			return linked, nil
		}
		if err == nil {
			err = errors.New("the account could not be bound")
		}
		return nil, err
	}
	return u, nil
}

// syncRoles makes the user's provider-mapped roles match this sign-in: every
// role the provider config can grant (defaultRole and every groupRoles value)
// is granted when the person's groups call for it now and revoked when they
// no longer do. Roles the provider config does not mention are left alone.
// Each grant and revoke that changes something is written to the audit log.
func (s *Server) syncRoles(ctx context.Context, p *login.Provider, u *store.User, groups []string) {
	want := map[string]bool{}
	if p.Config.DefaultRole != "" {
		want[p.Config.DefaultRole] = true
	}
	for _, g := range groups {
		if r := p.Config.GroupRoles[g]; r != "" {
			want[r] = true
		}
	}
	managed := map[string]bool{}
	for r := range want {
		managed[r] = true
	}
	for _, r := range p.Config.GroupRoles {
		if r != "" {
			managed[r] = true
		}
	}
	held := s.heldSpecs(ctx, u)
	for spec := range managed {
		if want[spec] || !held[specKey(spec)] {
			continue
		}
		if changed, err := s.revokeSpec(ctx, u, spec); err != nil {
			log.Printf("auth: %s: revoke %s from %s: %v", p.Config.Name, spec, u.Name, err)
		} else if changed {
			log.Printf("auth: AUDIT role sync (%s): revoked %s from %s", p.Config.Name, spec, u.Name)
		}
	}
	for spec := range want {
		if held[specKey(spec)] {
			continue
		}
		if err := s.grantSpec(ctx, u, spec); err != nil {
			log.Printf("auth: %s: grant %s to %s: %v", p.Config.Name, spec, u.Name, err)
		} else {
			log.Printf("auth: AUDIT role sync (%s): granted %s to %s", p.Config.Name, spec, u.Name)
		}
	}
}

// heldSpecs is what the user holds, in specKey form.
func (s *Server) heldSpecs(ctx context.Context, u *store.User) map[string]bool {
	held := map[string]bool{}
	if u.Role == RoleSuperadmin {
		held[RoleSuperadmin] = true
	}
	if ms, err := s.store.GrantsOf(ctx, u.ID); err == nil {
		for _, m := range ms {
			held[m.Role+":"+m.Name] = true
		}
	}
	return held
}

// specKey is a role spec as heldSpecs writes it: "superadmin", or
// "<role>:<name>".
func specKey(spec string) string {
	role, name, _ := strings.Cut(strings.TrimSpace(spec), ":")
	role = normalizeRole(role)
	if role == RoleSuperadmin {
		return role
	}
	return role + ":" + name
}

func (s *Server) oauthProvider(w http.ResponseWriter, r *http.Request) *login.Provider {
	p := s.providers[urlName(r, "provider")]
	if p == nil || p.Redirect == nil {
		notFound(w)
		return nil
	}
	return p
}

// oauthRedirectURI is built from the configured public URL only — never from
// the request's Host or X-Forwarded-* headers.
func (s *Server) oauthRedirectURI(name string) string {
	return s.configuredPublicURL() + "/api/auth/oauth/" + name + "/callback"
}

// oauthCookie is the sign-in cookie's name, path and Secure flag, decided by
// the public URL's scheme.
func (s *Server) oauthCookie() (name, path string, secure bool) {
	if strings.HasPrefix(s.configuredPublicURL(), "https://") {
		return "__Host-" + oauthCookieBase, "/", true
	}
	return oauthCookieBase, oauthCookiePath, false
}

type oauthTicket struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	Verifier string `json:"v"`
	Nonce    string `json:"n"`
	Expires  int64  `json:"e"`
}

func (s *Server) oauthMAC(payload string) []byte {
	k := hmac.New(sha256.New, s.jwtKey)
	k.Write([]byte(oauthKeyLabel))
	m := hmac.New(sha256.New, k.Sum(nil))
	m.Write([]byte(payload))
	return m.Sum(nil)
}

func (s *Server) sealTicket(t oauthTicket) string {
	raw, _ := json.Marshal(t)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + base64.RawURLEncoding.EncodeToString(s.oauthMAC(payload))
}

// openTicket checks the cookie's signature, expiry and provider.
func (s *Server) openTicket(v, provider string) (oauthTicket, bool) {
	var t oauthTicket
	payload, sig, ok := strings.Cut(v, ".")
	if !ok {
		return t, false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.oauthMAC(payload)) {
		return t, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(raw, &t) != nil {
		return t, false
	}
	if time.Now().Unix() > t.Expires || t.Provider != provider || t.State == "" || t.Verifier == "" {
		return t, false
	}
	return t, true
}

// oauthStart is GET /api/auth/oauth/{provider}/start: the sign-in's values in
// a signed cookie, then off to the provider.
func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	p := s.oauthProvider(w, r)
	if p == nil {
		return
	}
	a := login.NewAuthRequest()
	u, err := p.Redirect.AuthCodeURL(r.Context(), a, s.oauthRedirectURI(p.Config.Name))
	if err != nil {
		log.Printf("auth: %s: start: %v", p.Config.Name, err)
		writeErr(w, http.StatusBadGateway, errors.New("sign-in is unavailable; try again later"))
		return
	}
	name, path, secure := s.oauthCookie()
	http.SetCookie(w, &http.Cookie{
		Name: name, Path: path, MaxAge: oauthCookieMaxAge, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
		Value: s.sealTicket(oauthTicket{Provider: p.Config.Name, State: a.State, Verifier: a.Verifier, Nonce: a.Nonce,
			Expires: time.Now().Add(oauthCookieMaxAge * time.Second).Unix()}),
	})
	http.Redirect(w, r, u, http.StatusFound)
}

// oauthCallback is where the provider sends the browser back: the signed
// cookie must be this server's and its state must match, the code is
// exchanged with the PKCE verifier (and, for an OpenID provider, its id_token
// checked against the nonce), and the browser is signed in. The cookie is
// cleared whatever the outcome, panics included: it is good for one attempt.
// What the provider said when it refused is logged, not shown.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	p := s.oauthProvider(w, r)
	if p == nil {
		return
	}
	name, path, secure := s.oauthCookie()
	clear := func() {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: path, MaxAge: -1,
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure})
	}
	// Cleared first, before anything can fail or panic: the Set-Cookie header
	// is then on every response this request produces, a panic's 500
	// included (a header added after the response has started would be lost).
	clear()
	var t oauthTicket
	ok := false
	if c, err := r.Cookie(name); err == nil {
		t, ok = s.openTicket(c.Value, p.Config.Name)
	}
	q := r.URL.Query()
	if !ok || q.Get("code") == "" || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(t.State)) != 1 {
		writeErr(w, http.StatusBadRequest, errors.New("sign-in state did not match; start again"))
		return
	}
	failed := func(err error) {
		log.Printf("auth: %s: sign-in refused: %v", p.Config.Name, err)
		writeErr(w, http.StatusUnauthorized, errors.New("sign-in failed"))
	}
	id, err := p.Redirect.Exchange(r.Context(), q.Get("code"), s.oauthRedirectURI(p.Config.Name),
		login.AuthRequest{State: t.State, Verifier: t.Verifier, Nonce: t.Nonce})
	if err != nil {
		failed(err)
		return
	}
	u, err := s.externalUser(r.Context(), p, id)
	if errors.Is(err, errLinkPending) {
		writeErr(w, http.StatusForbidden, errLinkPending)
		return
	}
	if err != nil {
		failed(err)
		return
	}
	if err := s.startSession(r.Context(), w, r, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"sort"

	"github.com/muthuishere/wfnexus/apps/api/internal/login"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// Login providers (ADR 0028): password (local), oidc, google, github, ldap,
// mock-ldap. They authenticate only; every one ends in issueSession.

// The single-use values of one sign-in, kept in the browser between start and
// callback: the state, the PKCE verifier and the OpenID nonce.
const (
	oauthStateCookie    = "wfx_oauth_state"
	oauthVerifierCookie = "wfx_oauth_verifier"
	oauthNonceCookie    = "wfx_oauth_nonce"
	oauthCookiePath     = "/api/auth/oauth/"
	oauthCookieMaxAge   = 600
)

var oauthCookies = []string{oauthStateCookie, oauthVerifierCookie, oauthNonceCookie}

// buildProviders reads the server config. A provider that cannot be built —
// mock-ldap off loopback without force, a missing clientId — is NOT enabled,
// and says why in the log.
func buildProviders(s *Server) {
	ps, errs := login.Build(s.eng.AuthConfig().Providers, s.loopbackOnly)
	for _, err := range errs {
		log.Printf("auth: provider disabled: %v", err)
	}
	s.providers = ps
}

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

// externalUser finds or creates the local user an external identity maps to.
// The local name is "<username>@<provider>", so no provider can ever sign in
// as a local account. A new user gets NO roles, unless the provider config
// sets defaultRole or maps one of the person's groups.
func (s *Server) externalUser(ctx context.Context, p *login.Provider, id *login.Identity) (*store.User, error) {
	name := id.Username + "@" + p.Config.Name
	if u, err := s.store.UserByName(ctx, name); err == nil {
		if u.Disabled {
			return nil, errBadCredentials
		}
		return u, nil
	}
	u := &store.User{Name: name, DisplayName: id.Username}
	if err := s.store.CreateUser(ctx, u); err != nil {
		return nil, err
	}
	specs := []string{p.Config.DefaultRole}
	for _, g := range id.Groups {
		specs = append(specs, p.Config.GroupRoles[g])
	}
	for _, spec := range specs {
		if err := s.grantSpec(ctx, u, spec); err != nil {
			log.Printf("auth: %s: %v", p.Config.Name, err)
		}
	}
	return s.store.UserByName(ctx, name)
}

func (s *Server) oauthProvider(w http.ResponseWriter, r *http.Request) *login.Provider {
	p := s.providers[urlName(r, "provider")]
	if p == nil || p.Redirect == nil {
		notFound(w)
		return nil
	}
	return p
}

func (s *Server) oauthRedirectURI(r *http.Request, name string) string {
	return s.externalBase(r) + "/api/auth/oauth/" + name + "/callback"
}

// oauthStart is GET /api/auth/oauth/{provider}/start: the sign-in's state,
// PKCE verifier and nonce in cookies, then off to the provider.
func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	p := s.oauthProvider(w, r)
	if p == nil {
		return
	}
	a := login.NewAuthRequest()
	u, err := p.Redirect.AuthCodeURL(r.Context(), a, s.oauthRedirectURI(r, p.Config.Name))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	for name, v := range map[string]string{oauthStateCookie: a.State, oauthVerifierCookie: a.Verifier, oauthNonceCookie: a.Nonce} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: v, Path: oauthCookiePath, MaxAge: oauthCookieMaxAge,
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureRequest(r),
		})
	}
	http.Redirect(w, r, u, http.StatusFound)
}

// oauthCallback is where the provider sends the browser back: the state must
// match the cookie, the code is exchanged with the PKCE verifier (and, for an
// OpenID provider, its id_token checked against the nonce), and the browser is
// signed in. The sign-in's cookies are cleared whatever the outcome: each is
// good for one attempt.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	p := s.oauthProvider(w, r)
	if p == nil {
		return
	}
	cookie := func(name string) string {
		if c, err := r.Cookie(name); err == nil {
			return c.Value
		}
		return ""
	}
	a := login.AuthRequest{State: cookie(oauthStateCookie), Verifier: cookie(oauthVerifierCookie), Nonce: cookie(oauthNonceCookie)}
	for _, name := range oauthCookies {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: oauthCookiePath, MaxAge: -1,
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureRequest(r)})
	}
	q := r.URL.Query()
	if a.State == "" || a.Verifier == "" || q.Get("code") == "" ||
		subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(a.State)) != 1 {
		writeErr(w, http.StatusBadRequest, errors.New("sign-in state did not match; start again"))
		return
	}
	id, err := p.Redirect.Exchange(r.Context(), q.Get("code"), s.oauthRedirectURI(r, p.Config.Name), a)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	u, err := s.externalUser(r.Context(), p, id)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, err)
		return
	}
	if err := s.startSession(r.Context(), w, r, u.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

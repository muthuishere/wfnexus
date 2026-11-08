package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"sort"

	"github.com/muthuishere/wfnexus/apps/api/internal/login"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// Login providers (ADR 0028): password (local), oidc, google, github, ldap,
// mock-ldap. They authenticate only; every one ends in issueSession.

const oauthStateCookie = "wfx_oauth_state"

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

// oauthStart is GET /api/auth/oauth/{provider}/start: a state cookie, then
// off to the provider.
func (s *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	p := s.oauthProvider(w, r)
	if p == nil {
		return
	}
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	state := hex.EncodeToString(raw)
	u, err := p.Redirect.AuthCodeURL(r.Context(), state, s.oauthRedirectURI(r, p.Config.Name))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oauthStateCookie, Value: state, Path: "/api/auth/oauth/", MaxAge: 600,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secureRequest(r),
	})
	http.Redirect(w, r, u, http.StatusFound)
}

// oauthCallback is where the provider sends the browser back: the state must
// match the cookie, the code is exchanged, and the browser is signed in.
func (s *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	p := s.oauthProvider(w, r)
	if p == nil {
		return
	}
	c, err := r.Cookie(oauthStateCookie)
	q := r.URL.Query()
	if err != nil || c.Value == "" || q.Get("state") != c.Value || q.Get("code") == "" {
		writeErr(w, http.StatusBadRequest, errors.New("sign-in state did not match; start again"))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Value: "", Path: "/api/auth/oauth/", MaxAge: -1})
	id, err := p.Redirect.Exchange(r.Context(), q.Get("code"), s.oauthRedirectURI(r, p.Config.Name))
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

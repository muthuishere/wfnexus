package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/issuer"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// Accounts of an EXTERNAL token issuer.
//
// A person signed in elsewhere presents that issuer's ES256 access token. It
// is verified against the issuer's JWKS (internal/issuer), and its tid is
// mapped to a local tenant through a link an operator made. No link, no
// subject. Nothing in the token grants a permission: any role claim is
// ignored, and the subject reaches only the read routes under /api/svc/.

// issuerForTest replaces the configured verifier. Nothing outside _test.go
// files assigns it.
var issuerForTest *issuer.Verifier

// CheckTokenIssuer refuses a partial or unusable configuration at boot.
func CheckTokenIssuer(c config.TokenIssuer) error {
	if c.Partial() {
		return errors.New("token issuer: set all of WFX_TOKEN_ISSUER, WFX_TOKEN_AUDIENCE and WFX_TOKEN_JWKS_URL, or none")
	}
	if c.Enabled() {
		if err := issuer.CheckJWKSURL(c.JWKSURL); err != nil {
			return fmt.Errorf("token issuer: %w", err)
		}
	}
	return nil
}

func newIssuerVerifier(c config.TokenIssuer) *issuer.Verifier {
	if CheckTokenIssuer(c) != nil || !c.Enabled() {
		return nil
	}
	j := &issuer.JWKS{URL: c.JWKSURL}
	return &issuer.Verifier{Issuer: c.Issuer, Audience: c.Audience, Keys: j.KeysFor}
}

// subjectFromIssuer verifies an external token and resolves its tenant.
func (s *Server) subjectFromIssuer(ctx context.Context, tok string) (*Subject, error) {
	v := *s.issuer
	v.RevokedBefore = func(tid, sub, _ string) (time.Time, bool) {
		t, ok, err := s.store.IssuerRevokedBefore(ctx, v.Issuer, tid, sub)
		if err != nil {
			// Fail closed: a revocation that cannot be read revokes.
			return time.Now().Add(time.Hour), true
		}
		return t, ok
	}
	c, err := v.Verify(tok)
	if err != nil {
		// The class and detail only; the token is never part of err.
		log.Printf("issuer token refused: %v", err)
		return nil, errors.New("unauthenticated")
	}
	tenant, err := s.store.IssuerTenant(ctx, v.Issuer, c.Tenant)
	if err != nil {
		return nil, errors.New("unauthenticated")
	}
	return &Subject{Kind: SubjectAccount, Name: c.Subject, Tenant: tenant}, nil
}

func (s *Server) issuerRoutes(r interface {
	Put(string, http.HandlerFunc)
	Post(string, http.HandlerFunc)
}) {
	r.Put("/tenants/{tenant}/issuer-links", s.linkIssuerTenant)
	r.Post("/tenants/{tenant}/issuer-revocations", s.revokeIssuerSubject)
}

func (s *Server) configuredIssuer(w http.ResponseWriter) (string, bool) {
	if s.issuer == nil {
		writeErr(w, http.StatusConflict, errors.New("no token issuer is configured"))
		return "", false
	}
	return s.issuer.Issuer, true
}

// linkIssuerTenant maps the configured issuer's tenant id to this tenant.
func (s *Server) linkIssuerTenant(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	iss, ok := s.configuredIssuer(w)
	if !ok {
		return
	}
	var b struct {
		ExternalTenant string `json:"externalTenant"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || !toolNameRe.MatchString(b.ExternalTenant) {
		writeErr(w, 400, errors.New("externalTenant is required (1-128 of [A-Za-z0-9._:/-])"))
		return
	}
	if err := s.store.LinkIssuerTenant(r.Context(), t, iss, b.ExternalTenant); err != nil {
		if errors.Is(err, store.ErrLinkExists) {
			writeErr(w, 409, err)
			return
		}
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tenant": t, "externalTenant": b.ExternalTenant})
}

// revokeIssuerSubject refuses every token of one account of this tenant whose
// sign-in is before now.
func (s *Server) revokeIssuerSubject(w http.ResponseWriter, r *http.Request) {
	t, ok := s.adminTenant(w, r)
	if !ok {
		return
	}
	iss, ok := s.configuredIssuer(w)
	if !ok {
		return
	}
	var b struct {
		ExternalTenant string `json:"externalTenant"`
		Subject        string `json:"subject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.ExternalTenant == "" || b.Subject == "" {
		writeErr(w, 400, errors.New("externalTenant and subject are required"))
		return
	}
	// Only an external tenant linked to THIS tenant.
	if cur, err := s.store.IssuerTenant(r.Context(), iss, b.ExternalTenant); err != nil || cur != t {
		notFound(w)
		return
	}
	if err := s.store.RevokeIssuerSubject(r.Context(), iss, b.ExternalTenant, b.Subject, time.Now()); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"revoked": true})
}

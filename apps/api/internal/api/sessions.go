package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// One login, one JWT (ADR 0028).
//
// Every sign-in — password, device grant, browser grant — ends in issueSession:
// a refresh row in user_tokens (the value is shown once, stored as a hash) and
// a short-lived HS256 access token whose sid names that row. The middleware
// verifies the signature, then checks the row is still there, so revoking the
// row (logout, an admin) ends the session on the next request.

const (
	// refreshLabelPrefix marks a user_tokens row as a refresh token. Such a
	// value is traded at /api/auth/refresh and is never accepted as a bearer.
	refreshLabelPrefix = "refresh:"
	refreshCookieName  = "wfx_refresh"
	jwtKeyLabel        = "wfx-jwt-v1"
)

// jwtNow is the clock access tokens are signed and checked against; a test
// moves it to prove expiry.
var jwtNow = time.Now

// fixedJWTKey, when set (tests only), replaces the derived signing key so a
// test can mint access tokens directly.
var fixedJWTKey []byte

func isRefreshLabel(label string) bool { return strings.HasPrefix(label, refreshLabelPrefix) }

// jwtKeyFor derives the signing key from the env-store key. With no env store
// (no WFX_SECRET_KEY and no key file) the key is random per process: sessions
// then end on restart, which is the honest outcome for a server with nowhere
// to keep a key.
func jwtKeyFor(sealer any) []byte {
	if d, ok := sealer.(interface{ Derive(string) []byte }); ok && d != nil {
		return d.Derive(jwtKeyLabel)
	}
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return k
}

// session is what a sign-in hands a client.
type session struct {
	Access  string
	Refresh string
}

// issueSession mints a refresh row and its first access token.
func (s *Server) issueSession(ctx context.Context, userID uuid.UUID, label string) (session, error) {
	refresh := auth.NewToken()
	t, err := s.store.IssueUserToken(ctx, userID, auth.HashToken(refresh), refreshLabelPrefix+label, "")
	if err != nil {
		return session{}, err
	}
	u, _, err := s.store.UserBySession(ctx, t.ID)
	if err != nil {
		return session{}, err
	}
	return session{Access: s.mintAccess(u, t), Refresh: refresh}, nil
}

func (s *Server) mintAccess(u *store.User, t *store.UserToken) string {
	return auth.SignJWT(s.jwtKey, auth.Claims{Sub: u.ID.String(), Name: u.Name, Sid: t.ID.String()}, jwtNow(), auth.AccessTTL)
}

// tokenResponse is the one JSON shape every login answers with.
func tokenResponse(sess session) map[string]any {
	return map[string]any{
		"access_token":  sess.Access,
		"refresh_token": sess.Refresh,
		"token_type":    "Bearer",
		"expires_in":    int(auth.AccessTTL / time.Second),
	}
}

// subjectFromJWT verifies an access token and resolves its session row.
func (s *Server) subjectFromJWT(ctx context.Context, tok string) (*Subject, error) {
	c, err := auth.VerifyJWT(s.jwtKey, tok, jwtNow())
	if err != nil {
		return nil, err
	}
	sid, err := uuid.Parse(c.Sid)
	if err != nil {
		return nil, auth.ErrJWTMalformed
	}
	u, t, err := s.store.UserBySession(ctx, sid)
	if err != nil || u.ID.String() != c.Sub {
		return nil, errors.New("unauthenticated")
	}
	project := u.Project
	if t.Project != "" {
		project = t.Project
	}
	return &Subject{Kind: SubjectUser, Name: u.Name, Role: u.Role, Project: project, User: u, SessionID: t.ID}, nil
}

// refreshAccess trades a refresh value for a new access token.
func (s *Server) refreshAccess(ctx context.Context, refresh string) (string, *Subject, error) {
	if !strings.HasPrefix(refresh, "wfx_") {
		return "", nil, errors.New("unauthenticated")
	}
	u, t, err := s.store.UserByTokenHash(ctx, auth.HashToken(refresh))
	if err != nil || !isRefreshLabel(t.Label) {
		return "", nil, errors.New("unauthenticated")
	}
	project := u.Project
	if t.Project != "" {
		project = t.Project
	}
	return s.mintAccess(u, t), &Subject{Kind: SubjectUser, Name: u.Name, Role: u.Role, Project: project, User: u, SessionID: t.ID}, nil
}

// refresh is POST /api/auth/refresh {refresh_token}. A browser sends nothing
// in the body and is answered from its wfx_refresh cookie with a new
// wfx_session cookie.
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var b struct {
		RefreshToken string `json:"refresh_token"`
	}
	if r.ContentLength != 0 {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&b)
	}
	viaCookie := false
	if b.RefreshToken == "" {
		if c, err := r.Cookie(refreshCookieName); err == nil && c.Value != "" {
			if !sameOrigin(r) {
				writeErr(w, http.StatusForbidden, errors.New("cross-origin request refused"))
				return
			}
			b.RefreshToken, viaCookie = c.Value, true
		}
	}
	access, _, err := s.refreshAccess(r.Context(), b.RefreshToken)
	if err != nil {
		if viaCookie {
			s.clearSessionCookie(w, r)
		}
		writeErr(w, http.StatusUnauthorized, errors.New("unauthenticated: sign in again"))
		return
	}
	if viaCookie {
		s.setSessionCookie(w, r, access)
		writeJSON(w, http.StatusOK, map[string]any{"status": "refreshed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(auth.AccessTTL / time.Second),
	})
}

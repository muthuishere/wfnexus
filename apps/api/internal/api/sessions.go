package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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
			clearSessionCookie(w, r)
		}
		writeErr(w, http.StatusUnauthorized, errors.New("unauthenticated: sign in again"))
		return
	}
	if viaCookie {
		setSessionCookie(w, r, access)
		writeJSON(w, http.StatusOK, map[string]any{"status": "refreshed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(auth.AccessTTL / time.Second),
	})
}

// ---- per-workflow roles ----

const (
	memberOwner  = "owner"
	memberEditor = "editor"
)

// workflowAction names what a workflow-addressed request does, for the
// member check: "edit", "run", "own", or "" when it is not a member action.
func workflowAction(method, escapedPath string) (name, action string) {
	rest, ok := strings.CutPrefix(escapedPath, "/api/workflows/")
	if !ok || method == http.MethodGet || method == http.MethodHead {
		return "", ""
	}
	parts := strings.SplitN(rest, "/", 2)
	name, err := url.PathUnescape(parts[0])
	if err != nil || name == "" {
		return "", ""
	}
	sub := ""
	if len(parts) == 2 {
		sub = strings.SplitN(parts[1], "/", 2)[0]
	}
	switch sub {
	case "":
		if method == http.MethodPut || method == http.MethodDelete {
			return name, "edit"
		}
	case "memory":
		return name, "edit"
	case "runs", "dryrun", "dispatches":
		return name, "run"
	case "expose", "members":
		return name, "own"
	}
	return "", ""
}

func memberMay(role, action string) bool {
	switch role {
	case memberOwner:
		return true
	case memberEditor:
		return action == "edit" || action == "run"
	}
	return false
}

// memberRole is the caller's grant on a workflow, "" for none.
func (s *Server) memberRole(r *http.Request, workflow string) string {
	sub, ok := SubjectFrom(r.Context())
	if !ok || sub.User == nil {
		return ""
	}
	role, err := s.store.WorkflowMemberRole(r.Context(), workflow, sub.User.ID)
	if err != nil {
		return ""
	}
	return role
}

// memberAllows is the authorize middleware's second question: when the global
// role does not grant a workflow action, does a grant on THAT workflow?
func (s *Server) memberAllows(r *http.Request) bool {
	name, action := workflowAction(r.Method, r.URL.EscapedPath())
	if action == "" {
		return false
	}
	return memberMay(s.memberRole(r, name), action)
}

// mayManageMembers: a superadmin, or the workflow's owner.
func (s *Server) mayManageMembers(r *http.Request, workflow string) bool {
	return s.can(r, PermAdminWrite) || s.memberRole(r, workflow) == memberOwner
}

// runVisible: a caller without runs:all sees a run it started, or a run of a
// workflow it holds a grant on.
func (s *Server) runVisible(r *http.Request, run *store.Run) bool {
	if s.can(r, PermRunsAll) {
		return true
	}
	sub, ok := SubjectFrom(r.Context())
	if !ok {
		return false
	}
	return run.TriggeredBy == sub.Name || s.memberRole(r, run.Workflow) != ""
}

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.workflowInScope(w, r, name) {
		return
	}
	ms, err := s.store.ListWorkflowMembers(r.Context(), name)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, ms)
}

func (s *Server) setMember(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.workflowInScope(w, r, name) {
		return
	}
	if !s.mayManageMembers(r, name) {
		writeErr(w, http.StatusForbidden, errors.New("forbidden: only a superadmin or this workflow's owner chooses its members"))
		return
	}
	var b struct {
		Role string `json:"role"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&b); err != nil || (b.Role != memberOwner && b.Role != memberEditor) {
		writeErr(w, 400, errors.New(`expected {"role": "owner"|"editor"}`))
		return
	}
	u, err := s.store.UserByName(r.Context(), urlName(r, "user"))
	if err != nil {
		notFound(w)
		return
	}
	if err := s.store.SetWorkflowMember(r.Context(), name, u.ID, b.Role); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"workflow": name, "user": u.Name, "role": b.Role})
}

func (s *Server) removeMember(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if !s.workflowInScope(w, r, name) {
		return
	}
	if !s.mayManageMembers(r, name) {
		writeErr(w, http.StatusForbidden, errors.New("forbidden: only a superadmin or this workflow's owner chooses its members"))
		return
	}
	u, err := s.store.UserByName(r.Context(), urlName(r, "user"))
	if err != nil {
		notFound(w)
		return
	}
	if err := s.store.RemoveWorkflowMember(r.Context(), name, u.ID); err != nil {
		notFound(w)
		return
	}
	writeJSON(w, 200, map[string]any{"removed": true})
}

package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/auth"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// Who a request is from (ADR 0017).
//
// There is ONE middleware and it is installed unconditionally; the only thing
// that varies is what it does when no credential resolves. Bound to loopback
// the machine is the trust boundary, so the subject is NIL and the request is
// served — absent authentication, not permissive authentication: no anonymous
// user row is created, no default credential is generated, and there is no
// principal that could later be granted anything off-loopback. Bound anywhere
// else, a request with no valid subject is refused and the handler is never
// reached.
//
// There is deliberately no setting that changes this. A flag is exactly how a
// single-machine default becomes an exposed production install, so the
// reachability boundary and the auth boundary are the same boundary.

type subjectKind string

const (
	// SubjectUser is a person: a role and a project scope, issued by a device
	// grant. SubjectWorker is a machine: labels, issued by a join.
	SubjectUser   subjectKind = "user"
	SubjectWorker subjectKind = "worker"
)

// Subject is the authenticated caller. A nil *Subject means "no identity",
// which is only reachable on a loopback bind.
type Subject struct {
	Kind    subjectKind
	Name    string
	Role    string
	Project string
	User    *store.User
	Worker  *store.Worker
	// TokenHash is kept so a subject can revoke its OWN credential (logout)
	// without the value ever being held anywhere. The value is not here and
	// never is.
	TokenHash string
}

type subjectCtxKey struct{}

// SubjectFrom reads the authenticated caller off a request context. ok is false
// when there is none, which on a loopback server is the normal case.
func SubjectFrom(ctx context.Context) (*Subject, bool) {
	s, ok := ctx.Value(subjectCtxKey{}).(*Subject)
	return s, ok && s != nil
}

func withSubject(ctx context.Context, s *Subject) context.Context {
	return context.WithValue(ctx, subjectCtxKey{}, s)
}

// publicAPIPaths are the routes that cannot require a subject, because they
// are how a subject is obtained (RFC 8628 §3.1, §3.4) or how a monitor checks
// the process is alive. Everything else is guarded.
//
// This is an allowlist, not a bypass: it names four paths and a request for
// anything else resolves a subject or is refused.
var publicAPIPaths = map[string]bool{
	"/api/health":       true,
	"/api/device/code":  true,
	"/api/device/token": true,
	// /api/device/verify is listed here but is NOT unauthenticated: it does
	// its own subject resolution, so that a submission with a bad or missing
	// credential still reaches the §5.2 rate limit and the attempt counter
	// that burns a code being guessed at. A 401 from the middleware would
	// count nothing and teach an attacker the same thing for free.
	"/api/device/verify": true,
}

// requireSubject resolves the bearer to a user or a worker.
func (s *Server) requireSubject(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicAPIPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		tok := bearer(r)
		if tok != "" {
			sub, err := s.resolveSubject(r.Context(), tok)
			if err == nil {
				next.ServeHTTP(w, r.WithContext(withSubject(r.Context(), sub)))
				return
			}
			// A presented-but-unknown credential is a refusal even on
			// loopback: the caller asserted an identity and it is not one.
			writeErr(w, http.StatusUnauthorized, errors.New("unauthenticated"))
			return
		}
		if s.loopbackOnly {
			// Absent, not permissive: nil subject, nothing created.
			next.ServeHTTP(w, r)
			return
		}
		writeErr(w, http.StatusUnauthorized, errors.New("unauthenticated: run `wfx login --url <host>`"))
	})
}

// resolveSubject is the single lookup both subject kinds go through: hash the
// presented value, match a stored hash. The value is never compared in the
// clear and never leaves this function.
func (s *Server) resolveSubject(ctx context.Context, tok string) (*Subject, error) {
	if !strings.HasPrefix(tok, "wfx_") {
		return nil, errors.New("unauthenticated")
	}
	hash := auth.HashToken(tok)
	if s.store != nil {
		if u, t, err := s.store.UserByTokenHash(ctx, hash); err == nil {
			project := u.Project
			if t.Project != "" {
				project = t.Project
			}
			return &Subject{Kind: SubjectUser, Name: u.Name, Role: u.Role, Project: project, User: u, TokenHash: hash}, nil
		}
	}
	if wk, err := s.eng.AuthWorker(ctx, tok); err == nil {
		return &Subject{Kind: SubjectWorker, Name: wk.Name, Worker: wk, TokenHash: hash}, nil
	}
	return nil, errors.New("unauthenticated")
}

// revokeSelf is `wfx logout`'s server half: the caller's own token, and only
// its own, stops working. It is the same row a later admin revoke would delete.
func (s *Server) revokeSelf(w http.ResponseWriter, r *http.Request) {
	sub, ok := SubjectFrom(r.Context())
	if !ok || sub.Kind != SubjectUser {
		writeErr(w, http.StatusUnauthorized, errors.New("no user token to revoke"))
		return
	}
	if err := s.store.RevokeUserTokenByHash(r.Context(), sub.TokenHash); err != nil {
		writeErr(w, http.StatusNotFound, errors.New("token not found"))
		return
	}
	writeJSON(w, 200, map[string]any{"revoked": true})
}

// whoami tells a client which subject its credential resolves to. It returns
// metadata and never a value.
func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	sub, ok := SubjectFrom(r.Context())
	if !ok {
		writeJSON(w, 200, map[string]any{"authenticated": false, "loopback": s.loopbackOnly})
		return
	}
	writeJSON(w, 200, map[string]any{
		"authenticated": true,
		"kind":          string(sub.Kind),
		"name":          sub.Name,
		"role":          sub.Role,
		"project":       sub.Project,
	})
}

// LoopbackOnly is isLoopback for callers outside this package — main, which
// must make the same judgement about the bind address BEFORE it starts
// serving. One function, so the boot refusal and the middleware can never
// disagree about what loopback means.
func LoopbackOnly(addr string) bool { return isLoopback(addr) }

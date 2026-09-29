package api

import (
	"context"
	"errors"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// fakeAuthn stands in for a future identity provider. Nothing in it touches
// the users table, so passing these tests proves the handlers ask the
// Authenticator and not the store.
type fakeAuthn struct{ user *store.User }

func (f fakeAuthn) UserByToken(_ context.Context, tok string) (*store.User, *store.UserToken, error) {
	if tok != "wfx_from-the-idp" {
		return nil, nil, errors.New("unknown")
	}
	return f.user, &store.UserToken{Project: "p2"}, nil
}

func (f fakeAuthn) ApprovingUser(context.Context, string) (*store.User, error) { return f.user, nil }

func TestSubjectResolutionAndApprovalHonourTheAuthenticator(t *testing.T) {
	u := &store.User{Name: "ada", Role: "member", Project: "p1"}
	s := &Server{authn: fakeAuthn{user: u}, loopbackOnly: true}

	sub, err := s.resolveSubject(context.Background(), "wfx_from-the-idp")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Kind != SubjectUser || sub.User != u || sub.Project != "p2" {
		t.Fatalf("subject = %+v", sub)
	}

	got, err := s.approvingUser(context.Background(), nil, "")
	if err != nil || got != u {
		t.Fatalf("approving user = %v, %v", got, err)
	}
}

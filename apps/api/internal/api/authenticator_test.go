package api

import (
	"context"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// fakeAuthn stands in for a future identity provider. Nothing in it touches
// the users table, so passing these tests proves the handlers ask the
// Authenticator and not the store.
type fakeAuthn struct{ user *store.User }

func (f fakeAuthn) ApprovingUser(context.Context, string) (*store.User, error) { return f.user, nil }

func TestSubjectResolutionAndApprovalHonourTheAuthenticator(t *testing.T) {
	u := &store.User{Name: "ada", Role: "member", Project: "p1"}
	s := &Server{authn: fakeAuthn{user: u}, loopbackOnly: true}

	got, err := s.approvingUser(context.Background(), nil, "")
	if err != nil || got != u {
		t.Fatalf("approving user = %v, %v", got, err)
	}
}

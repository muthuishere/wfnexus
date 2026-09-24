package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/api"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

func bootStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wfnexus.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

// task 7.1 — first boot of a host that REQUIRES authentication seeds the roles
// as rows and exactly one admin; a second boot against the same store creates
// no further admin.
func TestFirstBootSeedsOneAdminAndTheRolesAsRows(t *testing.T) {
	ctx := context.Background()
	st := bootStore(t)

	// The first boot REFUSES to serve, having created the administrator.
	if err := bootstrapIdentity(ctx, ":8090", st); err == nil {
		t.Fatal("a non-loopback bind with no users served")
	}
	users, err := st.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Role != "admin" {
		t.Fatalf("first boot created %+v", users)
	}
	roles, err := st.ListRoles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != len(api.DefaultRoles()) {
		t.Fatalf("seeded %d role rows, want %d", len(roles), len(api.DefaultRoles()))
	}
	// Roles are ROWS carrying the permission vocabulary, not a set in the binary.
	for _, r := range roles {
		if len(r.Permissions) == 0 {
			t.Fatalf("role %q was seeded with no permissions", r.Name)
		}
	}

	// The second boot serves, and creates nothing further.
	if err := bootstrapIdentity(ctx, ":8090", st); err != nil {
		t.Fatalf("the second boot refused: %v", err)
	}
	again, err := st.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 {
		t.Fatalf("a second boot created a further admin: %+v", again)
	}
}

// The one-person scale: a loopback bind creates NO admin and NO roles.
func TestALoopbackBootCreatesNoAdminAndNoRoles(t *testing.T) {
	ctx := context.Background()
	st := bootStore(t)
	if err := bootstrapIdentity(ctx, "127.0.0.1:8090", st); err != nil {
		t.Fatalf("a loopback boot refused: %v", err)
	}
	if n, err := st.CountUsers(ctx); err != nil || n != 0 {
		t.Fatalf("a loopback boot created %d user(s) (err %v)", n, err)
	}
	if n, err := st.CountRoles(ctx); err != nil || n != 0 {
		t.Fatalf("a loopback boot created %d role(s) (err %v)", n, err)
	}
}

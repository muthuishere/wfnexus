package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

// ADR 0028: the members table on BOTH dialects.
func TestMembers(t *testing.T) {
	check := func(t *testing.T, st *Store) {
		ctx := context.Background()
		suffix := uuid.NewString()[:8]
		u := &User{Name: "m-" + suffix}
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.DeleteUser(ctx, u.Name) })
		wf, proj := "wf-"+suffix, "p-"+suffix

		if err := st.SetMember(ctx, MemberWorkflow, wf, u.ID, WorkflowRunner); err != nil {
			t.Fatal(err)
		}
		if err := st.SetMember(ctx, MemberWorkflow, wf, u.ID, WorkflowAdmin); err != nil { // upsert
			t.Fatal(err)
		}
		if err := st.SetMember(ctx, MemberProject, proj, u.ID, ProjectAdmin); err != nil {
			t.Fatal(err)
		}
		if ms, _ := st.ListMembers(ctx, MemberWorkflow, wf); len(ms) != 1 || ms[0].Role != WorkflowAdmin || ms[0].User != u.Name {
			t.Fatalf("workflow members: %+v", ms)
		}
		if gs, _ := st.GrantsOf(ctx, u.ID); len(gs) != 2 {
			t.Fatalf("grants: %+v", gs)
		}
		// A role that does not belong to the scope is refused by the schema.
		for _, bad := range [][2]string{{MemberProject, WorkflowRunner}, {MemberWorkflow, ProjectAdmin}, {MemberWorkflow, "editor"}} {
			if err := st.SetMember(ctx, bad[0], "x-"+suffix, u.ID, bad[1]); err == nil {
				t.Fatalf("stored %s on a %s", bad[1], bad[0])
			}
		}
		if err := st.RemoveMember(ctx, MemberWorkflow, wf, u.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.RemoveMember(ctx, MemberWorkflow, wf, u.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second remove: %v", err)
		}
		// Deleting the user deletes its grants.
		if err := st.DeleteUser(ctx, u.Name); err != nil {
			t.Fatal(err)
		}
		if gs, _ := st.GrantsOf(ctx, u.ID); len(gs) != 0 {
			t.Fatalf("grants outlived the user: %+v", gs)
		}
	}
	t.Run("sqlite", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "m.db")
		if err := Migrate(driverSQLite, path); err != nil {
			t.Fatal(err)
		}
		st, err := Open(context.Background(), driverSQLite, path)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		check(t, st)
	})
	t.Run("postgres", func(t *testing.T) {
		st := openPostgres(t)
		if st == nil {
			t.Skip("no postgres reachable — run `task infra:up`, or point TEST_DATABASE_URL at a server")
		}
		check(t, st)
	})
}

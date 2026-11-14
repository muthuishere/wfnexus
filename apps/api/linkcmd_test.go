package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// `wfx-server link list|confirm` works on the server's own database with no
// web sign-in, so the only administrator can confirm their own account link.
func TestServerConsoleConfirmsAPendingLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.db")
	if err := store.Migrate("sqlite", path); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), "sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	u := &store.User{Name: "carol@sso", Role: "superadmin"}
	if err := st.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE users SET link_state='legacy' WHERE name='carol@sso'`); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.BindLegacy(ctx, u.ID, "sso", "42", store.LinkConfirm); err != nil || !ok {
		t.Fatalf("bind: %v %v", ok, err)
	}
	// A second subject cannot re-bind it.
	if ok, _ := st.BindLegacy(ctx, u.ID, "sso", "99", ""); ok {
		t.Fatal("a bound account was bound again")
	}
	if ok, _ := st.SetSubject(ctx, uuid.New(), "sso", "42"); ok {
		t.Fatal("set a subject on a user that does not exist")
	}

	var out bytes.Buffer
	if err := linkCmd(ctx, st, []string{"list"}, &out); err != nil || !strings.Contains(out.String(), "carol@sso") || !strings.Contains(out.String(), "42") {
		t.Fatalf("list: %v %s", err, out.String())
	}
	out.Reset()
	if err := linkCmd(ctx, st, []string{"confirm", "carol@sso"}, &out); err != nil || !strings.Contains(out.String(), "confirmed") {
		t.Fatalf("confirm: %v %s", err, out.String())
	}
	if st, _ := st.LinkState(ctx, u.ID); st != "" {
		t.Fatalf("state after confirm = %q", st)
	}
	if err := linkCmd(ctx, st, []string{"confirm", "carol@sso"}, &out); err == nil {
		t.Fatal("confirmed twice")
	}
}

package api

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// The last-administrator rule counts every enabled superadmin, including one
// whose account waits on a link. When such accounts are all that remain, a
// warning says nobody can sign in as an administrator until a link is
// confirmed.
func TestRemovingAnAdminWarnsWhenNoRemainingAdminCanSignIn(t *testing.T) {
	op := newMockOP(t, "s3cret-value")
	h, st := oidcServerStore(t, op)
	ctx := context.Background()
	root := &store.User{Name: "root@sso", Role: RoleSuperadmin}
	if err := st.CreateUser(ctx, root); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE users SET link_state='confirm' WHERE name='root@sso'`); err != nil {
		t.Fatal(err)
	}
	oauthFlow(t, h, "sso", nil) // bob: superadmin through his group
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	op.userinfo = map[string]any{"preferred_username": "bob", "groups": []string{}}
	oauthFlow(t, h, "sso", nil)
	if u, _ := st.UserByName(ctx, "bob@sso"); u.Role == RoleSuperadmin {
		t.Fatal("bob kept superadmin although another admin exists")
	}
	if !strings.Contains(logs.String(), "WARNING no remaining superadmin can sign in") {
		t.Fatalf("no warning: %s", logs.String())
	}
}

// link list shows every account whose name collides with another's ignoring
// case as ambiguous, whatever its state.
func TestLinkListShowsEveryCaseCollision(t *testing.T) {
	_, st := oidcServerStore(t, newMockOP(t, "s3cret-value"))
	ctx := context.Background()
	for _, n := range []string{"Bo@corp", "bo@corp", "solo@corp"} {
		if err := st.CreateUser(ctx, &store.User{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	links, _ := st.PendingLinks(ctx)
	got := map[string]string{}
	for _, l := range links {
		got[l.Name] = l.State
	}
	if got["Bo@corp"] != store.LinkAmbiguous || got["bo@corp"] != store.LinkAmbiguous || got["solo@corp"] != "" || len(got) != 2 {
		t.Fatalf("link list = %+v", links)
	}
}

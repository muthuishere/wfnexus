package api

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Superadmins that all hold the role through an LDAP group leave the group
// and sign in at the same moment: one superadmin always remains.
func TestConcurrentSignInsNeverRemoveTheLastSuperadmin(t *testing.T) {
	const n = 5
	var users strings.Builder
	users.WriteString("users:\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&users, "  - username: adm%d\n    password: adm-long-password-%d\n    id: ID%d\n    groups: [%%s]\n", i, i, i)
	}
	inGroup := strings.ReplaceAll(users.String(), "%s", "admins")
	outGroup := strings.ReplaceAll(users.String(), "%s", "")
	mapped := func(st *store.Store, dir string) http.Handler { return ldapServerMapped(t, st, dir) }
	for round := 0; round < 10; round++ {
		_, st := testServerAuth(t, "127.0.0.1:8090", config.Auth{})
		h := mapped(st, inGroup)
		for i := 0; i < n; i++ {
			if w := providerSignIn(h, "corp", fmt.Sprintf("adm%d", i), fmt.Sprintf("adm-long-password-%d", i)); w.Code != 200 {
				t.Fatalf("round %d: sign-in %d: %d %s", round, i, w.Code, w.Body)
			}
		}
		h2 := mapped(st, outGroup)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				providerSignIn(h2, "corp", fmt.Sprintf("adm%d", i), fmt.Sprintf("adm-long-password-%d", i))
			}(i)
		}
		close(start)
		wg.Wait()
		admins := 0
		users, _ := st.ListUsers(context.Background())
		for _, u := range users {
			if u.Role == RoleSuperadmin {
				admins++
			}
		}
		if admins != 1 {
			t.Fatalf("round %d: %d superadmins left, want 1", round, admins)
		}
	}
}

// ldapServerMapped is ldapServer whose provider maps the group "admins" to
// superadmin.
func ldapServerMapped(t *testing.T, st *store.Store, directory string) http.Handler {
	t.Helper()
	p := filepath.Join(t.TempDir(), "directory.yaml")
	if err := os.WriteFile(p, []byte(directory), 0o600); err != nil {
		t.Fatal(err)
	}
	a := config.Auth{Providers: []config.AuthProvider{{Name: "corp", Type: "mock-ldap", File: p,
		GroupRoles: map[string]string{"admins": "superadmin"}}}}
	eng := engine.New(config.Config{Addr: "127.0.0.1:8090", Auth: a}, st, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
	return New(eng, st, nil, "127.0.0.1:8090", "", nil)
}

// Legacy LDAP accounts whose names differ only in case from the entry: one
// match is bound (audited); several are left alone, the sign-in refused, the
// audit line naming them all, and link list showing them as ambiguous.
func TestLegacyLDAPAccountsMatchIgnoringCaseUnlessAmbiguous(t *testing.T) {
	t.Run("one", func(t *testing.T) {
		_, st := testServerAuth(t, "127.0.0.1:8090", config.Auth{})
		markLegacy(t, st, "Ana@corp", RoleSuperadmin)
		h := ldapServer(t, st, "users:\n  - username: ana\n    password: ana-long-password\n    id: A1\n")
		var logs bytes.Buffer
		log.SetOutput(&logs)
		defer log.SetOutput(os.Stderr)
		if w := providerSignIn(h, "corp", "ana", "ana-long-password"); w.Code != 200 {
			t.Fatalf("sign-in: %d %s", w.Code, w.Body)
		}
		u, err := st.UserBySubject(context.Background(), "corp", "entryUUID:a1")
		if err != nil || u.Name != "Ana@corp" || u.Role != RoleSuperadmin {
			t.Fatalf("bound = %+v %v", u, err)
		}
		if !strings.Contains(logs.String(), "AUDIT account Ana@corp bound to directory id entryUUID:a1 on sign-in as ana") {
			t.Fatalf("no audit line: %s", logs.String())
		}
	})
	t.Run("several", func(t *testing.T) {
		_, st := testServerAuth(t, "127.0.0.1:8090", config.Auth{})
		markLegacy(t, st, "Ana@corp", RoleSuperadmin)
		markLegacy(t, st, "ana@corp", "")
		h := ldapServer(t, st, "users:\n  - username: ana\n    password: ana-long-password\n    id: A1\n")
		var logs bytes.Buffer
		log.SetOutput(&logs)
		defer log.SetOutput(os.Stderr)
		if w := providerSignIn(h, "corp", "ana", "ana-long-password"); w.Code == 200 {
			t.Fatalf("an ambiguous sign-in went through: %s", w.Body)
		}
		if !strings.Contains(logs.String(), "refused: legacy accounts Ana@corp, ana@corp differ only in case") {
			t.Fatalf("no audit line: %s", logs.String())
		}
		ctx := context.Background()
		for _, name := range []string{"Ana@corp", "ana@corp"} {
			u, _ := st.UserByName(ctx, name)
			if s, _ := st.LinkState(ctx, u.ID); s != store.LinkLegacy {
				t.Fatalf("%s changed to %q", name, s)
			}
		}
		if _, err := st.UserBySubject(ctx, "corp", "entryUUID:a1"); err == nil {
			t.Fatal("something was bound")
		}
		links, _ := st.PendingLinks(ctx)
		if len(links) != 2 || links[0].State != store.LinkAmbiguous || links[1].State != store.LinkAmbiguous {
			t.Fatalf("link list = %+v", links)
		}
	})
	t.Run("bindings differing in case", func(t *testing.T) {
		_, st := testServerAuth(t, "127.0.0.1:8090", config.Auth{})
		ctx := context.Background()
		for _, sub := range []string{"Ana", "ana"} {
			u := &store.User{Name: sub + "-x@corp"}
			if err := st.CreateUser(ctx, u); err != nil {
				t.Fatal(err)
			}
			if _, err := st.SetSubject(ctx, u.ID, "corp", sub); err != nil {
				t.Fatal(err)
			}
		}
		h := ldapServer(t, st, "users:\n  - username: ana\n    password: ana-long-password\n    id: A1\n")
		if w := providerSignIn(h, "corp", "ana", "ana-long-password"); w.Code == 200 {
			t.Fatalf("an ambiguous re-key went through: %s", w.Body)
		}
		if _, err := st.UserBySubject(ctx, "corp", "entryUUID:a1"); err == nil {
			t.Fatal("something was re-keyed")
		}
	})
}

// Without a public URL the server says so at startup, naming WFX_PUBLIC_URL.
func TestAMissingPublicURLIsWarnedAtStartup(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	testServerAuth(t, remoteAddr, config.Auth{})
	if !strings.Contains(logs.String(), "WARNING: WFX_PUBLIC_URL") {
		t.Fatalf("no warning: %s", logs.String())
	}
	logs.Reset()
	op := newMockOP(t, "s3cret-value")
	t.Setenv("TEST_SSO_SECRET", "s3cret-value")
	testServerPublic(t, ssoConfig(op), testPublicURL)
	if strings.Contains(logs.String(), "WARNING: WFX_PUBLIC_URL") {
		t.Fatalf("warned with a public URL set: %s", logs.String())
	}
}

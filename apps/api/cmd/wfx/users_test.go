package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// A fake /api/users + /api/roles that records what the CLI sent.
type fakeUsersAPI struct {
	mu    sync.Mutex
	calls []string
	body  map[string]map[string]any
	users []apiUser
}

func newFakeUsersAPI(t *testing.T) *fakeUsersAPI {
	t.Helper()
	tempContexts(t)
	f := &fakeUsersAPI{body: map[string]map[string]any{},
		users: []apiUser{{Name: "ana", Role: "viewer", Project: "p1"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		key := r.Method + " " + r.URL.Path
		f.calls = append(f.calls, key)
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.body[key] = b
		w.Header().Set("Content-Type", "application/json")
		switch key {
		case "GET /api/users":
			_ = json.NewEncoder(w).Encode(f.users)
		case "POST /api/users/ana/password", "POST /api/users/bo/password":
			if b["password"] == nil {
				_, _ = w.Write([]byte(`{"password":"GeneratedPass1234567"}`))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case "GET /api/roles":
			_, _ = w.Write([]byte(`[{"name":"viewer","permissions":["runs:read"]}]`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WFX_API", srv.URL)
	t.Setenv("WFX_API_TOKEN", "")
	return f
}

func TestUserAddCreatesThenSetsAOneTimePassword(t *testing.T) {
	f := newFakeUsersAPI(t)
	if err := userCmd([]string{"add", "bo", "--role", "publisher", "--project", "p2"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /api/users", "POST /api/users/bo/password"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("calls %v", f.calls)
	}
	if b := f.body["POST /api/users"]; b["role"] != "publisher" || b["project"] != "p2" {
		t.Fatalf("create body %v", b)
	}
	// ADR 0028: --role is optional; a new user starts with no roles.
	if err := userCmd([]string{"add"}); err == nil {
		t.Fatal("add without a name was accepted")
	}
}

func TestUserDisableAndRoleKeepTheOtherFields(t *testing.T) {
	f := newFakeUsersAPI(t)
	if err := userCmd([]string{"disable", "ana"}); err != nil {
		t.Fatal(err)
	}
	b := f.body["PUT /api/users/ana"]
	if b["disabled"] != true || b["role"] != "viewer" || b["project"] != "p1" {
		t.Fatalf("disable body %v", b)
	}
	if err := userCmd([]string{"role", "ana", "publisher"}); err != nil {
		t.Fatal(err)
	}
	b = f.body["PUT /api/users/ana"]
	if b["role"] != "publisher" || b["disabled"] != false || b["project"] != "p1" {
		t.Fatalf("role body %v", b)
	}
	if err := userCmd([]string{"role", "nobody", "viewer"}); err == nil {
		t.Fatal("an unknown user was accepted")
	}
}

func TestUserPasswdResetsWithoutTakingThePasswordAsAnArgument(t *testing.T) {
	f := newFakeUsersAPI(t)
	if err := userCmd([]string{"passwd", "ana"}); err != nil {
		t.Fatal(err)
	}
	if b := f.body["POST /api/users/ana/password"]; len(b) != 0 {
		t.Fatalf("a reset without --password-stdin sent %v; the server should generate it", b)
	}
}

func TestRoleSetSendsTheParsedPermissions(t *testing.T) {
	f := newFakeUsersAPI(t)
	if err := roleCmd([]string{"set", "ops", "runs:read,runs:write", "runs:read", "publish"}); err != nil {
		t.Fatal(err)
	}
	got := f.body["PUT /api/roles/ops"]["permissions"]
	want := []any{"runs:read", "runs:write", "publish"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("permissions %v", got)
	}
	if err := roleCmd([]string{"ls"}); err != nil {
		t.Fatal(err)
	}
}

func TestFormatUsersShowsPasswordStateNotThePassword(t *testing.T) {
	out := formatUsers([]apiUser{
		{Name: "a", Role: "admin", HasPassword: true, MustChange: true},
		{Name: "b", Role: "viewer", Disabled: true},
	})
	if !strings.Contains(out, "must change") || !strings.Contains(out, "disabled") || !strings.Contains(out, "none") {
		t.Fatalf("%s", out)
	}
}

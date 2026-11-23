package api

import (
	"strings"
	"testing"
)

// Cross-tenant paths the #189 spec does not reach, closed with the fix. Same
// fixture and helpers as tenancy_test.go.

// The shipped templates are readable by every tenant, so changeable by none:
// their state would otherwise be a channel between organizations.
func TestOrgCannotWriteTheSharedTemplatesSource(t *testing.T) {
	f := newOrgFixture(t, "")
	r := f.do(f.alice, "PUT", "/api/state", map[string]any{"scope": "project", "name": "templates", "key": "k", "value": "v"})
	if r.code/100 == 2 {
		t.Errorf("alice (org-admin) wrote state of the shared templates source: %d %.200s", r.code, r.body)
	}
}

// The global state namespace is the server's: an organization reaches it
// neither through the operator's door (no scope, or scope=global) nor
// through a run of its own.
func TestOrgCannotReachGlobalState(t *testing.T) {
	f := newOrgFixture(t, "")
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/state", nil},
		{"GET", "/api/state?scope=global", nil},
		{"GET", "/api/runs/" + f.runA + "/state?scope=global", nil},
		{"POST", "/api/runs/" + f.runA + "/state", map[string]any{"scope": "global", "key": "k", "value": "v"}},
	} {
		if r := f.do(f.alice, c.method, c.path, c.body); r.code != 404 {
			t.Errorf("alice %s %s: got %d, want 404: %.200s", c.method, c.path, r.code, r.body)
		}
	}
	// A person in no organization keeps it.
	if r := f.do(f.carol, "GET", "/api/state?scope=global", nil); r.code != 200 {
		t.Errorf("carol (no tenant) lost the global state: %d", r.code)
	}
}

// A copy saved under a bare name another tenant's workflow holds answers
// with the copy, never with the other tenant's definition.
func TestOrgCopyNeverAnswersWithAnotherTenantsDefinition(t *testing.T) {
	f := newOrgFixture(t, "")
	r := f.do(f.alice, "POST", "/api/templates/"+orgAcmeT+"/copy", map[string]any{"as": orgGlobexW, "project": orgAcmeP})
	if hits := f.leaks(r.body); len(hits) > 0 && strings.Contains(r.body, "globex-secret-workflow") {
		t.Errorf("the copy answered with globex's definition: %d %.300s", r.code, r.body)
	}
}

// Channel adapters are the operator's configuration.
func TestOrgCannotListChannelAdapters(t *testing.T) {
	f := newOrgFixture(t, "")
	if r := f.do(f.alice, "GET", "/api/channels", nil); r.code != 403 {
		t.Errorf("alice listed the channel adapters: %d %.200s", r.code, r.body)
	}
	if r := f.do(f.carol, "GET", "/api/channels", nil); r.code != 200 {
		t.Errorf("carol (no tenant) lost the channel list: %d", r.code)
	}
}

package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"net/http/httptest"

	"github.com/muthuishere/wfnexus/apps/api/internal/bundle"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
)

// A workflow that asks for a label this machine does not serve. The pull it
// produces is the case design §7 exists for: the bundle would install fine and
// then wait forever on turn one.
const labelledWorkflow = `name: code-review
description: review a diff
steps:
  - id: review
    name: Review
    prompt: "review it"
    skills: [fix-author]
    runs-on: windows
`

func (m *machine) publish(t *testing.T, yaml, version, token string) string {
	t.Helper()
	_, digest, tar := m.buildBundle(t, yaml, version)
	if w := do(m.h, "POST", "/api/bundles", token, publishBodyFor(digest, tar, version)); w.Code != 201 {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	return digest
}

// task 8.1/8.2 [SEC-TEST] — a pull onto a host that cannot run the bundle is
// refused naming what is absent and what would satisfy it, and INSTALLS
// NOTHING: no bundle skill root, no saved workflow. The refusal is the whole
// point; a half-installed bundle that fails later is the outcome it replaces.
func TestARefusedPullInstallsNothing(t *testing.T) {
	m := newMachine(t, ":8090")
	m.installSkill(t, "fix-author", "fix the author")
	tok := user(t, m.st, "ana", "publisher", "")
	digest := m.publish(t, labelledWorkflow, "1.0.0", tok)

	w := do(m.h, "POST", "/api/bundles/"+digest+"/install", tok, nil)
	if w.Code != 409 {
		t.Fatalf("install answered %d, want a refusal: %s", w.Code, w.Body.String())
	}
	var e struct{ Error string }
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cannot pull:", "label windows", "no worker online holds", "wfx-runner join"} {
		if !strings.Contains(e.Error, want) {
			t.Fatalf("the refusal does not say %q:\n%s", want, e.Error)
		}
	}
	root := filepath.Join(m.eng.BundleRootDir(), bundle.Hex(digest))
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("a refused pull materialised %s (err %v)", root, err)
	}
	if _, ok := m.eng.Definitions()["code-review"]; ok {
		t.Fatal("a refused pull saved the workflow anyway")
	}
}

// task 8.6 — absent, not empty. A workflow naming no provider, label or MCP
// server records no requirements, so the pull that today's code already allowed
// still just works: no check, no note, nothing new to configure.
func TestABundleWithNoRequirementsPullsUnchanged(t *testing.T) {
	m := newMachine(t, ":8090")
	m.installSkill(t, "fix-author", "fix the author")
	tok := user(t, m.st, "ana", "publisher", "")
	b, digest, tar := m.buildBundle(t, sampleWorkflow, "1.0.0")
	if b.Manifest.Requires != nil {
		t.Fatalf("a workflow with no provider, label or mcp recorded %+v", b.Manifest.Requires)
	}
	if w := do(m.h, "POST", "/api/bundles", tok, publishBodyFor(digest, tar, "1.0.0")); w.Code != 201 {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	w := do(m.h, "POST", "/api/bundles/"+digest+"/install", tok, nil)
	if w.Code != 200 {
		t.Fatalf("install answered %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Notes []string `json:"notes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Notes) != 0 {
		t.Fatalf("a bundle requiring nothing produced notes: %v", out.Notes)
	}
}

// An actor NOBODY TYPED is recorded as inferred, so the audit line cannot present
// a guess as a decision.
//
// The convenient default is the whole problem: `wfx` falls back to
// `$USER@hostname`, which is right for a person at their own terminal and wrong
// for an agent running the same command on their machine — the approval is then
// attributed to a human who never saw it. The platform cannot tell those apart, so
// it says so.
func TestAnInferredActorIsRecordedAsInferred(t *testing.T) {
	for _, tc := range []struct {
		name     string
		inferred bool
		wantNote bool
	}{
		{"a person typed --as", false, false},
		{"the client filled it in", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := engine.Actor{ID: "muthu@laptop", Via: "cli", Inferred: tc.inferred}
			got := a.String()
			if !strings.Contains(got, "muthu@laptop") || !strings.Contains(got, "via cli") {
				t.Fatalf("the audit line lost who or how: %q", got)
			}
			if has := strings.Contains(got, "inferred"); has != tc.wantNote {
				t.Errorf("inferred=%v produced %q", tc.inferred, got)
			}
		})
	}
}

// And an AUTHENTICATED subject is never inferred: somebody logged in as them, so
// the name was asserted by whoever held the credential. A client claiming
// otherwise must not be able to weaken that.
func TestAnAuthenticatedSubjectIsNeverInferred(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	r = r.WithContext(withSubject(r.Context(), &Subject{Name: "muthu"}))
	got := actorOf(r, stepBody{Actor: "somebody-else", ActorInferred: true})
	if got.ID != "muthu" {
		t.Errorf("a body actor overrode the authenticated subject: %+v", got)
	}
	if got.Inferred {
		t.Error("an authenticated subject was marked inferred; somebody authenticated as them")
	}
}

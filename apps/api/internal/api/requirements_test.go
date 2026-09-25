package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/bundle"
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

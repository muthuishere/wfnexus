package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// task 7.2 — the index is a VIEW of the registry, so a bundle that came from a
// git remote carries the remote and the resolved COMMIT through the API, and
// inspect hands both back. That is what lets the two views be reconciled.
func TestAnIndexedBundleCarriesItsGitOrigin(t *testing.T) {
	m := newMachine(t, ":8090")
	m.installSkill(t, "fix-author", "fix the author")
	tok := user(t, m.st, "ana", "publisher", "")
	_, digest, tar := m.buildBundle(t, sampleWorkflow, "1.0.0")

	remote, commit := "ssh://git@nas.lan/srv/wf.git", "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	body := publishBodyFor(digest, tar, "1.0.0")
	body["gitRemote"], body["gitCommit"] = remote, commit

	w := do(m.h, "POST", "/api/bundles", tok, body)
	if w.Code != 201 {
		t.Fatalf("publish answered %d: %s", w.Code, w.Body.String())
	}
	var row store.PublishedBundle
	if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if !row.FromGit() || *row.GitRemote != remote || *row.GitCommit != commit {
		t.Fatalf("the git origin did not survive the publish: %+v", row)
	}
	w = do(m.h, "GET", "/api/bundles/"+digest, tok, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"gitCommit":"a1b2c3d4`) {
		t.Fatalf("inspect lost the commit: %d %s", w.Code, w.Body.String())
	}
}

// A bundle uploaded to this host directly has NEITHER — absent, not an empty
// string pretending to be a value.
func TestADirectUploadHasNoGitOrigin(t *testing.T) {
	m := newMachine(t, ":8090")
	m.installSkill(t, "fix-author", "x")
	tok := user(t, m.st, "ana", "publisher", "")
	_, digest, tar := m.buildBundle(t, sampleWorkflow, "1.0.0")

	w := do(m.h, "POST", "/api/bundles", tok, publishBodyFor(digest, tar, "1.0.0"))
	if w.Code != 201 {
		t.Fatalf("publish answered %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "gitRemote") || strings.Contains(w.Body.String(), "gitCommit") {
		t.Fatalf("a direct upload reported a git origin: %s", w.Body.String())
	}
}

// Half a git origin is refused before anything is stored: a remote with no
// resolved commit names a place without naming what was read from it.
func TestAHalfGitOriginIsRefusedAndStoresNothing(t *testing.T) {
	m := newMachine(t, ":8090")
	m.installSkill(t, "fix-author", "x")
	tok := user(t, m.st, "ana", "publisher", "")
	_, digest, tar := m.buildBundle(t, sampleWorkflow, "1.0.0")

	body := publishBodyFor(digest, tar, "1.0.0")
	body["gitRemote"] = "ssh://git@nas.lan/srv/wf.git"
	w := do(m.h, "POST", "/api/bundles", tok, body)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "travel together") {
		t.Fatalf("a half git origin answered %d: %s", w.Code, w.Body.String())
	}
	assertNothingStored(t, m)
}

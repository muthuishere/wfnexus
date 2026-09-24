package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/blob"
	"github.com/muthuishere/wfnexus/apps/api/internal/bundle"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// machine is one host: its own skill root, its own workflows dir, its own blob
// store. Two of them is what the portability claim is asserted against.
type machine struct {
	h     http.Handler
	st    *store.Store
	eng   *engine.Engine
	dir   string
	blobs blob.Store
}

func newMachine(t *testing.T, addr string) *machine {
	t.Helper()
	dir := t.TempDir()
	// A machine's skill roots are ITS OWN. Without this the developer's real
	// ~/.claude/skills joins every root list and "a machine holding none of the
	// dependencies" is not what the test is standing up.
	t.Setenv("HOME", filepath.Join(dir, "home"))
	dbPath := filepath.Join(dir, "wfnexus.db")
	if err := store.Migrate("sqlite", dbPath); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st, err := store.Open(context.Background(), "sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(st.Close)
	if !isLoopback(addr) {
		for name, perms := range DefaultRoles() {
			if err := st.UpsertRole(context.Background(), name, perms); err != nil {
				t.Fatal(err)
			}
		}
	}
	skillsDir := filepath.Join(dir, "skills")
	wfDir := filepath.Join(dir, "workflows")
	for _, d := range []string{skillsDir, wfDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Config{Addr: addr, SkillsDir: skillsDir, WorkflowsDir: wfDir}
	bl, err := blob.OpenFolder(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	reg := skills.Load(skills.DefaultRoots(skillsDir)...)
	eng := engine.New(cfg, st, bl, map[string]*workflow.Definition{}, reg, nil)
	return &machine{h: New(eng, st, bl, addr, "", nil), st: st, eng: eng, dir: dir, blobs: bl}
}

func (m *machine) installSkill(t *testing.T, name, body string) {
	t.Helper()
	dir := filepath.Join(m.dir, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: "+name+"\ndescription: "+body+"\n---\n"+body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.eng.ReloadDefinitions(); err != nil {
		t.Fatal(err)
	}
}

const sampleWorkflow = `name: code-review
description: review a diff
steps:
  - id: review
    name: Review
    prompt: "review {{.Input.title}}"
    skills: [fix-author]
    tools: [read]
    output_schema:
      type: object
      properties:
        verdict: {type: string}
`

// buildBundle does what the CLI does: resolve against THIS machine's roots and
// carry the content.
func (m *machine) buildBundle(t *testing.T, yaml, version string) (*bundle.Bundle, string, []byte) {
	t.Helper()
	def, err := workflow.ParseYAML([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	b, err := bundle.Build("workflow", "", def.Name, version, []byte(yaml), def, m.eng.Skills(), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	digest, err := b.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := b.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return b, digest, raw
}

func publishBodyFor(digest string, tar []byte, version string) map[string]any {
	return map[string]any{"kind": "workflow", "project": "", "name": "code-review",
		"version": version, "digest": digest, "tarGz": tar}
}

// task 10.1 + [SEC-TEST] 10.6 — a publish carries every named skill with its
// digest, and records the AUTHENTICATED SUBJECT as its publisher.
func TestAPublishCarriesItsSkillsAndRecordsItsPublisher(t *testing.T) {
	m := newMachine(t, ":8090")
	m.installSkill(t, "fix-author", "fix the author")
	tok := user(t, m.st, "ana", "publisher", "")
	b, digest, tar := m.buildBundle(t, sampleWorkflow, "1.0.0")

	if len(b.Manifest.Skills) != 1 || b.Manifest.Skills[0].Name != "fix-author" {
		t.Fatalf("the bundle carries %+v", b.Manifest.Skills)
	}
	if b.Files["skills/fix-author/SKILL.md"] == nil {
		t.Fatal("the skill's content did not travel")
	}
	w := do(m.h, "POST", "/api/bundles", tok, publishBodyFor(digest, tar, "1.0.0"))
	if w.Code != 201 {
		t.Fatalf("publish answered %d: %s", w.Code, w.Body.String())
	}
	var row store.PublishedBundle
	if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row.PublishedByName != "ana" || row.PublishedBy == nil || row.PublishedAt.IsZero() {
		t.Fatalf("provenance not recorded: %+v", row)
	}
	if row.Digest != digest {
		t.Fatalf("stored digest %s, computed %s", row.Digest, digest)
	}
	// ...and the bytes are in the blob store under bundles/<sha256>/.
	if _, err := os.Stat(filepath.Join(m.dir, "blobs", "bundles", bundle.Hex(digest), "bundle.tar.gz")); err != nil {
		t.Fatalf("the bundle is not in the blob store: %v", err)
	}
	// Inspect returns both provenance fields.
	w = do(m.h, "GET", "/api/bundles/"+digest, tok, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"publishedBy_name":"ana"`) {
		t.Fatalf("inspect: %d %s", w.Code, w.Body.String())
	}
}

// [SEC-TEST] 10.6 — a publish with no valid subject stores NOTHING.
func TestPublishingWithNoSubjectIsRefused(t *testing.T) {
	m := newMachine(t, ":8090")
	m.installSkill(t, "fix-author", "x")
	_, digest, tar := m.buildBundle(t, sampleWorkflow, "1.0.0")
	if w := do(m.h, "POST", "/api/bundles", "", publishBodyFor(digest, tar, "1.0.0")); w.Code != 401 {
		t.Fatalf("an unauthenticated publish answered %d: %s", w.Code, w.Body.String())
	}
	assertNothingStored(t, m)
}

// [SEC-TEST] 10.6 / 7.5 — a subject scoped to A publishing into B is refused
// and stores nothing.
func TestPublishingIntoAnotherProjectIsRefused(t *testing.T) {
	m := newMachine(t, ":8090")
	m.installSkill(t, "fix-author", "x")
	tok := user(t, m.st, "ana", "publisher", "acme")
	_, digest, tar := m.buildBundle(t, sampleWorkflow, "1.0.0")
	body := publishBodyFor(digest, tar, "1.0.0")
	body["project"] = "other"
	if w := do(m.h, "POST", "/api/bundles", tok, body); w.Code != 403 {
		t.Fatalf("an out-of-scope publish answered %d: %s", w.Code, w.Body.String())
	}
	assertNothingStored(t, m)
}

// task 10.5 — republishing name@version is a REFUSAL, including for identical
// bytes; a new version is a new digest; a tag moves and a version does not.
func TestAVersionIsImmutableAndATagMoves(t *testing.T) {
	m := newMachine(t, ":8090")
	m.installSkill(t, "fix-author", "x")
	tok := user(t, m.st, "ana", "publisher", "")
	_, d1, tar1 := m.buildBundle(t, sampleWorkflow, "1.0.0")
	if w := do(m.h, "POST", "/api/bundles", tok, publishBodyFor(d1, tar1, "1.0.0")); w.Code != 201 {
		t.Fatalf("first publish: %d %s", w.Code, w.Body.String())
	}
	// Identical bytes, same version: still refused, and the message says the
	// digest matches so the author knows it is a no-op rather than a conflict.
	w := do(m.h, "POST", "/api/bundles", tok, publishBodyFor(d1, tar1, "1.0.0"))
	if w.Code != http.StatusConflict {
		t.Fatalf("republishing identical bytes answered %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no-op") || !strings.Contains(w.Body.String(), "1.0.0") {
		t.Fatalf("the refusal does not name the existing version: %s", w.Body.String())
	}
	// Different content, same version: also refused, and nothing changed.
	changed := strings.Replace(sampleWorkflow, "review a diff", "review a diff, harder", 1)
	_, d1b, tar1b := m.buildBundle(t, changed, "1.0.0")
	if d1b == d1 {
		t.Fatal("changed content produced the same digest")
	}
	if w := do(m.h, "POST", "/api/bundles", tok, publishBodyFor(d1b, tar1b, "1.0.0")); w.Code != http.StatusConflict {
		t.Fatalf("republishing different bytes answered %d", w.Code)
	}
	stored, err := m.st.Bundle(context.Background(), "", "workflow", "code-review", "1.0.0")
	if err != nil || stored.Digest != d1 {
		t.Fatalf("1.0.0 changed under a refused republish: %+v %v", stored, err)
	}
	// A new version is a new row and its own digest.
	_, d2, tar2 := m.buildBundle(t, changed, "1.1.0")
	if w := do(m.h, "POST", "/api/bundles", tok, publishBodyFor(d2, tar2, "1.1.0")); w.Code != 201 {
		t.Fatalf("1.1.0: %d %s", w.Code, w.Body.String())
	}
	// A tag moves; the versions do not.
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if w := do(m.h, "PUT", "/api/bundles/tags/latest", tok,
			map[string]any{"project": "", "kind": "workflow", "name": "code-review", "version": v}); w.Code != 200 {
			t.Fatalf("tagging %s: %d %s", v, w.Code, w.Body.String())
		}
	}
	got, err := m.st.BundleTag(context.Background(), "", "workflow", "code-review", "latest")
	if err != nil || got != "1.1.0" {
		t.Fatalf("the tag did not move: %q %v", got, err)
	}
	for v, want := range map[string]string{"1.0.0": d1, "1.1.0": d2} {
		row, err := m.st.Bundle(context.Background(), "", "workflow", "code-review", v)
		if err != nil || row.Digest != want {
			t.Fatalf("%s moved: %+v %v", v, row, err)
		}
	}
}

// task 10.3 — an unresolvable skill fails the publish, names the reference and
// the roots searched, and stores nothing.
func TestAnUnresolvableSkillFailsThePublish(t *testing.T) {
	m := newMachine(t, ":8090")
	def, err := workflow.ParseYAML([]byte(sampleWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	_, err = bundle.Build("workflow", "", def.Name, "1.0.0", []byte(sampleWorkflow), def, m.eng.Skills(), nil)
	if err == nil {
		t.Fatal("a workflow naming a skill this machine does not have was published")
	}
	if !strings.Contains(err.Error(), "fix-author") || !strings.Contains(err.Error(), "roots searched") {
		t.Fatalf("the error does not name the skill and the roots: %v", err)
	}
	assertNothingStored(t, m)
}

// [SEC-TEST] task 10.2 — the server-side half of the credential refusal: a
// client is not a guard, so a hand-built bundle carrying a pasted key is
// refused on receipt and nothing is stored.
func TestTheServerRefusesAPastedCredentialInAReceivedBundle(t *testing.T) {
	m := newMachine(t, ":8090")
	m.installSkill(t, "fix-author", "x")
	tok := user(t, m.st, "ana", "publisher", "")
	const pasted = "sk-live-AAAABBBBCCCCDDDD/EEEE+FFFF=="
	planted := strings.Replace(sampleWorkflow, "    tools: [read]\n",
		"    tools: [read]\n    env:\n      \""+pasted+"\": \"1\"\n", 1)

	// Built by hand, bypassing the client check entirely.
	def, err := workflow.ParseYAML([]byte(planted))
	if err != nil {
		t.Fatal(err)
	}
	b := bundle.New("workflow", "", def.Name, "1.0.0")
	b.AddWorkflow([]byte(planted))
	if err := b.AddSkillDir("fix-author", filepath.Join(m.dir, "skills", "fix-author")); err != nil {
		t.Fatal(err)
	}
	digest, err := b.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	tar, err := b.Pack()
	if err != nil {
		t.Fatal(err)
	}
	w := do(m.h, "POST", "/api/bundles", tok, publishBodyFor(digest, tar, "1.0.0"))
	if w.Code != 400 {
		t.Fatalf("the server accepted a pasted credential: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), pasted) {
		t.Fatalf("the refusal echoed the value: %s", w.Body.String())
	}
	assertNothingStored(t, m)

	// ...and a legitimate variable NAME publishes fine.
	fine := strings.Replace(sampleWorkflow, "    tools: [read]\n",
		"    tools: [read]\n    env:\n      OPENAI_API_KEY: \"${OPENAI_API_KEY}\"\n", 1)
	_, d2, tar2 := m.buildBundle(t, fine, "1.0.0")
	if w := do(m.h, "POST", "/api/bundles", tok, publishBodyFor(d2, tar2, "1.0.0")); w.Code != 201 {
		t.Fatalf("OPENAI_API_KEY was refused: %d %s", w.Code, w.Body.String())
	}
}

// task 10.4 / 9.5 — tampered bytes are a 400, nothing is indexed and nothing is
// written under the claimed key.
func TestTamperedBytesAreRefusedAndNothingIsStored(t *testing.T) {
	m := newMachine(t, ":8090")
	m.installSkill(t, "fix-author", "x")
	tok := user(t, m.st, "ana", "publisher", "")
	_, digest, tar := m.buildBundle(t, sampleWorkflow, "1.0.0")
	tar[len(tar)/2] ^= 0xff // corrupt the archive

	w := do(m.h, "POST", "/api/bundles", tok, publishBodyFor(digest, tar, "1.0.0"))
	if w.Code != 400 {
		t.Fatalf("tampered bytes answered %d: %s", w.Code, w.Body.String())
	}
	assertNothingStored(t, m)
	if _, err := os.Stat(filepath.Join(m.dir, "blobs", "bundles", bundle.Hex(digest))); err == nil {
		t.Fatal("bytes were written under the claimed key")
	}
}

func assertNothingStored(t *testing.T, m *machine) {
	t.Helper()
	rows, err := m.st.ListBundles(context.Background(), store.BundleFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("a refused publish stored %d row(s): %+v", len(rows), rows)
	}
}

// task 10.8 + non-negotiable 5 — on a loopback install with no users the
// publishing routes are ABSENT, not merely refused.
func TestPublishingIsAbsentOnALoopbackInstall(t *testing.T) {
	m := newMachine(t, "127.0.0.1:8090")
	for _, path := range []string{"/api/bundles", "/api/bundles/sha256:abc"} {
		if w := do(m.h, "GET", path, "", nil); w.Code != 404 {
			t.Fatalf("%s answered %d on a loopback install, want 404: %s", path, w.Code, w.Body.String())
		}
	}
	if w := do(m.h, "POST", "/api/bundles", "", map[string]any{}); w.Code != 404 {
		t.Fatalf("POST /api/bundles answered %d, want 404", w.Code)
	}
	// ...and the rest of the API still serves, unauthenticated, as before.
	if w := do(m.h, "GET", "/api/workflows", "", nil); w.Code != 200 {
		t.Fatalf("a loopback install stopped serving workflows: %d", w.Code)
	}
	n, err := m.st.CountUsers(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("a loopback install created %d user(s) (err %v)", n, err)
	}
}

// task 11.1 + 11.4 — THE PORTABILITY CLAIM. Publish on a machine that has the
// skill, install on one that has NONE of it, and assert the step declarations
// are identical; then plant a DIFFERENT same-named skill on the receiving
// machine and assert the carried copy still wins and the local one is Shadowed.
func TestABundleRunsIdenticallyOnAMachineHoldingNoneOfItsSkills(t *testing.T) {
	a := newMachine(t, ":8090")
	a.installSkill(t, "fix-author", "fix the author")
	tokA := user(t, a.st, "ana", "publisher", "")
	_, digest, tar := a.buildBundle(t, sampleWorkflow, "1.0.0")
	if w := do(a.h, "POST", "/api/bundles", tokA, publishBodyFor(digest, tar, "1.0.0")); w.Code != 201 {
		t.Fatalf("publish: %d %s", w.Code, w.Body.String())
	}
	// Machine A also runs it, so the two are comparable.
	defA, err := workflow.ParseYAML([]byte(sampleWorkflow))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.eng.SaveWorkflow(defA); err != nil {
		t.Fatal(err)
	}

	// Machine B holds nothing. It receives the same bytes.
	b := newMachine(t, ":8091")
	tokB := user(t, b.st, "bo", "publisher", "")
	if len(b.eng.Skills().List()) != 0 {
		t.Fatalf("the receiving machine was not empty: %+v", b.eng.Skills().List())
	}
	if w := do(b.h, "POST", "/api/bundles", tokB, publishBodyFor(digest, tar, "1.0.0")); w.Code != 201 {
		t.Fatalf("receiving publish: %d %s", w.Code, w.Body.String())
	}
	if w := do(b.h, "POST", "/api/bundles/"+digest+"/install", tokB, nil); w.Code != 200 {
		t.Fatalf("install: %d %s", w.Code, w.Body.String())
	}

	dryA, err := a.eng.DryRunWorkflow("code-review", map[string]any{"title": "t"})
	if err != nil {
		t.Fatal(err)
	}
	dryB, err := b.eng.DryRunWorkflow("code-review", map[string]any{"title": "t"})
	if err != nil {
		t.Fatalf("the receiving machine cannot run it: %v", err)
	}
	if !reflect.DeepEqual(dryA.Steps, dryB.Steps) {
		t.Fatalf("the step declarations differ:\n A %+v\n B %+v", dryA.Steps, dryB.Steps)
	}
	// Output contracts too.
	for i := range a.eng.Definitions()["code-review"].Steps {
		wantSchema := a.eng.Definitions()["code-review"].Steps[i].OutputSchema
		gotSchema := b.eng.Definitions()["code-review"].Steps[i].OutputSchema
		if !reflect.DeepEqual(wantSchema, gotSchema) {
			t.Fatalf("output contract differs on step %d:\n A %v\n B %v", i, wantSchema, gotSchema)
		}
	}
	if sk, ok := b.eng.Skills().Get("fix-author"); !ok {
		t.Fatal("the carried skill did not resolve on the receiving machine")
	} else if !strings.Contains(sk.Root, "bundles") {
		t.Fatalf("the skill resolved to %s, not the bundle root", sk.Root)
	}

	// A CONFLICTING local skill of the same name does not change the run: the
	// carried copy wins and the machine's own is recorded as Shadowed.
	b.installSkill(t, "fix-author", "a completely different skill")
	sk, ok := b.eng.Skills().Get("fix-author")
	if !ok {
		t.Fatal("fix-author disappeared")
	}
	if !strings.Contains(sk.Root, "bundles") {
		t.Fatalf("a local skill won over the bundle's: %s", sk.Root)
	}
	if len(sk.Shadowed) == 0 {
		t.Fatal("the local skill was not recorded as Shadowed")
	}
	dryB2, err := b.eng.DryRunWorkflow("code-review", map[string]any{"title": "t"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dryA.Steps, dryB2.Steps) {
		t.Fatalf("a conflicting local skill changed the run:\n A %+v\n B %+v", dryA.Steps, dryB2.Steps)
	}
}

// task 11.2 — the platform boundary holds: materialisation is server work on a
// CLI-initiated pull, and no new platform tool was added.
func TestNoPlatformToolWasAdded(t *testing.T) {
	for _, n := range []string{"bundle_pull", "bundle_install", "publish", "wfx_publish"} {
		if skills.IsPlatformTool(n) {
			t.Fatalf("a bundle tool was handed to a step: %s", n)
		}
	}
	// The list itself is unchanged by this change: materialisation is done by
	// the SERVER on a CLI-initiated publish or pull, never by a tool.
	if len(skills.PlatformTools()) == 0 {
		t.Fatal("the platform tool list disappeared")
	}
}

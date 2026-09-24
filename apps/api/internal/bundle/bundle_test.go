package bundle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/blob"
	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func skillTree(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "SKILL.md"), "---\nname: "+name+"\ndescription: d\n---\nbody\n")
	write(t, filepath.Join(dir, "references", "method.md"), "how\n")
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// task 9.1 — a directory's digest is reproducible from its CONTENTS alone: not
// from tar ordering, not from mtimes, not from permissions.
func TestADirectoryDigestIgnoresMtimeAndMode(t *testing.T) {
	dir := skillTree(t, "fix-author")
	first, _, err := DigestDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "SKILL.md"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "SKILL.md"), time0(), time0()); err != nil {
		t.Fatal(err)
	}
	second, _, err := DigestDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("a mode and mtime change moved the digest: %s -> %s", first, second)
	}
	// ...and content does move it.
	write(t, filepath.Join(dir, "references", "method.md"), "how, differently\n")
	third, _, err := DigestDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("changed content did not change the digest")
	}
}

// task 9.2 — pack, unpack, compare trees and digests.
func TestPackUnpackRoundTrip(t *testing.T) {
	b := New("workflow", "", "code-review", "1.0.0")
	b.AddWorkflow([]byte("name: code-review\n"))
	if err := b.AddSkillDir("fix-author", skillTree(t, "fix-author")); err != nil {
		t.Fatal(err)
	}
	digest, err := b.Verify("")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := b.Pack()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Unpack(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, err := back.Verify(digest)
	if err != nil {
		t.Fatalf("round trip did not verify: %v", err)
	}
	if got != digest {
		t.Fatalf("digest moved across a round trip: %s -> %s", digest, got)
	}
	if len(back.Files) != len(b.Files) {
		t.Fatalf("carried %d files, got %d back", len(b.Files), len(back.Files))
	}
	for p, want := range b.Files {
		if string(back.Files[p]) != string(want) {
			t.Fatalf("%s differs across the round trip", p)
		}
	}
}

// task 10.4 — tampered bytes are refused, and the claimed digest is not enough.
func TestTamperedContentIsRefused(t *testing.T) {
	b := New("workflow", "", "code-review", "1.0.0")
	b.AddWorkflow([]byte("name: code-review\n"))
	if err := b.AddSkillDir("fix-author", skillTree(t, "fix-author")); err != nil {
		t.Fatal(err)
	}
	digest, _ := b.Verify("")
	b.Files["skills/fix-author/SKILL.md"] = []byte("tampered\n")
	if _, err := b.Verify(digest); err == nil {
		t.Fatal("tampered skill content verified")
	} else if !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("wrong refusal: %v", err)
	}
	// A claimed digest that is not the manifest's own is refused too.
	clean := New("workflow", "", "code-review", "1.0.0")
	clean.AddWorkflow([]byte("name: code-review\n"))
	if _, err := clean.Verify("sha256:" + strings.Repeat("0", 64)); err == nil {
		t.Fatal("a false claimed digest verified")
	}
}

// task 9.3 — the keys are hex, so none of them can escape the Folder driver.
func TestBundleKeysCannotEscapeTheFolderStore(t *testing.T) {
	dir := t.TempDir()
	f, err := blob.OpenFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := New("workflow", "", "w", "1.0.0")
	b.AddWorkflow([]byte("name: w\n"))
	digest, _ := b.Verify("")
	tar, _ := b.Pack()
	manifest, _ := b.Manifest.Canonical()
	if err := Save(context.Background(), f, digest, tar, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bundles", Hex(digest), "bundle.tar.gz")); err != nil {
		t.Fatalf("the bundle is not under bundles/<hex>/: %v", err)
	}
	got, err := Load(context.Background(), f, digest)
	if err != nil || len(got) != len(tar) {
		t.Fatalf("read back %d bytes (err %v), wrote %d", len(got), err, len(tar))
	}
	if strings.ContainsAny(Hex(digest), "./\\") {
		t.Fatal("a digest that could escape the store")
	}
}

// [SEC-TEST] task 10.2 — the literal-credential refusal, and that it never
// echoes the value.
func TestPublishRefusesALiteralCredential(t *testing.T) {
	const pasted = "sk-proj-AAAABBBBCCCCDDDDEEEEFFFF/1234+5678=="
	def := &workflow.Definition{Name: "w", Steps: []workflow.Step{{ID: "one", Env: map[string]string{pasted: "x"}}}}
	err := CheckNoLiteralCredential(def, nil, nil, nil)
	if err == nil {
		t.Fatal("a pasted key in a step env key was accepted")
	}
	if strings.Contains(err.Error(), pasted) {
		t.Fatalf("the refusal echoed the value: %v", err)
	}
	if !strings.Contains(err.Error(), "NAME of an environment variable") {
		t.Fatalf("wrong message: %v", err)
	}
	// A provider's apiKeyEnv, same rule, same function.
	ok := &workflow.Definition{Name: "w", Steps: []workflow.Step{{ID: "one", Env: map[string]string{"OPENAI_API_KEY": "${OPENAI_API_KEY}"}}}}
	if err := CheckNoLiteralCredential(ok, nil, []catalog.Provider{{Name: "p", APIKeyEnv: "OPENAI_API_KEY"}}, nil); err != nil {
		t.Fatalf("a legitimate variable NAME was refused: %v", err)
	}
	if err := CheckNoLiteralCredential(ok, nil, []catalog.Provider{{Name: "p", APIKeyEnv: pasted}}, nil); err == nil {
		t.Fatal("a pasted provider key was accepted")
	}
}

func time0() time.Time { return time.Unix(1000000, 0) }

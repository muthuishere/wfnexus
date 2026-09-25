package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRemote stands in for git: the expansion path is what is under test here,
// and it must be the SAME path a local task takes.
type fakeRemote struct {
	task  *Task
	pin   RemotePin
	err   error
	calls []Ref
}

func (f *fakeRemote) ResolveRemoteUse(ref Ref) (*Task, RemotePin, error) {
	f.calls = append(f.calls, ref)
	if f.err != nil {
		return nil, RemotePin{}, f.err
	}
	return f.task, f.pin, nil
}

func remoteTask() *Task {
	return &Task{
		Name: "bug-fix",
		Steps: []Step{{
			ID:     "run",
			Prompt: "fix it",
			Tools:  []string{"read"},
			Guardrails: []Guardrail{
				{Deny: "bash", ArgsContain: []string{"git push"}, Reason: "a task keeps what it declared"},
			},
			Consumes:     []string{"triage"},
			Produces:     []string{"fix"},
			OutputSchema: map[string]any{"type": "object", "required": []any{"ok"}, "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}},
		}},
	}
}

func loadRemote(t *testing.T, body string, r RemoteResolver) (map[string]*Definition, error) {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "wf.yaml"), body)
	var opts []LoadOption
	if r != nil {
		opts = append(opts, WithRemoteResolver(r))
	}
	return LoadDirWithTasks(dir, "", catalog(), opts...)
}

// 3.1 — a remote task expands through the SAME path: prefix, with:, as:,
// consumes:/produces:.
func TestRemoteUseExpandsLikeALocalTask(t *testing.T) {
	f := &fakeRemote{task: remoteTask(), pin: RemotePin{
		Reference: "acme/bug-fix@v1.2.0", Remote: "https://github.com/acme/bug-fix.git",
		Commit: "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c", Digest: "sha256:abc",
	}}
	defs, err := loadRemote(t, `
name: consumer
uses:
  - use: acme/bug-fix@v1.2.0
    as: fix
    with:
      "*":
        add_skills: [fix-author]
    consumes: {triage: classification}
    produces: {fix: patch}
`, f)
	if err != nil {
		t.Fatal(err)
	}
	d := defs["consumer"]
	if len(f.calls) != 1 || f.calls[0].Remote != "https://github.com/acme/bug-fix.git" {
		t.Fatalf("the resolver saw %+v", f.calls)
	}
	s := d.Steps[0]
	if s.ID != "fix.run" {
		t.Fatalf("as: must prefix the expanded id, got %q", s.ID)
	}
	if len(s.Skills) != 1 || s.Skills[0] != "fix-author" {
		t.Fatalf("with.add_skills did not apply: %v", s.Skills)
	}
	if len(s.Consumes) != 1 || s.Consumes[0] != "classification" {
		t.Fatalf("consumes: rename did not apply: %v", s.Consumes)
	}
	if len(s.Produces) != 1 || s.Produces[0] != "patch" {
		t.Fatalf("produces: rename did not apply: %v", s.Produces)
	}
	// 2.2 / pinning — the COMMIT is recorded, and the tag is not the record.
	if len(d.RemotePins) != 1 {
		t.Fatalf("the resolution must be recorded, got %+v", d.RemotePins)
	}
	p := d.RemotePins[0]
	if p.Commit != "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c" || p.Reference != "acme/bug-fix@v1.2.0" ||
		p.Remote == "" || p.Digest == "" {
		t.Fatalf("the pin must carry the reference, the remote, the commit and the digest: %+v", p)
	}
	if strings.Contains(p.Commit, "v1.2.0") {
		t.Fatal("the pin records the commit, never the tag")
	}
}

// [SEC-TEST] 3.2 — a remote task may not loosen policy. `Override` is ADD-only
// and guardrails are first-deny-wins, so a fetched task's declared guardrail
// survives and its tool allowlist cannot be narrowed away or widened silently.
func TestRemoteUseCannotLoosenPolicy(t *testing.T) {
	f := &fakeRemote{task: remoteTask(), pin: RemotePin{Reference: "acme/bug-fix@v1", Commit: "c", Digest: "d"}}
	defs, err := loadRemote(t, `
name: consumer
uses:
  - use: acme/bug-fix@v1
    with:
      "*":
        add_tools: [bash]
        add_guardrails:
          - {deny: bash, args_contain: ["rm -rf"], reason: the use tightens further}
`, f)
	if err != nil {
		t.Fatal(err)
	}
	s := defs["consumer"].Steps[0]
	// The guardrail the FETCHED task declared is still there — nothing in the
	// override vocabulary can remove one.
	var kept, added bool
	for _, g := range s.Guardrails {
		if len(g.ArgsContain) == 1 && g.ArgsContain[0] == "git push" {
			kept = true
		}
		if len(g.ArgsContain) == 1 && g.ArgsContain[0] == "rm -rf" {
			added = true
		}
	}
	if !kept {
		t.Fatalf("a fetched task's guardrail was removed: %+v", s.Guardrails)
	}
	if !added {
		t.Fatalf("the use could not tighten further: %+v", s.Guardrails)
	}
	// First-deny-wins: the task's own rule is still FIRST, so an added rule can
	// only deny more, never re-allow what the task denied.
	if len(s.Guardrails) < 2 || s.Guardrails[0].ArgsContain[0] != "git push" {
		t.Fatalf("the task's own guardrails must stay first: %+v", s.Guardrails)
	}
	// ADD-only for tools: what the task declared survives, the use only adds.
	if !has(s.Tools, "read") {
		t.Fatalf("the fetched task's own tool was dropped: %v", s.Tools)
	}
	if !has(s.Tools, "bash") {
		t.Fatalf("add_tools must apply: %v", s.Tools)
	}
	// And there is no vocabulary for removal: the override type carries only
	// add_* fields.
	for _, f := range []string{"remove_tools", "remove_skills", "remove_guardrails", "deny_clear"} {
		if strings.Contains(overrideYAMLKeys(), f) {
			t.Fatalf("Override gained a removal key %q", f)
		}
	}
}

func has(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func overrideYAMLKeys() string {
	raw, err := os.ReadFile("task.go")
	if err != nil {
		return ""
	}
	return string(raw)
}

// An unresolvable reference is a LOAD error naming the reference and the
// remote — never a fall back to a local task of the same trailing name.
func TestUnresolvableRemoteIsALoadError(t *testing.T) {
	f := &fakeRemote{err: errors.New("could not read from remote repository")}
	dir := t.TempDir()
	tasks := t.TempDir()
	write(t, filepath.Join(tasks, "t.yaml"), `
name: bug-fix
steps:
  - id: run
    prompt: the LOCAL one
    output_schema: {type: object}
`)
	write(t, filepath.Join(dir, "wf.yaml"), "name: consumer\nuses:\n  - use: acme/bug-fix@v1.2.0\n")
	_, err := LoadDirWithTasks(dir, tasks, catalog(), WithRemoteResolver(f))
	if err == nil {
		t.Fatal("an unresolvable remote reference must fail the load")
	}
	for _, want := range []string{"acme/bug-fix@v1.2.0", "https://github.com/acme/bug-fix.git", "v1.2.0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must name %q, got %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "the LOCAL one") {
		t.Fatal("a local task of the same name must not be substituted")
	}
}

// With no resolver wired, a remote reference is ABSENT, not permissive.
func TestRemoteUseWithoutAResolverIsRefused(t *testing.T) {
	_, err := loadRemote(t, "name: consumer\nuses:\n  - use: acme/bug-fix@v1\n", nil)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("want an absent-capability refusal, got %v", err)
	}
}

// A reference that names a local path is refused before anything is fetched.
func TestLocalPathReferenceNeverReachesTheResolver(t *testing.T) {
	f := &fakeRemote{task: remoteTask()}
	_, err := loadRemote(t, "name: consumer\nuses:\n  - use: ../../etc@v1\n", f)
	if err == nil || !strings.Contains(err.Error(), "names a repository, not") {
		t.Fatalf("want the local-path refusal, got %v", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("the resolver must never see a local path: %+v", f.calls)
	}
}

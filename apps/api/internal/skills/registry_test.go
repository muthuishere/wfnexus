package skills

import (
	"os"
	"path/filepath"
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"
)

func writeSkill(t *testing.T, root, name, desc, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDiscoversSkillsWithMetadata(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "fix-author", "Write the fix", "# body")
	writeSkill(t, root, "pr-reviewer", "Review it", "# body")

	r := Load(root)
	if got := len(r.List()); got != 2 {
		t.Fatalf("got %d skills, want 2: %+v", got, r.List())
	}
	s, ok := r.Get("fix-author")
	if !ok {
		t.Fatal("fix-author missing")
	}
	if s.Description != "Write the fix" {
		t.Fatalf("description = %q", s.Description)
	}
	if s.Root != root || s.Location == "" {
		t.Fatalf("root/location = %q / %q", s.Root, s.Location)
	}
	if !r.Has("pr-reviewer") || r.Has("nope") {
		t.Fatal("Has is wrong")
	}
}

func TestListIsSortedByName(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"zeta", "alpha", "mid"} {
		writeSkill(t, root, n, "d", "b")
	}
	got := Load(root).List()
	for i, want := range []string{"alpha", "mid", "zeta"} {
		if got[i].Name != want {
			t.Fatalf("position %d = %s, want %s", i, got[i].Name, want)
		}
	}
}

func TestEarlierRootShadowsLater(t *testing.T) {
	project, machine := t.TempDir(), t.TempDir()
	writeSkill(t, project, "shared", "the project one", "b")
	writeSkill(t, machine, "shared", "the machine one", "b")
	writeSkill(t, machine, "only-machine", "d", "b")

	r := Load(project, machine)
	s, _ := r.Get("shared")
	if s.Description != "the project one" {
		t.Fatalf("project skill did not win: %q", s.Description)
	}
	if len(s.Shadowed) != 1 {
		t.Fatalf("shadowing not recorded: %+v", s.Shadowed)
	}
	if !r.Has("only-machine") {
		t.Fatal("later root should still contribute new names")
	}
}

func TestSkippedReportsBadFrontmatter(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "broken")
	os.MkdirAll(dir, 0o755)
	// frontmatter with no name is skipped by toolnexus
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\ndescription: no name here\n---\nbody"), 0o644)

	r := Load(root)
	if len(r.List()) != 0 {
		t.Fatalf("nameless skill was loaded: %+v", r.List())
	}
	if len(r.Skipped()) == 0 {
		t.Fatal("skip was not reported — a typo would vanish silently")
	}
}

func TestMissingAndRootsFor(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	writeSkill(t, a, "one", "d", "x")
	writeSkill(t, b, "two", "d", "x")
	r := Load(a, b)

	if miss := r.Missing([]string{"one", "two"}); len(miss) != 0 {
		t.Fatalf("false missing: %v", miss)
	}
	miss := r.Missing([]string{"one", "ghost", "spectre"})
	if len(miss) != 2 || miss[0] != "ghost" {
		t.Fatalf("Missing = %v", miss)
	}
	roots := r.RootsFor([]string{"one", "two", "ghost"})
	if len(roots) != 2 {
		t.Fatalf("RootsFor = %v, want both roots", roots)
	}
	if got := r.RootsFor([]string{"one", "one"}); len(got) != 1 {
		t.Fatalf("RootsFor should dedupe: %v", got)
	}
}

func TestDefaultRootsSkipsMissingDirs(t *testing.T) {
	roots := DefaultRoots(filepath.Join(t.TempDir(), "does-not-exist"))
	for _, r := range roots {
		if st, err := os.Stat(r); err != nil || !st.IsDir() {
			t.Fatalf("DefaultRoots returned a non-directory: %s", r)
		}
	}
}

// --- built-in tool scoping ---

func TestBuiltinAllowlistDisablesEverythingElse(t *testing.T) {
	// The bug this guards: SelectBuiltins only drops a tool explicitly set
	// false, so a map of just the allowed names leaves the rest switched ON.
	cfg := BuiltinAllowlist([]string{"bash", "read"})
	got := map[string]bool{}
	for _, tool := range tn.SelectBuiltins(cfg) {
		got[tool.Name] = true
	}
	if len(got) != 2 || !got["bash"] || !got["read"] {
		t.Fatalf("allowlist leaked; got %v", keys(got))
	}
	for _, denied := range []string{"write", "edit", "apply_patch", "webfetch", "todowrite", "grep", "glob", "question"} {
		if got[denied] {
			t.Fatalf("%s should not be in a bash+read step", denied)
		}
	}
}

func TestBuiltinAllowlistEmptyMeansNoTools(t *testing.T) {
	if got := tn.SelectBuiltins(BuiltinAllowlist(nil)); len(got) != 0 {
		t.Fatalf("no tools requested but got %d", len(got))
	}
}

func TestBuiltinAllowlistFullSetIsTheWholeToolbox(t *testing.T) {
	var all, fromToolnexus []string
	for _, b := range Builtins() {
		all = append(all, b.Name)
		if !IsPlatformTool(b.Name) {
			fromToolnexus = append(fromToolnexus, b.Name)
		}
	}
	if len(fromToolnexus) != 10 {
		t.Fatalf("expected 10 toolnexus built-ins, got %d: %v", len(fromToolnexus), fromToolnexus)
	}
	// Granting everything yields every TOOLNEXUS built-in. A platform tool is
	// registered on the toolkit by the engine, so it is not something
	// SelectBuiltins can return.
	if got := tn.SelectBuiltins(BuiltinAllowlist(all)); len(got) != len(fromToolnexus) {
		t.Fatalf("granting every tool yielded %d, want %d", len(got), len(fromToolnexus))
	}
	// the Claude-style coding surface must all be present
	set := BuiltinNames()
	for _, want := range []string{"bash", "read", "write", "edit", "apply_patch", "grep", "glob", "webfetch", "todowrite", "question"} {
		if !set[want] {
			t.Fatalf("built-in %q is missing", want)
		}
	}
}

func TestMissingBuiltinsCatchesTypos(t *testing.T) {
	miss := MissingBuiltins([]string{"bash", "shell", "read", "cat"})
	if len(miss) != 2 || miss[0] != "shell" || miss[1] != "cat" {
		t.Fatalf("MissingBuiltins = %v", miss)
	}
	if miss := MissingBuiltins([]string{"bash", "apply_patch"}); len(miss) != 0 {
		t.Fatalf("false positive: %v", miss)
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A platform tool is a name a step may request, so it must validate — and it
// must NOT be handed to toolnexus, which has never heard of it.
func TestPlatformToolsAreNamesButNotToolnexusBuiltins(t *testing.T) {
	names := BuiltinNames()
	for _, p := range PlatformTools() {
		if !names[p.Name] {
			t.Errorf("%s is not a valid tool name, so a workflow naming it would be refused", p.Name)
		}
		if !IsPlatformTool(p.Name) {
			t.Errorf("%s is not recognised as a platform tool", p.Name)
		}
	}
	if miss := MissingBuiltins([]string{ToolCatalog, ToolDryRun, ToolValidate}); len(miss) != 0 {
		t.Fatalf("platform tools reported as unknown: %v", miss)
	}

	// A step asking ONLY for platform tools gets no shell, no file access —
	// it asked for none, and the allowlist must not read that as "all".
	if got := tn.SelectBuiltins(BuiltinAllowlist([]string{ToolCatalog, ToolDryRun})); len(got) != 0 {
		var names []string
		for _, t := range got {
			names = append(names, t.Name)
		}
		t.Fatalf("platform-only step was granted %v", names)
	}
}

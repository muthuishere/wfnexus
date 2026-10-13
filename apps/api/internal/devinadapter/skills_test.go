package devinadapter

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDevinPresetArgvCarriesModelAndPermissionMode(t *testing.T) {
	a := Devin(CLI{Model: "opus", PermissionMode: PermissionAcceptEdits})
	want := []string{"--prompt-file", PlaceholderFile, "-p", "--permission-mode", "accept-edits", "--model", "opus"}
	if !reflect.DeepEqual(a.Args, want) {
		t.Fatalf("argv = %v, want %v", a.Args, want)
	}
	if a.ModelFlag != "" {
		t.Fatalf("a pinned model must not be re-appended per turn, ModelFlag=%q", a.ModelFlag)
	}
	if d := Devin(CLI{}); d.Args[4] != PermissionBypass || d.ModelFlag != "--model" {
		t.Fatalf("defaults: argv=%v modelFlag=%q", d.Args, d.ModelFlag)
	}
}

func TestIsDevin(t *testing.T) {
	cases := []struct {
		preset string
		cmd    []string
		want   bool
	}{
		{"", nil, true}, {"devin", nil, true}, {"claude", nil, false},
		{"", []string{"/opt/homebrew/bin/devin", "-p"}, true}, {"", []string{"opencode"}, false},
	}
	for _, c := range cases {
		if got := IsDevin(c.preset, c.cmd); got != c.want {
			t.Errorf("IsDevin(%q,%v)=%v", c.preset, c.cmd, got)
		}
	}
}

func TestMaterializeSkills(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("---\nname: greet\ndescription: d\n---\nhi"), 0o644)
	os.MkdirAll(filepath.Join(src, "scripts"), 0o755)
	os.WriteFile(filepath.Join(src, "scripts", "run.sh"), []byte("echo"), 0o755)

	work := t.TempDir()
	os.MkdirAll(filepath.Join(work, ".git", "info"), 0o755)
	stale := filepath.Join(work, DevinSkillsDir, "greet", "old.txt")
	os.MkdirAll(filepath.Dir(stale), 0o755)
	os.WriteFile(stale, []byte("x"), 0o644)

	for i := 0; i < 2; i++ { // idempotent
		if err := MaterializeSkills(work, map[string]string{"greet": src}); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(work, DevinSkillsDir, "greet", "SKILL.md")); !strings.Contains(string(b), "name: greet") {
		t.Fatalf("SKILL.md not copied: %q", b)
	}
	if st, err := os.Stat(filepath.Join(work, DevinSkillsDir, "greet", "scripts", "run.sh")); err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Fatalf("script not copied executable: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("stale file from an earlier copy survived")
	}
	ex, _ := os.ReadFile(filepath.Join(work, ".git", "info", "exclude"))
	if strings.Count(string(ex), "/.devin/skills/") != 1 {
		t.Fatalf("exclude = %q", ex)
	}
	if err := MaterializeSkills(work, map[string]string{"../x": src}); err == nil {
		t.Fatal("a path-like skill name must be refused")
	}
}

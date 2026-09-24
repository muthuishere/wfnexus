package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The exact escape that happened: the agent cd'd into the platform's own repo
// and ran git there, because `workdir` only sets the INITIAL directory.
func TestContainmentBlocksTheRealEscape(t *testing.T) {
	ws := t.TempDir()
	rail := containmentGuardrail(ws)
	cmd := `cd /srv/wfnexus && pwd && find . -name "*.py" | head -20`
	reason := rail(tn.BeforeToolEvent{Name: "bash", Args: map[string]any{"command": cmd}})
	if reason == "" {
		t.Fatal("the observed escape was allowed")
	}
	if !contains2([]string{reason}, reason) || len(reason) < 20 {
		t.Fatalf("the denial must explain itself: %q", reason)
	}
}

func TestContainmentDeniesEveryEscapeShape(t *testing.T) {
	ws := t.TempDir()
	rail := containmentGuardrail(ws)
	escapes := map[string]string{
		"absolute cd":      `cd /etc && cat passwd`,
		"cd after a ;":     `pwd ; cd /tmp && ls`,
		"cd after &&":      `ls && cd /usr/local`,
		"relative climb":   `cd ../../ && git status`,
		"pushd":            `pushd /var/log`,
		"cd back":          `cd -`,
		"home":             `cd ~`,
		"home path":        `cd ~/muthu`,
		"git -C elsewhere": `git -C /Users/someone/repo log`,
		"git --git-dir":    `git --git-dir=/elsewhere/.git status`,
		"unexpandable":     `cd $HOME && rm -rf .`,
		"quoted path":      `cd "/Users/someone/repo" && ls`,
	}
	for name, cmd := range escapes {
		t.Run(name, func(t *testing.T) {
			if rail(tn.BeforeToolEvent{Name: "bash", Args: map[string]any{"command": cmd}}) == "" {
				t.Fatalf("escape allowed: %s", cmd)
			}
		})
	}
}

func TestContainmentAllowsHonestWork(t *testing.T) {
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, "apps", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	rail := containmentGuardrail(ws)
	fine := map[string]string{
		"no cd at all":     `python -m pytest -q`,
		"relative cd":      `cd apps/api && go test ./...`,
		"cd into the root": `cd ` + ws + ` && git status`,
		"git in place":     `git checkout -b fix/thing && git commit -am "fix"`,
		"nested then back": `cd apps && cd api && ls`,
		"pipes and greps":  `grep -rn "discount" . | head -20`,
	}
	for name, cmd := range fine {
		t.Run(name, func(t *testing.T) {
			if reason := rail(tn.BeforeToolEvent{Name: "bash", Args: map[string]any{"command": cmd}}); reason != "" {
				t.Fatalf("honest command denied: %s\n%s", cmd, reason)
			}
		})
	}
}

// This test used to assert the opposite — that containment governed bash and
// nothing else — and that assertion is what let a live `test-backfill` run
// write an 18 KB file into the platform's own checkout through `write` with a
// relative path. The tool call was honest, the guardrail never looked, and the
// builtin resolved the path against the server process's cwd. The test encoded
// the hole, so it is inverted here rather than deleted.
func TestContainmentGovernsEveryTool(t *testing.T) {
	ws := t.TempDir()
	rail := containmentGuardrail(ws)

	outside := []struct {
		name string
		args map[string]any
	}{
		{"read", map[string]any{"path": "/etc/passwd"}},
		{"write", map[string]any{"path": "/tmp/pwned", "content": "x"}},
		{"edit", map[string]any{"path": "../../elsewhere/main.go"}},
		{"glob", map[string]any{"path": "/"}},
		{"apply_patch", map[string]any{"patchText": "*** Begin Patch\n*** Add File: /tmp/pwned\n+x\n*** End Patch"}},
		{"apply_patch", map[string]any{"patchText": "*** Begin Patch\n*** Update File: ../../other/repo/go.mod\n+x\n*** End Patch"}},
	}
	for _, tc := range outside {
		if r := rail(tn.BeforeToolEvent{Name: tc.name, Args: tc.args}); r == "" {
			t.Errorf("%s %v was allowed out of the workspace", tc.name, tc.args)
		}
	}

	// Relative paths are the normal case and must stay cheap: they resolve
	// against the WORKSPACE, so they are inside by definition.
	inside := []struct {
		name string
		args map[string]any
	}{
		{"read", map[string]any{"path": "apps/api/main.go"}},
		{"write", map[string]any{"path": "internal/planner/planner_test.go", "content": "x"}},
		{"read", map[string]any{"path": filepath.Join(ws, "go.mod")}},
		{"apply_patch", map[string]any{"patchText": "*** Begin Patch\n*** Add File: internal/x_test.go\n+x\n*** End Patch"}},
		{"todowrite", map[string]any{"todos": "[]"}},
	}
	for _, tc := range inside {
		if r := rail(tn.BeforeToolEvent{Name: tc.name, Args: tc.args}); r != "" {
			t.Errorf("%s %v denied inside the workspace: %s", tc.name, tc.args, r)
		}
	}

	if r := containmentGuardrail("")(tn.BeforeToolEvent{Name: "bash", Args: map[string]any{"command": "cd /etc"}}); r != "" {
		t.Fatal("with no workspace there is nothing to contain")
	}
}

// The other half of the same fix: the path a tool is HANDED must already point
// inside the workspace, because the builtin resolves it against this process's
// working directory and nothing else would.
func TestRelativePathsArePinnedToTheWorkspace(t *testing.T) {
	ws := t.TempDir()

	got := pinPaths("write", map[string]any{"path": "internal/planner/planner_test.go", "content": "x"}, ws)
	if got == nil || got["path"] != filepath.Join(ws, "internal/planner/planner_test.go") {
		t.Fatalf("relative write path not pinned: %v", got)
	}
	if got["content"] != "x" {
		t.Fatal("pinning dropped an unrelated argument")
	}

	// An absolute path is left alone — rewriting it would silently redirect a
	// call the agent meant literally. The guardrail denies it instead.
	if got := pinPaths("read", map[string]any{"path": "/etc/passwd"}, ws); got != nil {
		t.Fatalf("absolute path was rewritten: %v", got)
	}
	if got := pinPaths("read", map[string]any{"path": "x"}, ""); got != nil {
		t.Fatal("with no workspace there is nothing to pin")
	}

	patch := "*** Begin Patch\n*** Add File: internal/x_test.go\n+package x\n*** End Patch"
	got = pinPaths("apply_patch", map[string]any{"patchText": patch}, ws)
	if got == nil || !strings.Contains(got["patchText"].(string), "*** Add File: "+filepath.Join(ws, "internal/x_test.go")) {
		t.Fatalf("patch paths not pinned: %v", got)
	}
}

// Containment runs AHEAD of a step's own rules, so YAML can never widen it.
func TestContainmentCannotBeWidenedByYAML(t *testing.T) {
	ws := t.TempDir()
	rails := withContainment(ws, nil, []workflow.Guardrail{
		{Deny: "bash", ArgsContain: []string{"nothing-matches-this"}, Reason: "unrelated"},
	})
	if len(rails) != 2 {
		t.Fatalf("expected containment + 1 rule, got %d", len(rails))
	}
	ev := tn.BeforeToolEvent{Name: "bash", Args: map[string]any{"command": "cd /etc && ls"}}
	if rails[0](ev) == "" {
		t.Fatal("containment must be first and must deny")
	}
}

// A step's agent and its sub-agents are contained identically — a child with a
// shell could otherwise walk out on the parent's behalf.
func TestSubAgentsAreContainedToo(t *testing.T) {
	e := &Engine{}
	step := &workflow.Step{
		ID: "s", Team: []workflow.TeamMember{{ID: "explorer", Does: "reads", Tools: []string{"bash"}}},
	}
	ws := t.TempDir()
	team, tks, err := e.buildTeam(t.Context(), step, ws, nil, nil, nil)
	for _, tk := range tks {
		defer tk.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(team) != 1 {
		t.Fatalf("team = %d", len(team))
	}
	rails := team[0].Spec.Guardrails
	if len(rails) == 0 {
		t.Fatal("the sub-agent has no guardrails at all")
	}
	if rails[0](tn.BeforeToolEvent{Name: "bash", Args: map[string]any{"command": "cd /etc && ls"}}) == "" {
		t.Fatal("a sub-agent can leave the workspace")
	}
}

// The platform matrix is Linux, WSL, macOS and Windows. "Absolute" is not one
// thing across them, and a spelling the scanner does not recognise is a
// spelling it silently permits.
func TestAbsolutePathsAcrossPlatforms(t *testing.T) {
	cases := map[string]bool{
		"/etc/passwd":                    true,
		`C:\Windows\win.ini`:             true, // drive letter, backslashes
		"C:/Windows/win.ini":             true, // drive letter, forward slashes
		`D:\other\repo`:                  true,
		`\\fileserver\share\secrets.txt`: true, // UNC
		"/c/Users/me/.ssh/id_rsa":        true, // Git Bash's POSIX view of C:
		"/mnt/c/Users/me/.ssh/id_rsa":    true, // WSL's view of C:
		"relative/path.go":               false,
		"./x":                            false,
		"../up":                          false,
		"-":                              false,
		"C:":                             false, // a bare drive, not a path
		"http://example.com/x":           false,
	}
	for tok, want := range cases {
		got := len(absolutePaths("cat "+tok)) == 1
		if got != want {
			t.Errorf("absolutePaths(%q) detected=%v, want %v", tok, got, want)
		}
	}
}

// A Windows workspace and a differently-cased path argument must compare as the
// same place. This is the Windows form of the macOS /var symlink bug: root and
// target resolved by different rules, so an ordinary write read as an escape.
func TestWindowsPathsAreComparedCaseInsensitively(t *testing.T) {
	if runtime.GOOS != "windows" {
		// caseFold is a no-op elsewhere by design, so assert THAT rather than
		// skipping — a change making it fold everywhere would hide two
		// genuinely distinct paths on a case-sensitive volume.
		if caseFold("/Work/Main.go") != "/Work/Main.go" {
			t.Fatal("caseFold must not fold paths off Windows")
		}
		return
	}
	ws := t.TempDir()
	rail := containmentGuardrail(ws)
	mixed := filepath.Join(strings.ToUpper(ws), "main.go")
	if r := rail(tn.BeforeToolEvent{Name: "read", Args: map[string]any{"path": mixed}}); r != "" {
		t.Fatalf("same directory, different case, denied: %s", r)
	}
}

// An interpreter and the system libraries have to stay reachable on every
// platform, or nothing runs; a token that is neither is still an escape.
func TestSystemReadableCoversEveryPlatform(t *testing.T) {
	readable := []string{
		"/usr/bin/python3", "/bin/sh", "/lib64/ld-linux-x86-64.so.2", "/opt/homebrew/bin/go",
		"/dev/null", "/proc/cpuinfo", "/nix/store/abc-bash/bin/bash",
		`C:\Windows\System32\cmd.exe`, "C:/Program Files/Git/bin/bash.exe",
		"/c/Program Files/Git/usr/bin/sh", "/mingw64/bin/gcc.exe",
	}
	for _, p := range readable {
		if !systemReadable(p) {
			t.Errorf("%q should be readable — denying it breaks ordinary work", p)
		}
	}
	notReadable := []string{
		"/etc/passwd", "/home/me/.ssh/id_rsa", "/Users/me/secrets.env",
		`C:\Users\me\.ssh\id_rsa`, "/c/Users/me/.aws/credentials", "/var/lib/secret",
	}
	for _, p := range notReadable {
		if systemReadable(p) {
			t.Errorf("%q must NOT be waved through as a system path", p)
		}
	}
}

package engine

import (
	"os"
	"path/filepath"
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/workflow"
)

// The exact escape that happened: the agent cd'd into the platform's own repo
// and ran git there, because `workdir` only sets the INITIAL directory.
func TestContainmentBlocksTheRealEscape(t *testing.T) {
	ws := t.TempDir()
	rail := containmentGuardrail(ws)
	cmd := `cd /Users/muthuishere/muthu/gitworkspace/bug-fixer-platform && pwd && find . -name "*.py" | head -20`
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

func TestContainmentOnlyGovernsBash(t *testing.T) {
	rail := containmentGuardrail(t.TempDir())
	if r := rail(tn.BeforeToolEvent{Name: "read", Args: map[string]any{"path": "/etc/passwd"}}); r != "" {
		t.Fatalf("containment should govern bash only, got %q", r)
	}
	if r := containmentGuardrail("")(tn.BeforeToolEvent{Name: "bash", Args: map[string]any{"command": "cd /etc"}}); r != "" {
		t.Fatal("with no workspace there is nothing to contain")
	}
}

// Containment runs AHEAD of a step's own rules, so YAML can never widen it.
func TestContainmentCannotBeWidenedByYAML(t *testing.T) {
	ws := t.TempDir()
	rails := withContainment(ws, []workflow.Guardrail{
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
	team, tks, err := e.buildTeam(t.Context(), step, ws, nil, nil)
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

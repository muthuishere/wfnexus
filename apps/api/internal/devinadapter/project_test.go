// Parked until toolnexus exports InProcessTransport (toolnexus issue #95,
// shipped on the issues-devin-acp branch, not in v0.18.1 which this module
// pins). Without the tag transport.go breaks `go build ./...` for the whole
// module, and the package is tagged as a unit so its tests keep compiling.
// Nothing is deleted:
//   go test -tags toolnexus_inprocess ./internal/devinadapter/
// Drop the tag once a toolnexus release carries the export.

package devinadapter_test

// The adapter against THIS repo's real assets: the skills in /skills and the
// output contract of the `validate-bug` step in /workflows/bug-fix.yaml. The
// tests above prove the adapter works; these prove it works for the thing the
// project actually needs it to do.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	toolnexus "github.com/muthuishere/toolnexus/golang"
	devinadapter "github.com/muthuishere/wfnexus/apps/api/internal/devinadapter"
)

// repoRoot is four levels up: apps/api/internal/devinadapter.
const repoRoot = "../../../.."

func repoSkills(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(repoRoot, "skills")
	if _, err := os.Stat(filepath.Join(dir, "validate-bug", "SKILL.md")); err != nil {
		t.Skipf("repo skills not found: %v", err)
	}
	return dir
}

// Validation is the output_schema of the bug-fix workflow's validate-bug step,
// as a Go struct. Its json tags are what toolnexus advertises to the model.
type Validation struct {
	Valid       bool     `json:"valid"`
	Severity    string   `json:"severity"`
	Summary     string   `json:"summary"`
	Component   string   `json:"component"`
	MissingInfo []string `json:"missing_info"`
	Reasoning   string   `json:"reasoning"`
}

func submitOutput(into **Validation) toolnexus.Tool {
	return toolnexus.NativeToolReflect[Validation](
		"submit_output",
		"Submit the validation verdict for the bug report. Call exactly once, last.",
		func(_ context.Context, in Validation) (string, error) {
			v := in
			*into = &v
			return "recorded", nil
		},
	)
}

// The repo's six skills must all load and reach the model, with the step's
// tool allowlist (read/grep/glob/bash) intact.
func TestProjectSkillsLoad(t *testing.T) {
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		SkillsDir: []string{repoSkills(t)},
		ExtraTools: []toolnexus.Tool{
			toolnexus.NativeTool("submit_output", "Submit the verdict.", nil,
				func(context.Context, map[string]any) (string, error) { return "ok", nil }),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	back := &byName{reply: func(string, int) string { return answer("noted") }}
	runWith(t, back, tk, "hello")
	first := back.first()

	for _, skill := range []string{
		"validate-bug", "reproduce-bug", "fix-author",
		"pr-reviewer", "pr-publisher", "repo-navigator",
	} {
		if !strings.Contains(first, skill) {
			t.Errorf("skill %q never reached the model", skill)
		}
	}
	// The workflow's validate-bug step declares tools: [read, grep, glob, bash].
	for _, tool := range []string{"read", "grep", "glob", "bash"} {
		if !strings.Contains(first, `"name": "`+tool+`"`) {
			t.Errorf("step tool %q not advertised", tool)
		}
	}
}

// The validate-bug step end to end, scripted: skill load, a repo grep, then
// the schema'd verdict.
func TestProjectValidateBugStep(t *testing.T) {
	var got *Validation
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		SkillsDir:  []string{repoSkills(t)},
		ExtraTools: []toolnexus.Tool{submitOutput(&got)},
	})
	if err != nil {
		t.Fatal(err)
	}

	back := &byName{reply: func(_ string, calls int) string {
		switch calls {
		case 0:
			return toolCall("skill", map[string]any{"name": "validate-bug"})
		case 1:
			// The pattern is matched against paths RELATIVE to `path`, so the
			// search root goes in `path` and never in the pattern.
			root, _ := filepath.Abs(repoRoot)
			return toolCall("glob", map[string]any{"path": root, "pattern": "workflows/*.yaml"})
		case 2:
			return toolCall("submit_output", Validation{
				Valid: true, Severity: "high",
				Summary:     "coupon applies twice",
				Component:   "apps/api/internal/engine",
				MissingInfo: []string{},
				Reasoning:   "applyCoupon appends without a dedupe check",
			})
		default:
			return answer("verdict filed")
		}
	}}

	res := runWith(t, back, tk, "Bug: cart total wrong when a coupon is applied twice.")

	if got == nil {
		t.Fatalf("no verdict; status=%s", res.Status)
	}
	if !got.Valid || got.Severity != "high" {
		t.Errorf("verdict not round-tripped: %+v", *got)
	}
	// The skill body must genuinely have been delivered — it carries the
	// severity ladder the verdict is graded against.
	var sawSkill, sawGlob bool
	for _, c := range res.ToolCalls {
		t.Logf("called %-14s -> %.90s", c.Name, strings.ReplaceAll(c.Output, "\n", " "))
		if c.Name == "skill" && strings.Contains(c.Output, "Validate a bug report") {
			sawSkill = true
		}
		if c.Name == "glob" && strings.Contains(c.Output, "bug-fix.yaml") {
			sawGlob = true
		}
	}
	if !sawSkill {
		t.Error("the validate-bug skill body never came back")
	}
	if !sawGlob {
		t.Error("the glob builtin did not see the repo")
	}
}

// Live: the real validate-bug step, on the real repo, answered by devin over a
// warm ACP session — the shape a workflow step will actually run in.
func TestLiveProjectValidateBug(t *testing.T) {
	liveAgent(t) // skip guard
	skills := repoSkills(t)

	acp := devinadapter.NewACP(devinadapter.ACP{Model: liveModelName()})
	defer acp.Close()

	var got *Validation
	tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
		SkillsDir:  []string{skills},
		ExtraTools: []toolnexus.Tool{submitOutput(&got)},
	})
	if err != nil {
		t.Fatal(err)
	}

	a := devinadapter.New(devinadapter.Options{
		Agent:   acp,
		Workdir: t.TempDir(),
		Timeout: 8 * time.Minute,
		Trace: func(ex devinadapter.Exchange) {
			t.Logf("turn %d/%d (%d byte prompt)", ex.Turn, ex.Attempt, len(ex.Prompt))
		},
	})
	opts := a.InProcessOptions()
	opts.MaxTurns = 15 // the step's own max_turns
	opts.SystemPrompt = "You validate bug reports for the bug-fixer platform. Load the `validate-bug` skill, follow it, inspect the repository, then call submit_output."

	start := time.Now()
	res, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(),
		"Title: cart total is wrong when a coupon is applied twice\n\n"+liveBugReport, tk)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}

	for _, c := range res.ToolCalls {
		t.Logf("called %-14s -> %.70s", c.Name, strings.ReplaceAll(c.Output, "\n", " "))
	}
	t.Logf("%.1fs over %d turns, %d tool calls", took.Seconds(), res.Turns, res.ToolCallCount)

	if got == nil {
		t.Fatalf("no verdict; status=%s turns=%d text=%.200s", res.Status, res.Turns, res.Text)
	}
	b, _ := json.MarshalIndent(got, "", "  ")
	t.Logf("verdict:\n%s", b)

	switch got.Severity {
	case "low", "medium", "high", "critical":
	default:
		t.Errorf("severity %q is off the skill's ladder", got.Severity)
	}
	if got.Summary == "" || got.Reasoning == "" {
		t.Error("summary/reasoning not filled")
	}
	// The skill says: when valid=false, "list each missing item as a direct
	// question". Whether the model obeys is MODEL compliance, not wiring, and
	// it varies run to run — one live run returned valid=false with an empty
	// list. Reported, not failed, so this stays a check on the plumbing rather
	// than a coin flip on the model.
	if !got.Valid && len(got.MissingInfo) == 0 {
		t.Logf("SKILL RULE MISSED: valid=false with no missing_info questions")
	}
}

// Live experiment for toolnexus ADR 0025 gate 1: does a stateful ACP session
// actually answer a STALE copy of the request when every turn carries the full
// message array, and does the supersedes marker prevent it?
//
// The marker was added as a precaution, not in response to an observed
// failure. This is the attempt to observe it.
func TestLiveACPSupersedeExperiment(t *testing.T) {
	liveAgent(t) // skip guard

	// A two-step task: the model must call `step` twice with DIFFERENT values.
	// If it answers a stale copy of the request, it repeats the first call.
	probe := func(noSupersede bool) (calls []string, text string) {
		var seen []string
		step := toolnexus.NativeTool("step", "Record one step of the plan.",
			toolnexus.JSONSchema{
				"type":       "object",
				"properties": map[string]any{"name": map[string]any{"type": "string"}},
				"required":   []string{"name"},
			},
			func(_ context.Context, args map[string]any) (string, error) {
				name, _ := args["name"].(string)
				seen = append(seen, name)
				return "recorded " + name + "; now do the next step", nil
			})

		acp := devinadapter.NewACP(devinadapter.ACP{
			Model: liveModelName(), NoSupersede: noSupersede,
		})
		defer acp.Close()

		tk, err := toolnexus.CreateToolkit(context.Background(), toolnexus.Options{
			Builtins: false, ExtraTools: []toolnexus.Tool{step},
		})
		if err != nil {
			t.Fatal(err)
		}
		a := devinadapter.New(devinadapter.Options{Agent: acp, Workdir: t.TempDir(), Timeout: 6 * time.Minute})
		opts := a.InProcessOptions()
		opts.MaxTurns = 6

		res, err := toolnexus.CreateInProcessClient(opts).Run(context.Background(),
			"Call `step` with name=\"alpha\". When it returns, call `step` again with name=\"beta\". "+
				"Then answer with the word DONE. Never call step twice with the same name.", tk)
		if err != nil {
			t.Fatalf("noSupersede=%v: %v", noSupersede, err)
		}
		return seen, res.Text
	}

	withMarker, textWith := probe(false)
	t.Logf("with    supersedes: steps=%v text=%.40q", withMarker, textWith)

	without, textWithout := probe(true)
	t.Logf("without supersedes: steps=%v text=%.40q", without, textWithout)

	// The marker's own behaviour is what this package ships, so it must work.
	if len(withMarker) != 2 || withMarker[0] == withMarker[1] {
		t.Errorf("with the marker, expected two distinct steps, got %v", withMarker)
	}
	// Without it is the EXPERIMENT, not a requirement — a repeat here is the
	// stale-copy failure ADR 0025 is asking about.
	switch {
	case len(without) == 2 && without[0] != without[1]:
		t.Logf("GATE 1: no stale answer observed without the marker — the default may be unjustified")
	case len(without) >= 2 && without[0] == without[1]:
		t.Logf("GATE 1: STALE COPY REPRODUCED — repeated %q without the marker", without[0])
	default:
		t.Logf("GATE 1: inconclusive without the marker (steps=%v)", without)
	}
}

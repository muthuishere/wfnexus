package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// --- guardrails: policy on tool calls -------------------------------------

// A guardrail denies the call, the model is SHOWN the reason as the tool
// result, and the step still completes — policy is not a crash.
func TestGuardrailDeniesAndTheModelIsToldWhy(t *testing.T) {
	def := &workflow.Definition{Name: "policy", Steps: []workflow.Step{{
		ID: "work", Prompt: "do it", Tools: []string{"bash"},
		Guardrails: []workflow.Guardrail{{
			Deny: "bash", ArgsContain: []string{"git push"},
			Reason: "publishing is gated on human approval in this workflow",
		}},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)

	llm := newFakeLLM(t,
		bashCall("git push origin main"), // denied
		bashCall("echo allowed"),         // permitted
		submit(map[string]any{"ok": true}),
		finish(),
	)
	h := newHarness(t, def, llm, "")
	run := h.run(nil)

	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	// the denial must have reached the model as a tool result
	var denial string
	for _, m := range llm.toolResults() {
		if strings.Contains(m, "publishing is gated") {
			denial = m
		}
	}
	if denial == "" {
		t.Fatalf("the model was never shown the denial; tool results = %v", llm.toolResults())
	}
	// and the denied command must not have run
	for _, m := range llm.toolResults() {
		if strings.Contains(m, "Everything up-to-date") {
			t.Fatal("the denied command executed anyway")
		}
	}
}

func TestGuardrailLeavesOtherToolsAlone(t *testing.T) {
	def := &workflow.Definition{Name: "policy2", Steps: []workflow.Step{{
		ID: "work", Prompt: "do it", Tools: []string{"bash"},
		Guardrails:   []workflow.Guardrail{{Deny: "write", Reason: "read-only step"}},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)
	llm := newFakeLLM(t, bashCall("echo fine"), submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, "")
	if run := h.run(nil); run.Status != "done" {
		t.Fatalf("a guardrail on an unrelated tool blocked the step: %s (%s)", run.Status, run.Error)
	}
}

// compileGuardrails is the matching logic; the wildcard and arg matching are
// worth pinning independently of a live run.
func TestCompileGuardrailMatching(t *testing.T) {
	rails := compileGuardrails([]workflow.Guardrail{
		{Deny: "bash", ArgsContain: []string{"rm -rf", "git push"}, Reason: "dangerous"},
		{Deny: "*", ArgsContain: []string{"/etc/shadow"}, Reason: "no secrets"},
	})
	check := func(name string, args map[string]any) string {
		for _, r := range rails {
			if reason := r(tn.BeforeToolEvent{Name: name, Args: args}); reason != "" {
				return reason // first deny wins
			}
		}
		return ""
	}
	if got := check("bash", map[string]any{"command": "git push origin main"}); got != "dangerous" {
		t.Fatalf("match on args = %q", got)
	}
	if got := check("bash", map[string]any{"command": "GIT PUSH --force"}); got != "dangerous" {
		t.Fatalf("matching should be case-insensitive, got %q", got)
	}
	if got := check("bash", map[string]any{"command": "ls -la"}); got != "" {
		t.Fatalf("innocent command denied: %q", got)
	}
	if got := check("read", map[string]any{"path": "/etc/shadow"}); got != "no secrets" {
		t.Fatalf("wildcard rule = %q", got)
	}
	if got := check("read", map[string]any{"path": "main.go"}); got != "" {
		t.Fatalf("wildcard over-matched: %q", got)
	}
}

// --- sub-agent teams -------------------------------------------------------

// A step with a team gets the `task` tool and its child runs on its own
// transcript with only the tools the YAML scoped to it.
func TestStepDelegatesToItsTeam(t *testing.T) {
	def := &workflow.Definition{Name: "team", Steps: []workflow.Step{{
		ID: "lead", Prompt: "diagnose it", Tools: []string{},
		Team: []workflow.TeamMember{{
			ID: "explorer", Does: "read-only repository research", Tools: []string{"bash"},
		}},
		OutputSchema: objSchema([]any{"finding"}, map[string]any{"finding": strProp()}),
	}}}
	normalizeForTest(def)

	llm := newFakeLLM(t,
		turn{calls: []call{{name: "task", args: map[string]any{"agent": "explorer", "prompt": "find the bug"}}}},
		bashCall("grep -rn discount ."), // the CHILD's turn, on its own transcript
		turn{text: "cart.py multiplies by percent directly"},
		submit(map[string]any{"finding": "cart.py multiplies by percent directly"}),
		finish(),
	)
	h := newHarness(t, def, llm, "")
	run := h.run(nil)

	if run.Status != "done" {
		t.Fatalf("run = %s (%s)", run.Status, run.Error)
	}
	// the parent was offered task, and NOT the child's tools
	offered := llm.toolNamesOffered(0)
	if !contains2(offered, "task") {
		t.Fatalf("a step with a team must get the task tool; got %v", offered)
	}
	if contains2(offered, "bash") {
		t.Fatalf("the parent granted itself no tools but was offered bash: %v", offered)
	}
	// the child WAS offered bash on its own turn
	childOffered := llm.toolNamesOffered(1)
	if !contains2(childOffered, "bash") {
		t.Fatalf("the child's scoped tool was missing: %v", childOffered)
	}
	if contains2(childOffered, "submit_output") {
		t.Fatal("the child must not see the parent's submission tool")
	}
}

func TestNoTeamMeansNoTaskTool(t *testing.T) {
	def := &workflow.Definition{Name: "solo", Steps: []workflow.Step{{
		ID: "only", Prompt: "p", Tools: []string{"bash"},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)
	llm := newFakeLLM(t, submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, "")
	h.run(nil)
	if offered := llm.toolNamesOffered(0); contains2(offered, "task") {
		t.Fatalf("delegation must be opt-in, but task was offered: %v", offered)
	}
}

// --- durable suspension ----------------------------------------------------

// The agent asks a question, the run parks in needs_input with the question
// persisted, and answering re-runs the step with the answer in scope.
func TestAskHumanParksTheRunAndTheAnswerResumesIt(t *testing.T) {
	def := &workflow.Definition{Name: "ask", Steps: []workflow.Step{{
		ID: "gather", Prompt: "gather facts", Tools: []string{"bash"}, AskHuman: true,
		OutputSchema: objSchema([]any{"version"}, map[string]any{"version": strProp()}),
	}}}
	normalizeForTest(def)

	llm := newFakeLLM(t,
		turn{calls: []call{{name: "ask_human", args: map[string]any{
			"question": "Which version are you running?", "why": "the fix differs across majors",
		}}}},
		// after the answer the step re-runs from its prompt
		submit(map[string]any{"version": "2.1.0"}),
		finish(),
	)
	h := newHarness(t, def, llm, "")
	run := h.run(nil)

	if run.Status != "needs_input" {
		t.Fatalf("run = %s, want needs_input", run.Status)
	}
	if !strings.Contains(run.Error, "Which version") {
		t.Fatalf("the question was not surfaced: %q", run.Error)
	}
	st := h.steps(run.ID)["gather"]
	if st.Status != "needs_input" || len(st.Pending) == 0 {
		t.Fatalf("the suspension was not persisted: status=%s pending=%s", st.Status, st.Pending)
	}
	var req tn.Request
	if err := json.Unmarshal(st.Pending, &req); err != nil {
		t.Fatalf("stored Request is not decodable: %v", err)
	}
	if req.Kind != "input" || req.Prompt == "" {
		t.Fatalf("Request = %+v", req)
	}

	// the operator answers, hours later, possibly in another process
	if err := h.eng.AnswerQuestion(context.Background(), run.ID, "gather", "2.1.0"); err != nil {
		t.Fatal(err)
	}
	run = h.wait(run.ID)
	if run.Status != "done" {
		t.Fatalf("after the answer run = %s (%s)", run.Status, run.Error)
	}
	st = h.steps(run.ID)["gather"]
	if got := output(t, st)["version"]; got != "2.1.0" {
		t.Fatalf("output = %v", got)
	}
	if len(st.Pending) != 0 {
		t.Fatalf("the resolved question must be cleared, got %s", st.Pending)
	}
	// the answer must be in the run input for the re-run's prompt
	updated, _ := h.store.GetRun(context.Background(), run.ID)
	if !strings.Contains(string(updated.Input), "2.1.0") {
		t.Fatalf("answer not merged into the run input: %s", updated.Input)
	}
}

func TestAnswerRefusedWhenNothingWasAsked(t *testing.T) {
	def := &workflow.Definition{Name: "noask", Steps: []workflow.Step{{
		ID: "only", Prompt: "p", Tools: []string{"bash"},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)
	llm := newFakeLLM(t, submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	err := h.eng.AnswerQuestion(context.Background(), run.ID, "only", "hello")
	if err == nil || !strings.Contains(err.Error(), "not waiting") {
		t.Fatalf("answering an unasked step returned %v", err)
	}
}

// ask_human is only granted when the step asks for it.
func TestAskHumanToolIsOptIn(t *testing.T) {
	def := &workflow.Definition{Name: "noaskhuman", Steps: []workflow.Step{{
		ID: "only", Prompt: "p", Tools: []string{"bash"},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)
	llm := newFakeLLM(t, submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, "")
	h.run(nil)
	if offered := llm.toolNamesOffered(0); contains2(offered, "ask_human") {
		t.Fatalf("ask_human must be opt-in: %v", offered)
	}
}

// --- provider identifiers never reach the event log ------------------------

func TestScrubRemovesProviderIdentifiers(t *testing.T) {
	// OpenRouter's 400 body carries the account user_id (spikes/05)
	in := `LLM 400: {"error":{"message":"not a valid model ID","code":400},"user_id":"user_2abcDEF123"}`
	got := scrub(in)
	if strings.Contains(got, "user_2abcDEF123") {
		t.Fatalf("user_id survived scrubbing: %s", got)
	}
	if !strings.Contains(got, "not a valid model ID") {
		t.Fatalf("scrubbing destroyed the useful part: %s", got)
	}
	for _, secret := range []string{
		"sk-or-v1-0123456789abcdef0123456789abcdef",
		"Bearer abcdefghijklmnopqrstuvwxyz012345",
	} {
		if out := scrub("prefix " + secret + " suffix"); strings.Contains(out, secret) {
			t.Fatalf("%q survived scrubbing: %s", secret, out)
		}
	}
}

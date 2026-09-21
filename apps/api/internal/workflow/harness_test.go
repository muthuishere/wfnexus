package workflow

import (
	"strings"
	"testing"
)

// The harness a step declares — team, guardrails, budget, decide — is validated
// at LOAD time, so a typo is a boot error rather than a mid-run surprise.

func loadOne(t *testing.T, body string) error {
	t.Helper()
	dir := t.TempDir()
	writeWorkflow(t, dir, "x.yaml", body)
	_, err := LoadDir(dir, catalog())
	return err
}

const stepHead = "name: x\nsteps:\n  - id: a\n    prompt: p\n    tools: [bash]\n    output_schema: {type: object}\n"

func TestTeamValidation(t *testing.T) {
	bad := map[string]string{
		"member without does":    stepHead + "    team:\n      - {id: explorer, skills: [fix-author]}\n",
		"member without id":      stepHead + "    team:\n      - {does: research}\n",
		"duplicate member":       stepHead + "    team:\n      - {id: e, does: r}\n      - {id: e, does: r2}\n",
		"member id shadows step": stepHead + "    team:\n      - {id: a, does: r}\n",
		"member unknown skill":   stepHead + "    team:\n      - {id: e, does: r, skills: [ghost]}\n",
		"member unknown tool":    stepHead + "    team:\n      - {id: e, does: r, tools: [telepathy]}\n",
	}
	for label, body := range bad {
		t.Run(label, func(t *testing.T) {
			if err := loadOne(t, body); err == nil {
				t.Fatalf("%s was accepted", label)
			}
		})
	}
	good := stepHead + "    team:\n      - {id: explorer, does: read-only research, skills: [fix-author], tools: [read]}\n"
	if err := loadOne(t, good); err != nil {
		t.Fatalf("valid team rejected: %v", err)
	}
}

func TestGuardrailNeedsAReason(t *testing.T) {
	// the model is SHOWN the reason as the tool result, so a blank one is useless
	if err := loadOne(t, stepHead+"    guardrails:\n      - {deny: bash, args_contain: ['git push']}\n"); err == nil {
		t.Fatal("guardrail without a reason was accepted")
	}
	if err := loadOne(t, stepHead+"    guardrails:\n      - {deny: bash, args_contain: ['git push'], reason: publishing is gated}\n"); err != nil {
		t.Fatalf("valid guardrail rejected: %v", err)
	}
	if err := loadOne(t, stepHead+"    guardrails:\n      - {reason: nothing named}\n"); err == nil {
		t.Fatal("guardrail without a tool was accepted")
	}
}

func TestDecideQuestionValidation(t *testing.T) {
	bad := map[string]string{
		"unknown type":             stepHead + "    decide:\n      state: s\n      questions:\n        q: {type: vibes, instructions: i}\n",
		"no instructions":          stepHead + "    decide:\n      state: s\n      questions:\n        q: {type: noul}\n",
		"score with one level":     stepHead + "    decide:\n      state: s\n      questions:\n        q: {type: score, instructions: i, levels: [only]}\n",
		"score with eleven levels": stepHead + "    decide:\n      state: s\n      questions:\n        q:\n          type: score\n          instructions: i\n          levels: [a,b,c,d,e,f,g,h,i,j,k]\n",
		"no questions":             stepHead + "    decide:\n      state: s\n      questions: {}\n",
		"gate on unknown question": stepHead + "    decide:\n      state: s\n      questions:\n        q: {type: noul, instructions: i}\n      gates:\n        - {question: other, below: 0.5, action: fail}\n",
		"gate with two conditions": stepHead + "    decide:\n      state: s\n      questions:\n        q: {type: noul, instructions: i}\n      gates:\n        - {question: q, below: 0.5, at_least: 0.2, action: fail}\n",
		"gate with no condition":   stepHead + "    decide:\n      state: s\n      questions:\n        q: {type: noul, instructions: i}\n      gates:\n        - {question: q, action: fail}\n",
	}
	for label, body := range bad {
		t.Run(label, func(t *testing.T) {
			if err := loadOne(t, body); err == nil {
				t.Fatalf("%s was accepted", label)
			}
		})
	}
}

// The encoding obligation, enforced: options described only by their own id
// rank at chance, so the loader refuses them rather than shipping a judge that
// returns confident noise.
func TestDecideRefusesDegenerateChoiceOptions(t *testing.T) {
	degenerateCases := map[string]string{
		"id repeated as description": "        route:\n          type: choice\n          instructions: which desk\n          options: {billing: billing, shipping: shipping}\n",
		"all empty":                  "        route:\n          type: choice\n          instructions: which desk\n          options: {billing: '', shipping: ''}\n",
		"all identical":              "        route:\n          type: choice\n          instructions: which desk\n          options: {billing: the desk, shipping: the desk}\n",
	}
	for label, q := range degenerateCases {
		t.Run(label, func(t *testing.T) {
			err := loadOne(t, stepHead+"    decide:\n      state: s\n      questions:\n"+q)
			if err == nil {
				t.Fatalf("%s was accepted — it ranks at chance", label)
			}
			if !strings.Contains(err.Error(), "chance") {
				t.Fatalf("error should explain WHY: %v", err)
			}
		})
	}
	// described by consequence — accepted
	good := stepHead + "    decide:\n      state: s\n      questions:\n" +
		"        route:\n          type: choice\n          instructions: which desk owns this\n" +
		"          options:\n            billing: \"own it here when the problem is money - charges, refunds, invoices\"\n" +
		"            shipping: \"own it here when the problem is delivery - damage in transit, late parcels\"\n"
	if err := loadOne(t, good); err != nil {
		t.Fatalf("well-described options rejected: %v", err)
	}
}

func TestDecideAcceptsAFullBlock(t *testing.T) {
	body := stepHead + `    decide:
      state: "{{ .Input.title }}"
      questions:
        fixability:
          type: score
          instructions: how likely is an agent to fix this unaided
          levels:
            - "no chance: the report names no reproducible behaviour"
            - "possible: the behaviour is clear but the cause is not localised"
            - "likely: the failing call and the expected value are both stated"
        is_security:
          type: noul
          instructions: is this a security problem
          "true": the report describes data exposure or privilege escalation
          "false": the report describes ordinary incorrect behaviour
      gates:
        - {question: fixability, below: 1.0, action: needs_input, message: "too vague"}
`
	if err := loadOne(t, body); err != nil {
		t.Fatalf("full decide block rejected: %v", err)
	}
}

func TestBudgetAndSoulLoad(t *testing.T) {
	body := stepHead + "    soul: You are careful.\n" +
		"    ask_human: true\n" +
		"    budget: {max_turns: 40, max_tokens: 200000, max_tool_calls: 150, max_wall_sec: 900, max_concurrent: 4}\n"
	dir := t.TempDir()
	writeWorkflow(t, dir, "x.yaml", body)
	defs, err := LoadDir(dir, catalog())
	if err != nil {
		t.Fatal(err)
	}
	st := defs["x"].Steps[0]
	if st.Soul != "You are careful." || !st.AskHuman {
		t.Fatalf("soul/ask_human not parsed: %+v", st)
	}
	if st.Budget == nil || st.Budget.MaxTokens != 200000 || st.Budget.MaxWallSec != 900 {
		t.Fatalf("budget not parsed: %+v", st.Budget)
	}
}

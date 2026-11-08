package engine

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// stepAnswersKey is the run-input key holding each step's answered ask_human
// questions: {stepID: {question: answer}}. An answer re-runs the step from its
// prompt (answerQuestion), so the answer must travel with the step itself —
// a prompt that does not template `{{ .Input.answers }}` otherwise never sees
// it and the agent asks the same question again, forever.
const stepAnswersKey = "_step_answers"

func stepAnswers(input map[string]any, stepID string) map[string]string {
	all, _ := input[stepAnswersKey].(map[string]any)
	m, _ := all[stepID].(map[string]any)
	out := make(map[string]string, len(m))
	for q, a := range m {
		s, _ := a.(string)
		out[q] = s
	}
	return out
}

func recordStepAnswer(input map[string]any, stepID, question, answer string) {
	all, _ := input[stepAnswersKey].(map[string]any)
	if all == nil {
		all = map[string]any{}
	}
	m, _ := all[stepID].(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	m[question] = answer
	all[stepID] = m
	input[stepAnswersKey] = all
}

// withAnswersInPrompt appends the operator's answers to this step's earlier
// questions, so the re-run proceeds on them instead of asking again.
func withAnswersInPrompt(prompt string, answers map[string]string) string {
	if len(answers) == 0 {
		return prompt
	}
	qs := make([]string, 0, len(answers))
	for q := range answers {
		qs = append(qs, q)
	}
	sort.Strings(qs)
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\nThe operator already answered your questions for this step. Do NOT ask them again; proceed using these answers:\n")
	for _, q := range qs {
		b.WriteString("\nQ: " + q + "\nA: " + answers[q] + "\n")
	}
	return b.String()
}

// runStepAnswers reads a step's answered questions from the run's stored
// input; nil where there is no store (a worker runs the agent remotely and
// gets the answers through the prompt instead).
func (e *Engine) runStepAnswers(ctx context.Context, runID uuid.UUID, stepID string) map[string]string {
	if e.store == nil {
		return nil
	}
	run, err := e.store.GetRun(ctx, runID)
	if err != nil || run == nil {
		return nil
	}
	var input map[string]any
	if json.Unmarshal(run.Input, &input) != nil {
		return nil
	}
	return stepAnswers(input, stepID)
}

package engine

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Production (reqsume-prod-watch, runs 0df99050 / 200a3b91): an answered
// ask_human re-ran the step from a prompt that did not carry the answer, so the
// agent asked the identical question again — forever. The answer must reach
// the step: in its prompt, and as the tool result if it asks the same again.
func TestAnsweredQuestionIsDeliveredNotReAsked(t *testing.T) {
	def := &workflow.Definition{Name: "escalate", Steps: []workflow.Step{{
		ID: "escalate", Prompt: "ask which to file", Tools: []string{"ask_human"},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)
	q := map[string]any{"question": "Which of these should I file?"}
	llm := newFakeLLM(t,
		turn{calls: []call{{name: "ask_human", args: q}}}, // first attempt: pauses
		turn{calls: []call{{name: "ask_human", args: q}}}, // re-run: same question
		submit(map[string]any{"ok": true}),
		turn{text: "done"},
	)
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	h.wait(run.ID)
	if err := h.eng.AnswerQuestion(context.Background(), run.ID, "escalate",
		tn.Answer{Ok: true, Data: map[string]any{tn.RelayOutputKey: "none"}}, Actor{ID: "grace", Via: "api"}); err != nil {
		t.Fatal(err)
	}
	h.wait(run.ID)
	st := h.steps(run.ID)["escalate"]
	if st.Status != "done" && st.Status != "succeeded" && st.Status != "completed" {
		t.Fatalf("step %s (%s) — the answer did not reach the step", st.Status, st.Error)
	}
	llm.mu.Lock()
	raw, _ := json.Marshal(llm.requests[1]["messages"])
	llm.mu.Unlock()
	if !strings.Contains(string(raw), "none") {
		t.Fatalf("the re-run prompt does not carry the answer: %s", raw)
	}
}

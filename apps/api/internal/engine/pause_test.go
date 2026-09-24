package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/notify"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// ask_human named in `tools:` — the ADR 0021 way — grants the tool through the
// SAME per-step allowlist as every other tool (ADR 0004), with no boolean.
func TestAskHumanIsGrantedByNamingItInTools(t *testing.T) {
	def := &workflow.Definition{Name: "askviatools", Steps: []workflow.Step{{
		ID: "gather", Prompt: "gather facts", Tools: []string{"bash", "ask_human"},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)
	llm := newFakeLLM(t, submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, "")
	h.run(nil)
	if offered := llm.toolNamesOffered(0); !contains2(offered, "ask_human") {
		t.Fatalf("naming ask_human in tools must grant it: %v", offered)
	}
}

// The approval gate records WHO, WHEN and WHAT — not just status='approved'.
func TestApprovalRecordsTheActor(t *testing.T) {
	def := &workflow.Definition{Name: "whoapproved", Steps: []workflow.Step{{
		ID: "publish", Prompt: "publish", Tools: []string{"bash"}, RequiresApproval: true,
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)
	llm := newFakeLLM(t, submit(map[string]any{"ok": true}), finish())
	h := newHarness(t, def, llm, "")
	run := h.run(nil)

	if err := h.eng.Approve(context.Background(), run.ID, "publish", Actor{}); err == nil {
		t.Fatal("an approval with no actor must be refused")
	}
	if err := h.eng.Approve(context.Background(), run.ID, "publish", Actor{ID: "ada", Via: "api"}); err != nil {
		t.Fatal(err)
	}
	h.wait(run.ID)
	st := h.steps(run.ID)["publish"]
	if st.ResolvedBy != "ada (via api)" || st.Resolution != "approved" || st.ResolvedAt == nil {
		t.Fatalf("approver not recorded: by=%q resolution=%q at=%v", st.ResolvedBy, st.Resolution, st.ResolvedAt)
	}
}

// A DECLINE is not a timeout: Answer.Ok/Reason survive into the step row.
func TestDeclineIsDistinguishableFromExpiry(t *testing.T) {
	for _, tc := range []struct{ reason, runStatus string }{
		{"declined", "cancelled"},
		{"expired", "failed"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			def := &workflow.Definition{Name: "decl" + tc.reason, Steps: []workflow.Step{{
				ID: "gather", Prompt: "gather", Tools: []string{"ask_human"},
				OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
			}}}
			normalizeForTest(def)
			llm := newFakeLLM(t, turn{calls: []call{{name: "ask_human", args: map[string]any{
				"question": "may I?", "why": "because",
			}}}})
			h := newHarness(t, def, llm, "")
			run := h.run(nil)
			h.wait(run.ID)
			err := h.eng.AnswerQuestion(context.Background(), run.ID, "gather",
				tn.Answer{Ok: false, Reason: tc.reason}, Actor{ID: "grace", Via: "api"})
			if err != nil {
				t.Fatal(err)
			}
			st := h.steps(run.ID)["gather"]
			if st.Resolution != tc.reason || st.ResolvedBy != "grace (via api)" {
				t.Fatalf("resolution=%q by=%q", st.Resolution, st.ResolvedBy)
			}
			got, _ := h.store.GetRun(context.Background(), run.ID)
			if got.Status != tc.runStatus {
				t.Fatalf("run = %s, want %s", got.Status, tc.runStatus)
			}
		})
	}
}

// A pause reaches the notifier, carrying a POINTER — run, step, question and a
// URL to answer at — and nothing that resolves it.
func TestNotifierReceivesThePauseAndCarriesNoCapability(t *testing.T) {
	var mu sync.Mutex
	var got []notify.Pause
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p notify.Pause
		_ = json.NewDecoder(r.Body).Decode(&p)
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer sink.Close()

	def := &workflow.Definition{Name: "notifyme", Steps: []workflow.Step{{
		ID: "gather", Prompt: "gather", Tools: []string{"ask_human"},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)
	llm := newFakeLLM(t, turn{calls: []call{{name: "ask_human", args: map[string]any{
		"question": "Which version are you running?", "why": "the fix differs",
	}}}})
	h := newHarness(t, def, llm, "")
	h.eng.catalog.Notifiers.Add(catalog.Notifier{Name: "sink", Kind: "webhook", URL: sink.URL}, "test")

	run := h.run(nil)
	h.wait(run.ID)

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("notifier saw %d pauses, want 1", len(got))
	}
	p := got[0]
	if p.RunID != run.ID.String() || p.StepID != "gather" || p.Kind != "input" {
		t.Fatalf("pause = %+v", p)
	}
	if !strings.Contains(p.Prompt, "Which version") {
		t.Fatalf("the question did not travel: %q", p.Prompt)
	}
	if !strings.Contains(p.URL, run.ID.String()) {
		t.Fatalf("no pointer at the run: %q", p.URL)
	}
	// The payload must not carry anything that RESOLVES the pause: no token,
	// no signature, no one-click answer link. Possession of the channel is
	// never an authorization (ADR 0021).
	raw, _ := json.Marshal(p)
	for _, bad := range []string{"token", "secret", "signature", "/approve", "/answer"} {
		if strings.Contains(strings.ToLower(string(raw)), bad) {
			t.Fatalf("the notification carries a capability (%q): %s", bad, raw)
		}
	}
}

// A missing notifier is not an error: zero config must stay zero config.
func TestNoNotifierIsNotAnError(t *testing.T) {
	def := &workflow.Definition{Name: "nonotifier", Steps: []workflow.Step{{
		ID: "gather", Prompt: "gather", Tools: []string{"ask_human"},
		OutputSchema: objSchema([]any{"ok"}, map[string]any{"ok": boolProp()}),
	}}}
	normalizeForTest(def)
	llm := newFakeLLM(t, turn{calls: []call{{name: "ask_human", args: map[string]any{"question": "q?"}}}})
	h := newHarness(t, def, llm, "")
	run := h.run(nil)
	got := h.wait(run.ID)
	if got.Status != "needs_input" {
		t.Fatalf("run = %s (%s)", got.Status, got.Error)
	}
}

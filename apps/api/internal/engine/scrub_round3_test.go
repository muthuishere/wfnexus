package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/muthuishere/toolnexus/golang/agents"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A secret holding "/" and "é", as a JSON encoder writes it inside a string:
// "\/" (PHP) and "\u00e9" (python, jq --ascii-output).
const r3Secret = "ab/cd-é-0123456789"

const r3Escaped = `ab\/cd-\u00e9-0123456789`

func r3NotLeaked(t *testing.T, where, got string) {
	t.Helper()
	for _, bad := range []string{r3Escaped, r3Secret, `ab\/cd`, `\u00e9-0123456789`} {
		if i := strings.Index(got, bad); i >= 0 {
			t.Fatalf("%s: the secret survived:\n%s", where, got[max(i-200, 0):min(len(got), i+200)])
		}
	}
	if !strings.Contains(got, "[redacted:API_TOKEN]") {
		t.Fatalf("%s: nothing was redacted:\n%s", where, got[:min(len(got), 400)])
	}
}

// Escaped secrets in text that is not one JSON document: a JSON stdout over
// the event limit (scrubbed as text, then cut), NDJSON, JSON inside a log
// line, and a JSON document Go cannot parse.
func TestEscapedSecretsInTextThatIsNotOneJSONDocument(t *testing.T) {
	secrets := []secretValue{{"API_TOKEN", r3Secret}}
	big := `{"token":"` + r3Escaped + `","pad":"` + strings.Repeat("x", 5000) + `"}`
	cases := map[string]string{
		"large JSON stdout, cut": trim(redactText(big, secrets), 4000),
		"NDJSON":                 redactText(`{"n":1,"token":"`+r3Escaped+`"}`+"\n"+`{"n":2,"token":"`+r3Escaped+`"}`+"\n", secrets),
		"JSON in a log line":     redactText(`2026-11-14T10:00:00Z INFO sent payload={"auth":{"token":"`+r3Escaped+`"}} status=200`, secrets),
		"unparseable JSON":       string(scrubJSON([]byte(`{"n":1e400,"token":"`+r3Escaped+`"}`), secrets)),
		"an error text":          redactText(`request failed: {"error":"bad token `+r3Escaped+`"}`, secrets),
	}
	for name, got := range cases {
		t.Run(name, func(t *testing.T) {
			r3NotLeaked(t, name, got)
			if name == "NDJSON" && strings.Count(got, "[redacted:API_TOKEN]") != 2 {
				t.Fatalf("both lines: %s", got)
			}
		})
	}
	// A surrogate pair and the remaining escapes decode too.
	emoji := []secretValue{{"API_TOKEN", "tok-😀-\"q\"-\\-0123"}}
	got := redactText(`INFO {"t":"tok-\ud83d\ude00-\"q\"-\\-0123"} sent`, emoji)
	if !strings.Contains(got, "[redacted:API_TOKEN]") || strings.Contains(got, "0123") {
		t.Fatalf("surrogate pair: %s", got)
	}
}

// JSON nested 6 to 8 levels deep, with a secret holding " \ < > &: beyond the
// levels written back one by one, the value holding it is replaced whole.
func TestASecretInDeeplyNestedJSONIsRedacted(t *testing.T) {
	secret := `pw"<x>&\y-0123456789`
	secrets := []secretValue{{"API_TOKEN", secret}}
	for _, levels := range []int{6, 7, 8, 20} {
		doc := string(mustJSON(map[string]any{"token": secret}))
		for i := 1; i < levels; i++ {
			doc = string(mustJSON(map[string]any{"l": doc}))
		}
		out := string(scrubJSON([]byte(doc), secrets))
		// Decode every level: no level holds the secret in any spelling.
		var v any = out
		for i := 0; i < levels+2; i++ {
			s, ok := v.(string)
			if !ok {
				break
			}
			if strings.Contains(s, secret) || strings.Contains(s, jsonInner(secret, false)) || strings.Contains(s, jsonInner(secret, true)) {
				t.Fatalf("%d levels: level %d holds the secret", levels, i)
			}
			var next any
			if json.Unmarshal([]byte(s), &next) != nil {
				break
			}
			if m, ok := next.(map[string]any); ok {
				v = m["l"]
				if v == nil {
					v = m["token"]
				}
			} else {
				break
			}
		}
		if !strings.Contains(out, "[redacted:") {
			t.Fatalf("%d levels: nothing redacted", levels)
		}
	}
	// Deep JSON with no secret is left alone.
	clean := string(mustJSON(map[string]any{"l": string(mustJSON(map[string]any{"l": string(mustJSON(map[string]any{"l": string(mustJSON(map[string]any{"l": string(mustJSON(map[string]any{"l": "plain"}))}))}))}))}))
	if got := scrubJSON([]byte(clean), secrets); string(got) != clean {
		t.Fatalf("clean deep JSON changed:\n%s", got)
	}
}

// Large integers survive scrubbing byte for byte, and a document with nothing
// to redact is returned exactly as it came.
func TestScrubbingKeepsNumbersAndUnchangedBytes(t *testing.T) {
	secrets := []secretValue{{"API_TOKEN", "tok-0123456789"}}
	doc := []byte(`{"id":12345678901234567890,"f":1.50,"token":"tok-0123456789"}`)
	out := string(scrubJSON(doc, secrets))
	if !strings.Contains(out, "12345678901234567890") || strings.Contains(out, "tok-0123456789") {
		t.Fatalf("scrubJSON: %s", out)
	}
	ev := redactEvent(json.RawMessage(doc), secrets)
	if b := string(mustJSON(ev)); !strings.Contains(b, "12345678901234567890") {
		t.Fatalf("redactEvent: %s", b)
	}
	// The same inside a string that is itself JSON (a step that printed it).
	nested := mustJSON(map[string]any{"stdout": `{"id":12345678901234567890,"token":"tok-0123456789"}`})
	if out := string(scrubJSON(nested, secrets)); !strings.Contains(out, "12345678901234567890") || strings.Contains(out, "tok-0123456789") {
		t.Fatalf("nested: %s", out)
	}
	clean := []byte("{ \"id\" : 12345678901234567890,\n  \"ok\": true }")
	if got := scrubJSON(clean, secrets); !bytes.Equal(got, clean) {
		t.Fatalf("unchanged document rewritten: %q", got)
	}
}

// A bare "…" ending an ordinary sentence is not taken for a cut secret on a
// short tail; a longer tail at an explicit truncation marker still is.
func TestCutPrefixesNeedALongerTailAtABareEllipsis(t *testing.T) {
	secrets := []secretValue{{"API_TOKEN", "hunter2-0123456789"}}
	if got := redactText("we were hunt…", secrets); got != "we were hunt…" {
		t.Fatalf("short tail at a bare ellipsis redacted: %q", got)
	}
	if got := redactText("token hunter2-01…", secrets); !strings.Contains(got, "[redacted:API_TOKEN]") {
		t.Fatalf("long tail at a bare ellipsis: %q", got)
	}
	if got := redactText("token hunt\n… truncated", secrets); !strings.Contains(got, "[redacted:API_TOKEN]") {
		t.Fatalf("tail at a truncation marker: %q", got)
	}
}

// A secret too short to redact is said in the log by name, never by value.
func TestAShortSecretIsWarnedByName(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	var r runSecrets
	r.add(uuid.New(), map[string]string{"API_TOKEN": "abc12"}, map[string]bool{"API_TOKEN": true})
	if !strings.Contains(logs.String(), "secret API_TOKEN is shorter than 6 characters") || strings.Contains(logs.String(), "abc12") {
		t.Fatalf("log: %s", logs.String())
	}
}

// get hands out a copy: a concurrent add, which sorts the stored slice, cannot
// race with a reader (run with -race).
func TestRunSecretsGetIsSafeWithConcurrentAdds(t *testing.T) {
	var r runSecrets
	id := uuid.New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			r.add(id, map[string]string{"API_TOKEN_" + string(rune('A'+i%26)) + string(rune('a'+i/26)): strings.Repeat("v", 6+i)}, nil)
		}(i)
		go func() {
			defer wg.Done()
			s := r.get(id)
			for j := range s {
				_ = s[j].value
			}
			_ = redactText("x", s)
		}()
	}
	wg.Wait()
}

// A secret rotated in the middle of a run: the old and the new value are both
// scrubbed from what the run stores afterwards.
func TestARotatedSecretIsScrubbedInBothValues(t *testing.T) {
	var r runSecrets
	id := uuid.New()
	r.add(id, map[string]string{"API_TOKEN": "old-value-0123456"}, map[string]bool{"API_TOKEN": true})
	r.add(id, map[string]string{"API_TOKEN": "new-value-6543210"}, map[string]bool{"API_TOKEN": true})
	got := redactText("before old-value-0123456 after new-value-6543210", r.get(id))
	if strings.Contains(got, "old-value") || strings.Contains(got, "new-value") {
		t.Fatalf("got %q", got)
	}
}

// What a step writes to WFX_STATE outlives the run (workflow state): a secret
// in it is scrubbed before it is kept.
func TestWorkflowStateFromAStepIsScrubbed(t *testing.T) {
	name := "state-scrub-" + uuid.NewString()[:8]
	h := newHarness(t, &workflow.Definition{
		Name: name,
		Steps: []workflow.Step{{ID: "s", Env: map[string]string{"API_TOKEN": "state-secret-0123456789"},
			Run: `printf '{"seen":"%s"}' "$API_TOKEN" > "$WFX_STATE"`}},
	}, newFakeLLM(t), "")
	t.Cleanup(func() { _ = h.eng.DeleteState(context.Background(), model.StateScopeWorkflow, name, "seen") })
	if run := h.run(nil); run.Status != "done" {
		t.Fatalf("run: %s %s", run.Status, run.Error)
	}
	v, _, _ := h.eng.GetState(context.Background(), model.StateScopeWorkflow, name, "seen")
	if strings.Contains(v, "state-secret-0123456789") || !strings.Contains(v, "[redacted:API_TOKEN]") {
		t.Fatalf("state seen = %q", v)
	}
}

// Every place a run keeps text gets the escaped spellings scrubbed too: the
// step's stdout and stderr (a JSON stdout over the event limit, NDJSON, JSON in
// a log line), its summary, outcome and state, the failing step's error, the
// run's error and outcome, and every event.
func TestEscapedSecretsAreScrubbedFromEverythingARunKeeps(t *testing.T) {
	name := "esc-scrub-" + uuid.NewString()[:8]
	// The escaped spelling comes in through a variable no one registers, so
	// the command text itself (stored as the step's prompt) holds neither.
	env := map[string]string{"API_TOKEN": r3Secret, "ESCAPED_FORM": r3Escaped}
	script := `E="$ESCAPED_FORM"
printf '{"token":"%s","pad":"%s"}\n' "$E" "$(printf '%5000s' | tr ' ' x)"
printf '{"n":1,"token":"%s"}\n{"n":2,"token":"%s"}\n' "$E" "$E" >&2
printf 'INFO payload={"auth":{"token":"%s"}} status=200\n' "$E" >&2
printf 'checked {"token":"%s"}\n' "$E" >> "$WFX_STEP_SUMMARY"
printf 'ok: token %s\n' "$E" > "$WFX_OUTCOME"
printf '{"seen":"%s"}' "$E" > "$WFX_STATE"`
	h := newHarness(t, &workflow.Definition{
		Name: name,
		Steps: []workflow.Step{
			{ID: "a", Env: env, Run: script},
			{ID: "b", Env: env, Run: `printf 'boom {"token":"%s"}\n' "$ESCAPED_FORM" >&2; exit 3`},
		},
	}, newFakeLLM(t), "")
	t.Cleanup(func() { _ = h.eng.DeleteState(context.Background(), model.StateScopeWorkflow, name, "seen") })
	run := h.run(nil)
	if run.Status != "failed" {
		t.Fatalf("run: %s %s", run.Status, run.Error)
	}
	var all strings.Builder
	all.WriteString(run.Error + "\n" + run.Outcome + "\n")
	for _, st := range h.steps(run.ID) {
		all.WriteString(string(st.Output) + "\n" + st.Error + "\n" + st.RawText + "\n" + st.Prompt + "\n")
	}
	all.WriteString(eventTexts(t, h.store, run.ID))
	v, _, _ := h.eng.GetState(context.Background(), model.StateScopeWorkflow, name, "seen")
	all.WriteString(v)
	r3NotLeaked(t, "a run", all.String())
	a := output(t, h.steps(run.ID)["a"])
	for _, k := range []string{"stdout", "stderr", "summary", "outcome"} {
		if s, _ := a[k].(string); !strings.Contains(s, "[redacted:API_TOKEN]") {
			t.Errorf("%s not scrubbed: %q", k, s[:min(len(s), 200)])
		}
	}
	if !strings.Contains(h.steps(run.ID)["b"].Error, "[redacted:API_TOKEN]") || !strings.Contains(v, "[redacted:API_TOKEN]") {
		t.Fatalf("error %q, state %q", h.steps(run.ID)["b"].Error, v)
	}
}

// An agent step's raw text, prompt and error, and its artifacts, are scrubbed
// of the escaped spellings before they are stored.
func TestEscapedSecretsAreScrubbedFromAgentTextAndArtifacts(t *testing.T) {
	h := newHarness(t, &workflow.Definition{Name: "esc-agent-" + uuid.NewString()[:8],
		Steps: []workflow.Step{{ID: "s", Run: "true"}}}, newFakeLLM(t), "")
	ctx := context.Background()
	run, err := h.store.CreateRun(ctx, "local", h.currentWorkflow(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.EnsureStep(ctx, run.ID, "s", 0); err != nil {
		t.Fatal(err)
	}
	h.eng.redactions.add(run.ID, map[string]string{"API_TOKEN": r3Secret}, map[string]bool{"API_TOKEN": true})
	said := `the reply was {"token":"` + r3Escaped + `"}`
	h.eng.setStep(ctx, run.ID, "s", model.StepPatch{RawText: str(said), Prompt: str("use " + said), Error: str("failed: " + said)})
	st := h.steps(run.ID)["s"]
	for what, got := range map[string]string{"raw text": st.RawText, "prompt": st.Prompt, "error": st.Error} {
		r3NotLeaked(t, what, got)
	}
	blob := &memBlob{}
	h.eng.blob = blob
	h.eng.saveArtifacts(ctx, run.ID, "s", "", "", agents.TaskResult{Text: said})
	if len(blob.got) != 1 {
		t.Fatalf("artifacts: %d", len(blob.got))
	}
	r3NotLeaked(t, "final.txt", blob.got[0])
}

type memBlob struct{ got []string }

func (m *memBlob) Put(_ context.Context, _ string, r io.Reader, _ int64, _ string) error {
	b, err := io.ReadAll(r)
	m.got = append(m.got, string(b))
	return err
}

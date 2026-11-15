package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// r4Secret starts with a letter no marker, pad or "[redacted:…]" contains,
// so a piece of it left at a cut is visible however short it is.
const r4Secret = "Qv7Wx3Lm9Pt2Hk8Rv4Ty6Nb1"

func r4Secrets() []secretValue { return []secretValue{{name: "R4_TOKEN", value: r4Secret}} }

// pieceBefore is the longest prefix of secret that the text ends with right
// before any occurrence of marker (0 if none).
func pieceBefore(text, marker, secret string) int {
	best := 0
	for from := 0; ; {
		i := strings.Index(text[from:], marker)
		if i < 0 {
			return best
		}
		head := text[:from+i]
		for k := len(secret) - 1; k > best; k-- {
			if strings.HasSuffix(head, secret[:k]) {
				best = k
				break
			}
		}
		from += i + len(marker)
	}
}

// anyRun reports a run of n or more characters of secret anywhere in text.
func anyRun(text, secret string, n int) string {
	for i := 0; i+n <= len(secret); i++ {
		if strings.Contains(text, secret[i:i+n]) {
			return secret[i : i+n]
		}
	}
	return ""
}

// A text can hold more than one truncation marker: a worker's cut stdout and
// then the engine's own cut, or a marker the tool merely printed. Each is
// handled where it is; an earlier marker no longer hides the real cut.
func TestCutHandlesEveryMarkerWhereItIs(t *testing.T) {
	secrets := r4Secrets()
	for k := minCutPrefix; k < len(r4Secret); k++ {
		piece := r4Secret[:k]
		for _, text := range []string{
			// a printed worker marker, then the engine's cut
			"log said\n… truncated earlier\nthen " + piece + "\n… (99 more bytes)",
			// a worker's cut stdout followed by stderr, then the engine's cut
			"out " + piece + "\n… truncated" + "err " + piece + "\n… (12 more bytes)",
			// a story file cut after a printed "\n… (" marker
			"see\n… (below)\n" + piece + summaryCutMarker,
		} {
			got := redactText(text, secrets)
			if run := anyRun(got, r4Secret, minCutPrefix); run != "" {
				t.Fatalf("k=%d: %q survived in %q", k, run, got)
			}
		}
	}
}

// clip scrubs the WHOLE text before it cuts, so no piece of a secret is left
// at the cut however few characters of it fall before the cut. Cutting first
// leaves 1-3 characters that no backstop can tell from ordinary text.
func TestClipScrubsTheWholeTextBeforeItCuts(t *testing.T) {
	secrets := r4Secrets()
	pad := strings.Repeat(".", 100)
	oldKept := 0
	for off := 1; off < len(r4Secret); off++ {
		for _, lead := range []string{"", "x\n… truncated y "} {
			text := lead + pad + r4Secret + " tail"
			n := len(lead) + len(pad) + off
			got := clip(text, n, secrets, moreBytes)
			if k := pieceBefore(got, "\n… (", r4Secret); k > 0 {
				t.Fatalf("off=%d: %d characters of the secret kept at the cut: %q", off, k, got)
			}
			if run := anyRun(got, r4Secret, minCutPrefix); run != "" {
				t.Fatalf("off=%d: %q survived: %q", off, run, got)
			}
			// The cut-first order, for comparison: it keeps a piece.
			if old := redactText(trim(text, n), secrets); pieceBefore(old, "\n… (", r4Secret) > 0 {
				oldKept++
			}
		}
	}
	if oldKept == 0 {
		t.Fatal("the cut-first order kept nothing; this test cannot tell the orders apart")
	}
	if got := clip("short "+r4Secret, 1000, secrets, moreBytes); got != "short [redacted:R4_TOKEN]" {
		t.Fatalf("an uncut text is still scrubbed: %q", got)
	}
	// A piece of a value printed apart from the rest (no whole form to
	// match), cut right after it: removed at the cut.
	got := clip(pad+r4Secret[:8]+" and the rest later", len(pad)+8, secrets, moreBytes)
	if strings.Contains(got, r4Secret[:8]) {
		t.Fatalf("a piece at the cut survived: %q", got)
	}
}

// sinkEngine is a worker engine (no store) whose events are collected after
// emit has scrubbed them.
func sinkEngine(t *testing.T) (*Engine, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var events []map[string]any
	e := NewWorkerEngine(func(kind string, payload any) {
		raw, _ := json.Marshal(payload)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		mu.Lock()
		events = append(events, m)
		mu.Unlock()
	})
	e.cfg.WorkDir = t.TempDir()
	return e, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), events...)
	}
}

func eventsOfKind(events []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, ev := range events {
		if ev["kind"] == kind {
			p, _ := ev["payload"].(map[string]any)
			out = append(out, p)
		}
	}
	return out
}

// A `run:` step's stdout, stderr and tool result are scrubbed whole, then
// cut (nodes.go): a secret straddling the 4000-byte cut leaves nothing.
func TestARunStepsOutputIsScrubbedBeforeItIsCut(t *testing.T) {
	for _, off := range []int{1, 2, 3, 5, 12, len(r4Secret) - 1} {
		e, events := sinkEngine(t)
		runID := uuid.New()
		pad := strings.Repeat(".", maxEventOutput-off)
		step := &workflow.Step{ID: "s", Run: `printf '%s%s tail' "$PAD" "$R4_TOKEN"; printf '%s%s tail' "$PAD" "$R4_TOKEN" >&2`,
			Env: map[string]string{"R4_TOKEN": r4Secret, "PAD": pad}}
		out, err := e.runCommand(context.Background(), runID, step, workflow.TemplateData{})
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"stdout", "stderr"} {
			s, _ := out[k].(string)
			if n := pieceBefore(s, "\n… (", r4Secret); n > 0 || anyRun(s, r4Secret, 4) != "" {
				t.Fatalf("off=%d %s keeps %d characters: %q", off, k, n, s[len(s)-80:])
			}
		}
		res := eventsOfKind(events(), "tool_result")
		if len(res) != 1 {
			t.Fatalf("tool results: %v", res)
		}
		s, _ := res[0]["output"].(string)
		if n := pieceBefore(s, "\n… (", r4Secret); n > 0 {
			t.Fatalf("off=%d tool result keeps %d characters: %q", off, n, s[len(s)-80:])
		}
	}
}

// executeNode scrubs a node's output as a JSON tree, which reaches a value
// nested deeper than the text pass decodes (maxDecodeDepth): replaced whole.
func TestExecuteNodeScrubsWhatTheTextPassCannotReach(t *testing.T) {
	const secret = `Qv7"Wx3Lm9Pt2Hk8` // a quote: every JSON level escapes it again
	var v any = map[string]any{"token": secret}
	for i := 0; i < 6; i++ {
		v = map[string]any{"inner": string(mustJSON(v))}
	}
	doc := string(mustJSON(v))
	if got := redactText(doc, []secretValue{{"R4_TOKEN", secret}}); !strings.Contains(got, "Wx3Lm9Pt2Hk8") {
		t.Fatal("the text pass reaches this depth; the test no longer isolates the tree pass")
	}
	e, _ := sinkEngine(t)
	step := &workflow.Step{ID: "s", Run: `printf '%s' "$DOC"`, Env: map[string]string{"R4_TOKEN": secret, "DOC": doc}}
	out, err := e.executeNode(context.Background(), uuid.New(), step, workflow.TemplateData{})
	if err != nil {
		t.Fatal(err)
	}
	if s := fmt.Sprint(out["stdout"]); strings.Contains(s, "Wx3Lm9Pt2Hk8") || !strings.Contains(s, deepMarker) {
		t.Fatalf("stdout = %s", s)
	}
}

// An agent's tool result is scrubbed of the run's secrets, then of provider
// identifiers, then cut (step.go, clipScrubbed).
func TestAnAgentsToolResultIsScrubbedBeforeItIsCut(t *testing.T) {
	for _, off := range []int{1, 2, 3, 9} {
		e, events := sinkEngine(t)
		runID := uuid.New()
		e.redactions.add(runID, map[string]string{"R4_TOKEN": r4Secret}, nil)
		h := e.hooks(context.Background(), runID, "s", t.TempDir(), 10, nil, "")
		_, _ = h.AfterTool(context.Background(), tn.AfterToolEvent{Name: "bash", Result: tn.ToolResult{
			Output: strings.Repeat(".", maxEventOutput-off) + r4Secret + " tail"}})
		res := eventsOfKind(events(), "tool_result")
		s, _ := res[0]["output"].(string)
		if n := pieceBefore(s, "\n… (", r4Secret); n > 0 {
			t.Fatalf("off=%d: %d characters kept: %q", off, n, s[len(s)-60:])
		}
	}
	// A secret an identifier pattern would take half of: the run's secrets
	// go first, so nothing of it is left.
	const shaped = "zz9Kq7Wv_user_id=Wx3Lm9Pt"
	e, events := sinkEngine(t)
	runID := uuid.New()
	e.redactions.add(runID, map[string]string{"R4_TOKEN": shaped}, nil)
	h := e.hooks(context.Background(), runID, "s", t.TempDir(), 10, nil, "")
	_, _ = h.AfterTool(context.Background(), tn.AfterToolEvent{Name: "bash", Result: tn.ToolResult{Output: "got " + shaped}})
	if s := fmt.Sprint(eventsOfKind(events(), "tool_result")[0]["output"]); strings.Contains(s, "zz9Kq7") {
		t.Fatalf("part of the secret kept: %q", s)
	}
}

// A story file is scrubbed whole, then cut (readCapped).
func TestAStoryFileIsScrubbedBeforeItIsCut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	for _, off := range []int{1, 2, 3, 7} {
		n := 200
		_ = os.WriteFile(path, []byte(strings.Repeat(".", n-off)+r4Secret+" tail"), 0o600)
		got := readCapped(path, n, r4Secrets())
		if k := pieceBefore(got, summaryCutMarker, r4Secret); k > 0 || !strings.HasSuffix(got, summaryCutMarker) {
			t.Fatalf("off=%d: %d characters kept: %q", off, k, got[len(got)-60:])
		}
	}
}

// The failing command's stderr line in the step error is scrubbed whole, then
// cut (stderrTail).
func TestTheFailingCommandsStderrLineIsScrubbedBeforeItIsCut(t *testing.T) {
	for _, off := range []int{1, 2, 3, 10} {
		got := stderrTail(map[string]any{"stderr": "first line\n" + strings.Repeat(".", 300-off) + r4Secret + " tail"}, r4Secrets())
		if k := pieceBefore(got, "…", r4Secret); k > 0 || anyRun(got, r4Secret, 4) != "" {
			t.Fatalf("off=%d: %d characters kept: %q", off, k, got)
		}
	}
}

// A worker's 64 KB cap falls inside a secret: the server finds the piece
// before the worker's marker by position.
func TestAWorkersCutSecretIsRemovedAtItsMarker(t *testing.T) {
	e := &Engine{}
	payload, _ := json.Marshal(JobPayload{Env: map[string]string{"R4_TOKEN": r4Secret}})
	for k := minCutPrefix; k < len(r4Secret); k++ {
		res := e.ScrubJobResult(context.Background(), uuid.New(), payload, JobResult{
			Stdout: "printed\n… (a quoted marker)\n" + r4Secret[:k] + "\n… truncated",
		})
		if run := anyRun(res.Stdout, r4Secret, minCutPrefix); run != "" {
			t.Fatalf("k=%d: %q survived: %q", k, run, res.Stdout)
		}
	}
}

// Base64 of a value inside a longer run: for each length mod 3, each of the
// three byte offsets, and bytes after it or not, every character that encodes
// the value's bytes alone is gone, in both alphabets, padded or not.
func TestBase64IsCaughtAtEveryOffsetWithBytesAround(t *testing.T) {
	for _, val := range []string{"Zq7Wx3", "Zq7Wx3L", "Zq7Wx3Lm", "Zq7Wx3Lm9Pt2", "Zq7Wx3Lm9Pt2H", "Zq7Wx3Lm9Pt2Hk"} {
		secrets := []secretValue{{"B64", val}}
		for p := 0; p <= 2; p++ {
			for _, lead := range []string{"\x00\x00", "\xff\xfe", "us"} {
				for _, trail := range []string{"", "\x00", "@x", ":\xffz", "more bytes"} {
					for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
						full := enc.EncodeToString([]byte(lead[:p] + val + trail))
						from, to := (8*p+5)/6, 8*(p+len(val))/6
						core := full[from:to]
						got := redactText("Authorization: Basic "+full+" end", secrets)
						if strings.Contains(got, core) || !strings.Contains(got, "[redacted:B64]") {
							t.Fatalf("len %d (mod 3 = %d), offset %d, %d bytes after: %q not caught in %q",
								len(val), len(val)%3, p, len(trail), core, got)
						}
					}
				}
			}
		}
	}
	// The Basic-auth header curl -u "$KEY:" sends: the value at offset 0, a
	// colon after it.
	val := "Zq7Wx3Lm9Pt2H" // len%3 == 1
	hdr := base64.StdEncoding.EncodeToString([]byte(val + ":"))
	if got := redactText(hdr, []secretValue{{"K", val}}); !strings.Contains(got, "[redacted:K]") {
		t.Fatalf("curl -u KEY: not caught: %q", got)
	}
}

var reUpperPct = regexp.MustCompile(`%[0-9A-F]{2}`)

// Percent-encoding in any case, the encodeURIComponent spelling and the
// fully percent-encoded form.
func TestPercentEncodingIsCaughtInAnyCase(t *testing.T) {
	v := "s3cr/t+v=lue&x?é 9"
	secrets := []secretValue{{"K", v}}
	lower := func(s string) string { return reUpperPct.ReplaceAllStringFunc(s, strings.ToLower) }
	full := func(s string, f string) string {
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			fmt.Fprintf(&b, f, s[i])
		}
		return b.String()
	}
	mixed := func(s string) string {
		i := 0
		return reUpperPct.ReplaceAllStringFunc(s, func(m string) string {
			i++
			if i%2 == 0 {
				return strings.ToLower(m)
			}
			return m
		})
	}
	component := encodeURIComponent(v)
	for name, f := range map[string]string{
		"query lower":         lower(url.QueryEscape(v)),
		"path lower":          lower(url.PathEscape(v)),
		"query mixed":         mixed(url.QueryEscape(v)),
		"encodeURIComponent":  component,
		"component lower":     lower(component),
		"fully encoded upper": full(v, "%%%02X"),
		"fully encoded lower": full(v, "%%%02x"),
	} {
		got := redactText("GET /cb?token="+f+"&next=1 HTTP/1.1", secrets)
		if strings.Contains(got, "lue") || !strings.Contains(got, "?token=[redacted:K]&next=1") {
			t.Errorf("%s (%s) not caught: %q", name, f, got)
		}
	}
	// Ordinary percent text is left alone.
	if got := redactText("100%25 done, 50%ok", secrets); got != "100%25 done, 50%ok" {
		t.Fatalf("changed: %q", got)
	}
}

// encodeURIComponent is JavaScript's: everything but A-Z a-z 0-9 - _ . ! ~ * ' ( ).
func encodeURIComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// A secret escaped twice (JSON inside a JSON string, in NDJSON or a log
// line), and JSON-escaped percent-encoding, are found in text.
func TestDoubleEscapedSecretsInNDJSONAndLogText(t *testing.T) {
	v := `pa"ss/wé\x-0123456789`
	secrets := []secretValue{{"K", v}}
	once := jsonInner(v, false)
	twice := jsonInner(once, false)
	thrice := jsonInner(twice, false)
	ascii := strings.ReplaceAll(twice, "é", `\\u00e9`) // python-style \u escape, escaped again
	for name, text := range map[string]string{
		"ndjson twice":   `{"n":1,"body":"{\"msg\":\"` + once + `\"}"}` + "\n" + `{"n":2,"body":"` + twice + `"}`,
		"log twice":      `INFO payload="` + twice + `" status=200`,
		"log thrice":     `DEBUG raw=` + thrice,
		"ascii twice":    `{"x":"` + ascii + `"}` + " trailing text",
		"escaped %-form": `log {"url":"` + jsonInner("https://h/?k="+reUpperPct.ReplaceAllStringFunc(url.QueryEscape(v), strings.ToLower), false) + `"}`,
	} {
		got := redactText(text, secrets)
		if strings.Contains(got, "0123456789") || !strings.Contains(got, "[redacted:K]") {
			t.Errorf("%s not caught: %q", name, got)
		}
	}
}

// A secret made of digits, printed as a JSON number, is redacted; other
// numbers keep their digits.
func TestASecretPrintedAsAJSONNumberIsScrubbed(t *testing.T) {
	secrets := []secretValue{{"PIN", "12345678901234"}}
	got := string(scrubJSON([]byte(`{"pin":12345678901234,"n":1000000,"big":987654321098765432109}`), secrets))
	if strings.Contains(got, "12345678901234,") || !strings.Contains(got, `"pin":"[redacted:PIN]"`) ||
		!strings.Contains(got, `"n":1000000`) || !strings.Contains(got, `"big":987654321098765432109`) {
		t.Fatalf("got %s", got)
	}
	ev, _ := json.Marshal(redactEvent(map[string]any{"pin": json.Number("12345678901234")}, secrets))
	if strings.Contains(string(ev), "12345678901234") {
		t.Fatalf("event %s", ev)
	}
}

// A scrubbed output keeps integers as written (json.Number): 1000000 reads
// as 1000000 to a template, never 1e+06. An output with nothing to redact is
// returned as it was.
func TestScrubbedOutputKeepsIntegers(t *testing.T) {
	secrets := r4Secrets()
	out := scrubOutput(map[string]any{"n": float64(1000000), "exitCode": 3, "confidence": 0.9, "s": "x " + r4Secret}, secrets)
	if fmt.Sprint(out["n"]) != "1000000" {
		t.Fatalf("n = %v", out["n"])
	}
	got, err := workflow.Render("{{ .Output.n }}", workflow.TemplateData{Output: out})
	if err != nil || got != "1000000" {
		t.Fatalf("rendered %q %v", got, err)
	}
	if outExitCode(out) != 3 {
		t.Fatalf("exit code = %v", out["exitCode"])
	}
	if c, ok := confidenceOf(ensureConfidence(out)); !ok || c != 0.9 {
		t.Fatalf("confidence = %v", out["confidence"])
	}
	clean := map[string]any{"n": float64(1000000)}
	if same := scrubOutput(clean, secrets); fmt.Sprintf("%p", same) != fmt.Sprintf("%p", clean) {
		t.Fatal("an output with nothing to redact was rebuilt")
	}
}

// sealedNames hands out a copy: a caller ranging over it while add writes the
// stored map would race.
func TestSealedNamesIsACopy(t *testing.T) {
	var r runSecrets
	id := uuid.New()
	r.add(id, map[string]string{"A_KEY": "aaaaaaaa"}, map[string]bool{"A_KEY": true})
	got := r.sealedNames(id)
	got["OTHER"] = true
	if r.sealedNames(id)["OTHER"] {
		t.Fatal("the stored map was handed out")
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			r.add(id, map[string]string{fmt.Sprintf("K%d_KEY", i): "bbbbbbbb"}, map[string]bool{fmt.Sprintf("K%d_KEY", i): true})
		}(i)
		go func() {
			defer wg.Done()
			for range r.sealedNames(id) {
			}
		}()
	}
	wg.Wait()
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// A run's secrets stay for the grace period after it ends, so a late write is
// still scrubbed, and are gone after it.
func TestAnEndedRunsSecretsLeaveAfterTheGracePeriod(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	r := runSecrets{now: clk.now}
	id := uuid.New()
	r.add(id, map[string]string{"R4_TOKEN": r4Secret}, nil)
	r.markEnded(id)
	clk.add(endedSecretsGrace - time.Second)
	// A second end (a cancel after done) does not extend the period.
	r.markEnded(id)
	r.sweep()
	if got := redactText("late "+r4Secret, r.get(id)); strings.Contains(got, r4Secret) {
		t.Fatalf("a late write inside the grace period was not scrubbed: %q", got)
	}
	clk.add(2 * time.Second)
	r.sweep()
	if len(r.get(id)) != 0 || r.held() != 0 || len(r.sealedNames(id)) != 0 {
		t.Fatalf("still held after the grace period: %d", r.held())
	}
}

// However many runs end, the ended sets held are capped (oldest first); a
// run that has not ended, or runs again, is never dropped.
func TestRetainedSecretsStayBoundedOverManyRuns(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	r := runSecrets{now: clk.now}
	live := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for i, id := range live {
		r.add(id, map[string]string{"LIVE_TOKEN": fmt.Sprintf("live-value-%04d", i)}, nil)
	}
	again := uuid.New()
	r.add(again, map[string]string{"AGAIN_TOKEN": "again-value-0001"}, nil)
	r.markEnded(again)
	r.markRunning(again) // resumed: not ended any more
	var last uuid.UUID
	for i := 0; i < 2000; i++ {
		last = uuid.New()
		r.add(last, map[string]string{"RUN_TOKEN": fmt.Sprintf("run-value-%06d", i)}, nil)
		r.markEnded(last)
		clk.add(time.Millisecond)
	}
	if n := r.held(); n > maxEndedSecrets+len(live)+1 {
		t.Fatalf("%d sets held after 2000 ended runs", n)
	}
	for _, id := range append(live, again, last) {
		if len(r.get(id)) == 0 {
			t.Fatalf("a running run, or the newest ended one, was dropped: %v (live %v again %v last %v)", id, live, again, last)
		}
	}
	clk.add(endedSecretsGrace)
	r.sweep()
	if n := r.held(); n != len(live)+1 {
		t.Fatalf("%d sets held after the grace period, want only the %d running ones", n, len(live)+1)
	}
}

// emit scrubs every event before any destination.
func TestEmitScrubsEveryEvent(t *testing.T) {
	e, events := sinkEngine(t)
	runID := uuid.New()
	e.redactions.add(runID, map[string]string{"R4_TOKEN": r4Secret}, nil)
	e.emit(context.Background(), runID, "s", "log", map[string]any{"text": "saw " + r4Secret})
	if s := fmt.Sprint(events()); strings.Contains(s, r4Secret) || !strings.Contains(s, "[redacted:R4_TOKEN]") {
		t.Fatalf("event: %s", s)
	}
}

// After a restart nothing is registered: a worker's live events are scrubbed
// with the job's own env, registered again before they are stored.
func TestWorkerEventsAfterARestartAreScrubbed(t *testing.T) {
	e, events := sinkEngine(t) // a fresh engine: nothing registered
	payload, _ := json.Marshal(JobPayload{Env: map[string]string{"R4_TOKEN": r4Secret}})
	e.IngestWorkerEvents(context.Background(), uuid.New(), payload, []WorkerEvent{
		{StepID: "s", Kind: "tool_result", Payload: map[string]any{"output": "env: R4_TOKEN=" + r4Secret}},
	})
	if s := fmt.Sprint(events()); strings.Contains(s, r4Secret) || !strings.Contains(s, "[redacted:R4_TOKEN]") {
		t.Fatalf("events: %s", s)
	}
}

// A file attachment holding a secret is sent as a scrubbed copy; the
// workspace file is not changed; a binary file holding one is refused.
func TestASendAttachmentIsScrubbed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	_ = os.WriteFile(path, []byte("token "+r4Secret+"\n"), 0o600)
	clean, done, err := scrubAttachment(path, r4Secrets())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(clean)
	if clean == path || strings.Contains(string(b), r4Secret) || filepath.Base(clean) != "report.txt" {
		t.Fatalf("sent %s: %q", clean, b)
	}
	done()
	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Fatal("the scrubbed copy was not removed")
	}
	if orig, _ := os.ReadFile(path); !strings.Contains(string(orig), r4Secret) {
		t.Fatal("the workspace file was changed")
	}
	plain := filepath.Join(dir, "plain.txt")
	_ = os.WriteFile(plain, []byte("nothing here"), 0o600)
	if got, _, _ := scrubAttachment(plain, r4Secrets()); got != plain {
		t.Fatal("a clean file was copied")
	}
	bin := filepath.Join(dir, "img.png")
	_ = os.WriteFile(bin, append([]byte{0x89, 'P', 'N', 'G', 0xff, 0xfe}, []byte(r4Secret)...), 0o600)
	if _, _, err := scrubAttachment(bin, r4Secrets()); err == nil {
		t.Fatal("a binary file holding a secret was let through")
	}
}

// The provider-fallback log line and a stopped agent's error quote a reply
// and cut it: scrubbed whole first, the run's secrets before the identifier
// patterns.
func TestQuotedRepliesAreScrubbedBeforeTheyAreCut(t *testing.T) {
	const shaped = "zz9Kq7Wv_user_id=Wx3Lm9Pt"
	for _, off := range []int{1, 2, 3, 8} {
		e, _ := sinkEngine(t)
		runID := uuid.New()
		e.redactions.add(runID, map[string]string{"R4_TOKEN": r4Secret, "SHAPED_TOKEN": shaped}, nil)
		line := e.fallbackLine(runID, "dead", "mock", strings.Repeat(".", 300-off)+r4Secret+" tail", nil)
		if k := pieceBefore(line, "\n… (", r4Secret); k > 0 {
			t.Fatalf("fallback off=%d: %d characters kept: %q", off, k, line)
		}
		stopped := e.stoppedError(runID, "a", "max_turns", strings.Repeat(".", 500-off)+r4Secret+" tail").Error()
		if k := pieceBefore(stopped, "\n… (", r4Secret); k > 0 {
			t.Fatalf("stopped off=%d: %d characters kept: %q", off, k, stopped)
		}
		for _, got := range []string{
			e.fallbackLine(runID, "dead", "mock", "LLM 400: "+shaped, nil),
			e.fallbackLine(runID, "dead", "mock", "", errors.New("LLM 400: "+shaped)),
			e.stoppedError(runID, "a", "max_turns", "said "+shaped).Error(),
		} {
			if strings.Contains(got, "zz9Kq7") {
				t.Fatalf("part of the secret kept: %q", got)
			}
		}
	}
}

// A worker's diff travels as base64 in JSON, where no form of a value can be
// found: it is scrubbed as text first.
func TestAWorkersDiffIsScrubbed(t *testing.T) {
	e := &Engine{}
	payload, _ := json.Marshal(JobPayload{Env: map[string]string{"R4_TOKEN": r4Secret}})
	res := e.ScrubJobResult(context.Background(), uuid.New(), payload, JobResult{OK: true,
		Agent: &AgentOutcome{Diff: []byte("+token = \"" + r4Secret + "\"\n")}})
	if res.Agent == nil || strings.Contains(string(res.Agent.Diff), r4Secret) || !strings.Contains(string(res.Agent.Diff), "[redacted:R4_TOKEN]") {
		t.Fatalf("diff %q", res.Agent.Diff)
	}
}

// A quoted error is scrubbed whole and never cut, even when the marker is
// longer than the value it replaces.
func TestAQuotedErrorIsNotCutByItsRedaction(t *testing.T) {
	e, _ := sinkEngine(t)
	runID := uuid.New()
	e.redactions.add(runID, map[string]string{"DB_PASSWORD": "pw-12345"}, nil)
	line := e.fallbackLine(runID, "dead", "mock", "", errors.New("dial: auth pw-12345 refused by host"))
	if strings.Contains(line, "pw-12345") || strings.Contains(line, "more bytes") || !strings.HasSuffix(line, "refused by host) — falling back to mock") {
		t.Fatalf("line = %q", line)
	}
}

// scrubResult scrubs both what an agent step submitted and the question it
// paused on (every field: the kind is sent to notifiers as it is).
func TestScrubResultScrubsOutputAndQuestion(t *testing.T) {
	e, _ := sinkEngine(t)
	runID := uuid.New()
	e.redactions.add(runID, map[string]string{"R4_TOKEN": r4Secret}, nil)
	res := stepResult{Output: map[string]any{"finding": "saw " + r4Secret},
		Pending: &tn.Request{ID: "q", Kind: "input " + r4Secret, Prompt: "is " + r4Secret + " right?"}}
	e.scrubResult(runID, &res)
	if s := fmt.Sprint(res.Output, *res.Pending); strings.Contains(s, r4Secret) {
		t.Fatalf("kept: %s", s)
	}
}

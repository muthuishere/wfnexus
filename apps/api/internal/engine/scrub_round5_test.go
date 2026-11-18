package engine

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// r5Secret has bytes every URL form escapes differently.
const r5Secret = "Qv7/Wx3+Lm9=Pt2&Hk8 Rv4~Ty6?Nb1"

func r5PctEvery(s string, upper bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if upper {
			fmt.Fprintf(&b, "%%%02X", s[i])
		} else {
			fmt.Fprintf(&b, "%%%02x", s[i])
		}
	}
	return b.String()
}

// r5LowerHex lowercases the hex digits of every %XX.
func r5LowerHex(s string) string {
	b := []byte(s)
	for i := 0; i+2 < len(b); i++ {
		if b[i] == '%' {
			b[i+1], b[i+2] = []byte(strings.ToLower(string(b[i+1 : i+3])))[0], []byte(strings.ToLower(string(b[i+1 : i+3])))[1]
		}
	}
	return string(b)
}

// r5EncodeURIComponent is JavaScript's encodeURIComponent: everything but
// A-Z a-z 0-9 - _ . ! ~ * ' ( ) is %XX, a space too.
func r5EncodeURIComponent(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func r5Forms() map[string]string {
	b := []byte(r5Secret)
	return map[string]string{
		"raw":                   r5Secret,
		"base64":                base64.StdEncoding.EncodeToString(b),
		"base64 url":            base64.RawURLEncoding.EncodeToString(b),
		"hex":                   hex.EncodeToString(b),
		"HEX":                   strings.ToUpper(hex.EncodeToString(b)),
		"query":                 url.QueryEscape(r5Secret),
		"query lowercase":       r5LowerHex(url.QueryEscape(r5Secret)),
		"path":                  url.PathEscape(r5Secret),
		"path lowercase":        r5LowerHex(url.PathEscape(r5Secret)),
		"encodeURIComponent":    r5EncodeURIComponent(r5Secret),
		"encodeURIComponent lc": r5LowerHex(r5EncodeURIComponent(r5Secret)),
		"every byte %xx":        r5PctEvery(r5Secret, false),
		"every byte %XX":        r5PctEvery(r5Secret, true),
		"json \\/":              strings.ReplaceAll(r5Secret, "/", `\/`),
		"json \\u":              strings.Trim(string(mustJSON(r5Secret)), `"`),
		"json \\u00XX every":    jsonEveryByte(r5Secret),
	}
}

func jsonEveryByte(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		fmt.Fprintf(&b, `\u%04x`, s[i])
	}
	return b.String()
}

// r5Kept is the longest piece (4 bytes or more) of form that got still holds.
func r5Kept(got, form string) string {
	for n := len(form); n >= minCutPrefix; n-- {
		for i := 0; i+n <= len(form); i++ {
			if strings.Contains(got, form[i:i+n]) {
				return form[i : i+n]
			}
		}
	}
	return ""
}

// A worker cuts a long stdout at 64 KB and appends "\n… truncated" knowing no
// secrets; the platform scrubs what arrives. Wherever that cut falls inside
// a value, in every form the redactor knows, no piece of 4 bytes or more of
// the form, as written, is kept, once the cut has left at least one whole
// byte of the value (an escape cut before its first byte, `\u00`, carries
// none of it).
func TestAWorkerCutLeavesNoPieceOfAnyForm(t *testing.T) {
	secrets := []secretValue{{"R5_TOKEN", r5Secret}}
	for name, form := range r5Forms() {
		unit := 1 // bytes of form that carry one byte of the value
		switch {
		case strings.HasPrefix(form, `\u`):
			unit = 6
		case strings.HasPrefix(form, "%"):
			unit = 3
		}
		for k := unit; k <= len(form); k++ {
			for _, lead := range []string{"x ", "url=https://h.example/?k=", `{"a":"`} {
				full := strings.Repeat(".", 100) + lead + form + " and more after it"
				cut := len(full) - len(form) - len(" and more after it") + k
				got := redactText(full[:cut]+"\n… truncated", secrets)
				if kept := r5Kept(got, form); kept != "" {
					t.Fatalf("%s cut after %d of %d bytes keeps %q: %q", name, k, len(form), kept, got)
				}
			}
		}
	}
}

// Ordinary text at a cut is left exactly as written.
func TestAWorkerCutLeavesOrdinaryTextAlone(t *testing.T) {
	secrets := []secretValue{{"R5_TOKEN", r5Secret}}
	for _, text := range []string{"50%", "a%2", `C:\`, `path\u00`, "Qv7", "%41%42%4", "say Zz9\\", "100% done\\n"} {
		in := "x " + text + "\n… truncated"
		if got := redactText(in, secrets); got != in {
			t.Errorf("%q became %q", in, got)
		}
	}
}

// ScrubJobResult reads a worker's report with numbers kept as written: an
// agent's output is map[string]any, where a plain decode turns a 20-digit id
// into a float and changes it.
func TestAWorkersAgentOutputKeepsItsIntegers(t *testing.T) {
	e := &Engine{}
	payload, _ := json.Marshal(JobPayload{Env: map[string]string{"R5_TOKEN": r5Secret}})
	res := e.ScrubJobResult(context.Background(), uuid.New(), payload, JobResult{OK: true,
		Agent: &AgentOutcome{Output: map[string]any{"n": json.Number("1000000"), "id": json.Number("12345678901234567890"), "s": "k " + r5Secret}}})
	b := string(mustJSON(res.Agent.Output))
	if !strings.Contains(b, `"n":1000000`) || !strings.Contains(b, `"id":12345678901234567890`) ||
		strings.Contains(b, r5Secret) || !strings.Contains(b, "[redacted:R5_TOKEN]") {
		t.Fatalf("agent output: %s", b)
	}
}

// A value inside a longer base64 run, at any byte offset, in the URL alphabet
// (- and _ where the standard one has + and /): the value's own bytes give
// different characters there.
func TestAValueInsideURLAlphabetBase64IsFound(t *testing.T) {
	const sec = "Zq7?Wx3>Lm9?Pt2>Hk8" // bytes whose base64 uses + and /
	for lead := 0; lead <= 2; lead++ {
		for _, enc := range []*base64.Encoding{base64.URLEncoding, base64.RawURLEncoding} {
			E := enc.EncodeToString(append(make([]byte, lead), []byte(sec+"!!")...))
			if !strings.ContainsAny(E, "-_") {
				t.Fatalf("lead %d: %q has no URL-alphabet character; the test shows nothing", lead, E)
			}
			if got := redactText("tok="+E, []secretValue{{"B", sec}}); !strings.Contains(got, "[redacted:B]") {
				t.Errorf("lead %d: %q", lead, got)
			}
		}
	}
}

// A step parks the run while a sibling of its wave is still running: the
// sibling's writes come later than the park. A run with a step in flight is
// never dropped, by the grace period or by the cap, and its grace period
// starts again when that step finishes.
func TestAStepInFlightKeepsItsRunsSecrets(t *testing.T) {
	clk := &fakeClock{t: time.Now()}
	r := runSecrets{now: clk.now}
	id := uuid.New()
	r.add(id, map[string]string{"R5_TOKEN": r5Secret}, nil)
	r.stepStarted(id)
	r.markEnded(id) // a sibling parked the run
	clk.add(endedSecretsGrace + time.Second)
	for i := 0; i < maxEndedSecrets+10; i++ { // and many runs ended since
		other := uuid.New()
		r.add(other, map[string]string{"O_TOKEN": fmt.Sprintf("other-value-%06d", i)}, nil)
		r.markEnded(other)
	}
	r.sweep()
	if len(r.get(id)) == 0 {
		t.Fatal("dropped while a step of the run was in flight")
	}
	r.stepFinished(id)
	clk.add(endedSecretsGrace - time.Second)
	r.sweep()
	if len(r.get(id)) == 0 {
		t.Fatal("dropped before a grace period passed since its last step finished")
	}
	clk.add(2 * time.Second)
	r.sweep()
	if len(r.get(id)) != 0 {
		t.Fatal("still held a grace period after its last step finished")
	}
}

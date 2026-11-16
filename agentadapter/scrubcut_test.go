package agentadapter

// Every place the adapter shortens a text for an error scrubs the WHOLE text
// first and cuts after. A cut made first can split a sensitive value, and the
// piece left matches no whole value any more, so no later scrub finds it.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// cutSecret is long on purpose: a scrub that replaces it shrinks a text a lot,
// which is the case where a piece at the very start of a kept tail could come
// back into view.
var cutSecret = "sk-live-" + strings.Repeat("7f3Qz9XyW2pL5mN8vR4t", 10)

func scrubCutSecret(s string) string { return strings.ReplaceAll(s, cutSecret, "[redacted]") }

// shownPiece reports a piece of cutSecret (4 bytes or more) found in s.
func shownPiece(s string) string {
	for n := len(cutSecret); n >= 4; n-- {
		for i := 0; i+n <= len(cutSecret); i++ {
			if strings.Contains(s, cutSecret[i:i+n]) {
				return cutSecret[i : i+n]
			}
		}
	}
	return ""
}

func TestShowTailScrubsBeforeItCuts(t *testing.T) {
	// The value straddles where a 4 KB cut of the raw text would fall.
	raw := cutSecret + strings.Repeat("#", tailShow-50)
	got := showTail(raw, false, scrubCutSecret)
	if p := shownPiece(got); p != "" {
		t.Fatalf("the tail shows a piece of the value: %q", p)
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("the scrubbed value is not in the tail: %q", got[:40])
	}
}

func TestShowTailDropsAPieceTheBufferSplit(t *testing.T) {
	// The buffer dropped its start mid-value, and every later byte is the value
	// whole, so the scrub shrinks the text to under tailShow: the piece at the
	// front must still not be shown.
	unit := cutSecret + "#"
	body := strings.Repeat(unit, tailKeep/len(unit))
	var tb tailBuffer
	_, _ = tb.Write([]byte(strings.Repeat(".", 1000) + cutSecret))
	_, _ = tb.Write([]byte(body))
	start := 1000 + len(cutSecret) + len(body) - tailKeep // where the kept bytes begin
	if !tb.cut || start <= 1000 || start >= 1000+len(cutSecret) || !strings.HasPrefix(string(tb.b), cutSecret[start-1000:]+cutSecret) {
		t.Fatalf("test setup: the buffer does not start mid-value: %q", tb.b[:20])
	}
	if n := len(scrubCutSecret(string(tb.b))); n >= tailShow {
		t.Fatalf("test setup: the scrub left %d bytes, not under %d", n, tailShow)
	}
	got := tb.text(scrubCutSecret)
	if p := shownPiece(got); p != "" {
		t.Fatalf("the tail shows the piece the buffer split: %q", p)
	}
}

func TestShowTailCutsOnARuneBoundary(t *testing.T) {
	// "€" is 3 bytes: the plain cut point (4096 from the end) falls inside one.
	got := showTail(strings.Repeat("€", tailShow), false, nil)
	if !utf8.ValidString(got) {
		t.Fatal("the cut split a rune")
	}
	if len(got) > tailShow+len("… ") {
		t.Fatalf("tail is %d bytes", len(got))
	}
}

func TestClipTitleScrubsBeforeItCuts(t *testing.T) {
	got := clipTitle(strings.Repeat(".", 60)+cutSecret, scrubCutSecret)
	if p := shownPiece(got); p != "" {
		t.Fatalf("the title shows a piece of the value: %q", p)
	}
	// "€" is 3 bytes: byte 80 falls inside one.
	if got := clipTitle(strings.Repeat("€", 60), nil); !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("cut title %q", got)
	}
}

// A warm process that dies: its stderr tail, in the turn's error, is scrubbed
// before it is cut.
func TestAWarmProcessDeathIsScrubbedBeforeItIsCut(t *testing.T) {
	f := filepath.Join(t.TempDir(), "stderr")
	if err := os.WriteFile(f, []byte(cutSecret+strings.Repeat("#", tailShow-50)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, a := range []Agent{
		&ClaudeStream{Bin: os.Args[0], Env: append(fakeEnv("claude"), "WARM_FAKE_STDERR="+f)},
		&CodexServer{Bin: os.Args[0], Env: append(fakeEnv("codex"), "WARM_FAKE_STDERR="+f)},
	} {
		t.Run(a.Name(), func(t *testing.T) {
			defer a.(WarmAgent).Close()
			turn := turnText("die")
			turn.Scrub = scrubCutSecret
			_, err := a.Execute(context.Background(), turn)
			if err == nil {
				t.Fatal("a dead process returned no error")
			}
			if p := shownPiece(err.Error()); p != "" {
				t.Fatalf("the error shows a piece of the value: %q", p)
			}
			if !strings.Contains(err.Error(), "[redacted]") {
				t.Fatalf("the stderr tail is not in the error: %.200s", err)
			}
		})
	}
}

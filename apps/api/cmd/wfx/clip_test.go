package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// clip used to slice bytes, so a limit landing inside a multi-byte character
// (the ⏎ it inserts, or an em dash) produced invalid UTF-8 — and a program
// reading `wfx logs` crashed decoding it.
func TestClipNeverSplitsACharacter(t *testing.T) {
	s := strings.Repeat("a", 9) + "—tail" // the em dash spans bytes 9..11
	for n := 1; n < len(s); n++ {
		if out := clip(s, n); !utf8.ValidString(out) {
			t.Fatalf("clip(%q, %d) = %q is not valid UTF-8", s, n, out)
		}
	}
}

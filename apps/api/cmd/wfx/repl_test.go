package main

import (
	"strings"
	"testing"
)

// A typed line is argv, and quotes hold. strings.Fields would be wrong in a way
// that only surfaces later: `-i title="fix the parser"` is the ordinary case for
// this product, and splitting it into three would hand a verb arguments that fail
// somewhere far from the typo.
func TestSplitLineHonoursQuotes(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{`runs`, []string{"runs"}},
		{`  runs   --json  `, []string{"runs", "--json"}},
		{`run bug-fix -i title="fix the parser"`, []string{"run", "bug-fix", "-i", "title=fix the parser"}},
		{`run w -i a='one two' -i b="three four"`, []string{"run", "w", "-i", "a=one two", "-i", "b=three four"}},
		{`state set --workflow k ""`, []string{"state", "set", "--workflow", "k", ""}},
	} {
		got, err := splitLine(tc.line)
		if err != nil {
			t.Fatalf("%q: %v", tc.line, err)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%q → %q, want %q", tc.line, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%q → arg %d is %q, want %q", tc.line, i, got[i], tc.want[i])
			}
		}
	}
	if _, err := splitLine(`run w -i a="unclosed`); err == nil {
		t.Error("an unclosed quote was accepted; the verb would receive a truncated value")
	}
}

// A session survives a typo. main exits 1 because a script needs the status; a
// REPL that exited on a bad verb would be unusable, so the error prints and the
// loop continues — and the NEXT line still runs.
func TestASessionSurvivesAnErrorAndStillRunsTheNextLine(t *testing.T) {
	tempContexts(t)
	var out strings.Builder
	replIn, replOut = strings.NewReader("nosuchverb\nversion\nexit\n"), &out
	t.Cleanup(func() { replIn, replOut = nil, nil })

	if err := repl(nil); err != nil {
		t.Fatalf("the session itself failed: %v", err)
	}
	if !strings.Contains(out.String(), "error:") {
		t.Errorf("the bad verb was not reported:\n%s", out.String())
	}
}

// `wfx runs` typed inside wfx is what everybody does at least once. Reporting
// "unknown command wfx" there reads like a broken install, so the prefix is
// dropped.
func TestTheWfxPrefixIsDroppedRatherThanReported(t *testing.T) {
	tempContexts(t)
	var out strings.Builder
	replIn, replOut = strings.NewReader("wfx nosuchverb\nexit\n"), &out
	t.Cleanup(func() { replIn, replOut = nil, nil })
	if err := repl(nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"wfx"`) {
		t.Errorf("the wfx prefix was reported as the unknown command:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "nosuchverb") {
		t.Errorf("the real unknown verb was not named:\n%s", out.String())
	}
}

// A comment and a blank line are not commands. Pasting a snippet into a session
// is the reason: a pasted block carries both.
func TestBlankLinesAndCommentsAreNotCommands(t *testing.T) {
	tempContexts(t)
	var out strings.Builder
	replIn, replOut = strings.NewReader("\n   \n# just a note\nexit\n"), &out
	t.Cleanup(func() { replIn, replOut = nil, nil })
	if err := repl(nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "error") {
		t.Errorf("a blank line or a comment was dispatched:\n%s", out.String())
	}
}

// The prompt names the host. The one mistake that matters in a session is running
// the right command against the wrong platform, and a prompt that hid its target
// would invite it.
func TestThePromptNamesTheHost(t *testing.T) {
	if got := replPrompt("https://wfx.example.com"); !strings.Contains(got, "wfx.example.com") {
		t.Errorf("the prompt does not name the host: %q", got)
	}
}

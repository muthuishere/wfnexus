package judge

import (
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// The cut points are the contract every skill and workflow reads: 0.30 and
// 0.70 themselves are UNCERTAIN, so a value sitting exactly on a line goes to a
// person rather than to whichever side a rounding error prefers.
func TestBandsSendTheMiddleToAHuman(t *testing.T) {
	for p, want := range map[float64]string{0: "no", 0.29: "no", 0.30: "uncertain", 0.5: "uncertain", 0.70: "uncertain", 0.71: "yes", 1: "yes"} {
		if got := DefaultBands.Band(p); got != want {
			t.Errorf("Band(%v) = %s, want %s", p, got, want)
		}
	}
}

func TestQuestionsRefuseWhatCannotBeAsked(t *testing.T) {
	for name, q := range map[string]workflow.Question{
		"unknown type":      {Type: "maybe", Instructions: "x"},
		"one-option choice": {Type: "choice", Instructions: "x", Options: map[string]string{"a": "only"}},
		"one-level score":   {Type: "score", Instructions: "x", Levels: []string{"only"}},
	} {
		if _, err := Questions(map[string]workflow.Question{"q": q}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := Questions(map[string]workflow.Question{"q": {Type: "noul", Instructions: "is it?"}}); err != nil {
		t.Errorf("a plain noul was refused: %v", err)
	}
}

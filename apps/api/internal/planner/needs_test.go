package planner

import "testing"

func ids(as []Action) []string {
	var out []string
	for _, a := range as {
		out = append(out, a.ID)
	}
	return out
}

// An action with needs waits for them whatever the facts say; one whose needs
// are done but whose guard does not hold is settled (skipped), so what needs
// it is not stuck behind it.
func TestNeedsOrderActionsAndSettleSkippedOnes(t *testing.T) {
	never := func(*World) bool { return false }
	p := Plan{Actions: []Action{
		{ID: "plan"},
		{ID: "approve", Needs: []string{"plan"}, Guard: never},
		{ID: "act", Needs: []string{"plan", "approve"}},
	}}
	w := NewWorld()
	done := map[string]bool{}
	if got := ids(p.Ready(w, done)); len(got) != 1 || got[0] != "plan" {
		t.Fatalf("first wave = %v, want only plan", got)
	}
	if s := p.Settled(w, done); len(s) != 0 {
		t.Fatalf("settled before plan ran: %v", ids(s))
	}
	done["plan"] = true
	if got := ids(p.Ready(w, done)); len(got) != 0 {
		t.Fatalf("act started before approve was settled: %v", got)
	}
	s := p.Settled(w, done)
	if len(s) != 1 || s[0].ID != "approve" {
		t.Fatalf("settled = %v, want approve", ids(s))
	}
	done["approve"] = true
	if got := ids(p.Ready(w, done)); len(got) != 1 || got[0] != "act" {
		t.Fatalf("after approve: %v, want act", got)
	}
}

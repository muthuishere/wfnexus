package engine

import (
	"context"
	"encoding/json"
	"testing"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func reviewEngine(t *testing.T) *Engine {
	t.Helper()
	t.Setenv("WFX_TEST_NO_KEY", "")
	cfg := config.Config{ClassifierBaseURL: "https://example.invalid", ClassifierModel: "m", ClassifierAPIKeyEnv: "WFX_TEST_NO_KEY"}
	return New(cfg, nil, nil, map[string]*workflow.Definition{}, skills.Load(), nil)
}

func TestAProposalWithNoClassifierIsUnreviewedAndNeverFails(t *testing.T) {
	r := reviewEngine(t).ReviewProposal(context.Background(), "diff", "ok")
	if r.Verdict != "unreviewed" || r.Score != nil {
		t.Fatalf("review = %+v", r)
	}
}

func TestTheClassifierVerdictAndScoreAreStoredOnReview(t *testing.T) {
	e := reviewEngine(t)
	for _, c := range []struct {
		p    float64
		want string
	}{{0.91, "approve"}, {0.12, "reject"}} {
		raw, _ := json.Marshal(map[string]any{"model": "jev", "calibrated": true,
			"answers": map[string]any{proposalQuestion: map[string]any{"type": "noul", "noul": c.p}}})
		e.UseClassifier(tn.ClassifierOptions{Style: tn.StyleStatic, Model: "jev",
			Decisions: []tn.RecordedDecision{{State: proposalState("d", "ok"), Questions: proposalQuestions(), Response: raw}}})
		r := e.ReviewProposal(context.Background(), "d", "ok")
		if r.Verdict != c.want || r.Score == nil || *r.Score != c.p {
			t.Fatalf("p=%v: review = %+v", c.p, r)
		}
	}
}

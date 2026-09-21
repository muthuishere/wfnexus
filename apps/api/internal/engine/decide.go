package engine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/workflow"
)

// decisionRecord is what we store and show: the typed answers plus the two
// caveats that decide whether a number may be thresholded at all.
type decisionRecord struct {
	Model      string                 `json:"model"`
	Calibrated bool                   `json:"calibrated"`
	Answers    map[string]answerValue `json:"answers"`
	CostUSD    *float64               `json:"costUsd,omitempty"`
}

type answerValue struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Legend        map[string]string  `json:"legend,omitempty"`
	NearUniform   bool               `json:"nearUniform,omitempty"`
}

// toQuestions converts the YAML questions into toolnexus questions.
func toQuestions(qs map[string]workflow.Question) (map[string]tn.Question, error) {
	out := map[string]tn.Question{}
	for key, q := range qs {
		switch q.Type {
		case "noul":
			nq := tn.NoulQuestion{Instructions: q.Instructions}
			if q.True != "" || q.False != "" {
				nq.Criteria = &tn.NoulCriteria{True: q.True, False: q.False}
			}
			out[key] = nq
		case "choice":
			out[key] = tn.ChoiceQuestion{Instructions: q.Instructions, Criteria: q.Options}
		case "score":
			out[key] = tn.ScoreQuestion{Instructions: q.Instructions, Criteria: q.Levels}
		default:
			return nil, fmt.Errorf("question %q: unknown type %q", key, q.Type)
		}
	}
	return out, nil
}

// decide runs the step's classifier pass before its agent starts: typed
// questions on a small model, answered in one round trip.
func (e *Engine) decide(ctx context.Context, runID uuid.UUID, step *workflow.Step, data workflow.TemplateData) (*decisionRecord, map[string]any, error) {
	if step.Decide == nil {
		return nil, nil, nil
	}
	state, err := workflow.Render(step.Decide.State, data)
	if err != nil {
		return nil, nil, fmt.Errorf("decide state: %w", err)
	}
	questions, err := toQuestions(step.Decide.Questions)
	if err != nil {
		return nil, nil, err
	}
	c, err := e.classifier()
	if err != nil {
		return nil, nil, err
	}
	d, err := c.Evaluate(ctx, state, questions)
	if err != nil {
		return nil, nil, fmt.Errorf("classifier: %s", scrub(err.Error()))
	}

	rec := &decisionRecord{Model: d.Model, Calibrated: d.Calibrated, Answers: map[string]answerValue{}}
	// Cost is nil when the backend does not report one — absent is NOT zero, and
	// printing $0.00 for "this backend does not say" would be a lie about money.
	rec.CostUSD = d.Usage.Cost
	// vals is the flat view prompts and gates read: {"fixability": 1.8, ...}
	vals := map[string]any{}
	for key, q := range step.Decide.Questions {
		switch q.Type {
		case "noul":
			a, err := d.Noul(key)
			if err != nil {
				return nil, nil, err
			}
			v := a.Noul
			rec.Answers[key] = answerValue{Type: "noul", Noul: &v}
			vals[key] = v
		case "choice":
			a, err := d.Choice(key)
			if err != nil {
				return nil, nil, err
			}
			conf := a.Confidence
			rec.Answers[key] = answerValue{Type: "choice", Choice: a.Choice, Confidence: &conf,
				Probabilities: a.Probabilities, NearUniform: a.NearUniform}
			vals[key] = a.Choice
		case "score":
			a, err := d.Score(key)
			if err != nil {
				return nil, nil, err
			}
			v, conf := a.Score, a.Confidence
			rec.Answers[key] = answerValue{Type: "score", Score: &v, Confidence: &conf,
				Probabilities: a.Probabilities, Legend: a.Legend}
			vals[key] = v
		}
	}
	e.emit(ctx, runID, step.ID, "decision", rec)
	return rec, vals, nil
}

// classifier builds the judge. systemone is reachable over OpenRouter with
// model typesafe/jev-1.13; the library's own default base 400s ("Unknown
// model"), so the base URL and model are always set explicitly. Verified in
// spikes/04-classifier.
func (e *Engine) classifier() (*tn.Classifier, error) {
	if e.classifierOpts != nil {
		return tn.CreateClassifier(*e.classifierOpts)
	}
	return tn.CreateClassifier(tn.ClassifierOptions{
		Style:     tn.StyleSystemOne,
		BaseURL:   e.cfg.ClassifierBaseURL,
		Model:     e.cfg.ClassifierModel,
		APIKeyEnv: e.cfg.ClassifierAPIKeyEnv,
	})
}

// decideGate reports whether a gate's condition is met by the answers.
func decideGate(g workflow.DecideGate, vals map[string]any) bool {
	v, ok := vals[g.Question]
	if !ok {
		return false
	}
	switch {
	case g.Below != nil:
		n, ok := asFloat(v)
		return ok && n < *g.Below
	case g.AtLeast != nil:
		n, ok := asFloat(v)
		return ok && n >= *g.AtLeast
	case g.Is != "":
		s, ok := v.(string)
		return ok && s == g.Is
	}
	return false
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

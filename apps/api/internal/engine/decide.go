package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/google/uuid"
	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/judge"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
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

// toQuestions converts the YAML questions into toolnexus questions — the same
// conversion `wfx judge` uses (package judge), so a question means one thing.
func toQuestions(qs map[string]workflow.Question) (map[string]tn.Question, error) {
	return judge.Questions(qs)
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
	c, err := e.classifierFor(step)
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

// classifierFor builds the judge a step asked for.
//
// A step's `classifier:` names a registry entry — any JEV model an install has
// configured. It used to be validated at load and then IGNORED: every decide
// ran on the global default, so a step naming another model quietly got a
// different one. Now the entry is what runs, and only a step that names none
// falls back to the process default.
//
// The default: systemone over OpenRouter with typesafe/jev-1.13; the library's
// own default base 400s ("Unknown model"), so base URL and model are always set
// explicitly. Verified in spikes/04-classifier.
func (e *Engine) classifierFor(step *workflow.Step) (*tn.Classifier, error) {
	if e.classifierOpts != nil {
		return tn.CreateClassifier(*e.classifierOpts)
	}
	name := ""
	if step != nil {
		name = step.Classifier
	}
	// The operator's default classifier stands in for a step that names none,
	// exactly as if it had; an unknown default is refused, not bypassed.
	if name == "" {
		name = e.cfg.DefaultClassifier
	}
	if name != "" {
		entry, err := e.catalog.Classifiers.Require(name)
		if err != nil {
			return nil, err
		}
		opts, keyEnv, err := judge.Options(entry)
		if err != nil {
			return nil, err
		}
		e.keyFromStore(keyEnv)
		return tn.CreateClassifier(opts)
	}
	e.keyFromStore(e.cfg.ClassifierAPIKeyEnv)
	return tn.CreateClassifier(tn.ClassifierOptions{
		Style:     tn.StyleSystemOne,
		BaseURL:   e.cfg.ClassifierBaseURL,
		Model:     e.cfg.ClassifierModel,
		APIKeyEnv: e.cfg.ClassifierAPIKeyEnv,
	})
}

// keyFromStore makes a classifier key held in the platform's env store
// visible to the classifier, which reads its key only from the process
// environment by NAME. Under launchd the process has none of the operator's
// shell variables, so every judge ran keyless and TypeSafe answered 403 while
// the key sat in `wfx env` (2026-09-27). Only the system scope is used: a
// judge is not a project's. The value is never logged or returned.
func (e *Engine) keyFromStore(name string) {
	if name == "" || os.Getenv(name) != "" {
		return
	}
	sys, err := e.platformEnv(context.Background(), "")
	if err != nil {
		return
	}
	if v := sys[name]; v != "" {
		_ = os.Setenv(name, v)
	}
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

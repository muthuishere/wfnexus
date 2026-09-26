// Package judge turns typed questions into classifier calls and reads the
// answers back. It is shared by the engine's `decide:`/`judge:` steps and by
// `wfx judge`, so a question means the same thing in a workflow, in a script,
// and in an agent's shell — the thresholds somebody tuned against one of them
// hold for the others.
package judge

import (
	"fmt"
	"time"

	tn "github.com/muthuishere/toolnexus/golang"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Questions converts workflow questions into classifier questions.
func Questions(qs map[string]workflow.Question) (map[string]tn.Question, error) {
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
			if len(q.Options) < 2 {
				return nil, fmt.Errorf("question %q: a choice needs at least two options", key)
			}
			out[key] = tn.ChoiceQuestion{Instructions: q.Instructions, Criteria: q.Options}
		case "score":
			if len(q.Levels) < 2 {
				return nil, fmt.Errorf("question %q: a score needs at least two levels", key)
			}
			out[key] = tn.ScoreQuestion{Instructions: q.Instructions, Criteria: q.Levels}
		default:
			return nil, fmt.Errorf("question %q: unknown type %q (want noul, choice or score)", key, q.Type)
		}
	}
	return out, nil
}

// Bands are the two cut points that split a probability into no / uncertain /
// yes. The default is TypeSafe's own self-consistency cookbook: below 0.30 is
// no, above 0.70 is yes, and everything between goes to a HUMAN rather than
// letting 0.49 and 0.51 trigger opposite automatic actions.
type Bands struct{ Low, High float64 }

var DefaultBands = Bands{Low: 0.30, High: 0.70}

// Band names where a probability falls.
func (b Bands) Band(p float64) string {
	switch {
	case p < b.Low:
		return "no"
	case p > b.High:
		return "yes"
	default:
		return "uncertain"
	}
}

// Answer is one answer as a script or an agent reads it: flat JSON, with the
// band already worked out so nobody re-implements the thresholds by hand.
type Answer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Band          string             `json:"band,omitempty"`
	Choice        string             `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	NearUniform   bool               `json:"nearUniform,omitempty"`
}

// Read extracts every asked question's answer from a decision. A choice or a
// score gets a band from its confidence — "the classifier is not sure which"
// is exactly the case a person should see.
func Read(d tn.Decision, qs map[string]workflow.Question, b Bands) (map[string]Answer, error) {
	out := map[string]Answer{}
	for key, q := range qs {
		switch q.Type {
		case "noul":
			a, err := d.Noul(key)
			if err != nil {
				return nil, err
			}
			v := a.Noul
			out[key] = Answer{Type: "noul", Noul: &v, Band: b.Band(v)}
		case "choice":
			a, err := d.Choice(key)
			if err != nil {
				return nil, err
			}
			c := a.Confidence
			band := "sure"
			if a.NearUniform || c <= b.High {
				band = "uncertain"
			}
			out[key] = Answer{Type: "choice", Choice: a.Choice, Confidence: &c,
				Probabilities: a.Probabilities, NearUniform: a.NearUniform, Band: band}
		case "score":
			a, err := d.Score(key)
			if err != nil {
				return nil, err
			}
			v, c := a.Score, a.Confidence
			band := "sure"
			if c <= b.High {
				band = "uncertain"
			}
			out[key] = Answer{Type: "score", Score: &v, Confidence: &c, Probabilities: a.Probabilities, Band: band}
		}
	}
	return out, nil
}

// Options turns a classifier registry entry into client options, so ANY JEV
// model someone configures — `{"backend": "typesafe", "model": "jev-2.0"}` — is
// usable by name from a workflow step (`classifier: jev-2`) and from
// `wfx judge --classifier jev-2`, through this one function.
//
// keyEnv is the NAME of the variable the key is read from, for an error that
// says which one is missing — empty when the endpoint takes none (a bare URL
// with no apiKeyEnv). The value is never read here.
func Options(c catalog.Classifier) (opts tn.ClassifierOptions, keyEnv string, err error) {
	opts = tn.ClassifierOptions{
		Style: tn.StyleSystemOne, Model: c.Model, BaseURL: c.BaseURL, APIKeyEnv: c.APIKeyEnv,
		Headers: c.Headers, Retries: c.Retries, RetryableStatuses: c.RetryableStatuses,
		RequestParams: c.RequestParams,
	}
	if c.TimeoutSec > 0 {
		opts.Timeout = time.Duration(c.TimeoutSec) * time.Second
	}
	switch c.Backend {
	case "typesafe":
		opts.Backend, keyEnv = tn.BackendTypeSafe, "TYPESAFE_API_KEY"
	case "openrouter":
		opts.Backend, keyEnv = tn.BackendOpenRouter, "OPENROUTER_API_KEY"
	case "":
		// A bare URL: the JEV wire at that endpoint, no vendor preset. It
		// needs no key at all when it is a self-hosted judge behind the
		// network, or one in a header — so none is assumed.
		if c.BaseURL == "" {
			return opts, "", fmt.Errorf("classifier %q: no backend and no baseUrl — nothing to call", c.Name)
		}
	default:
		// llm and static need a chat client or a recorded corpus handed in by
		// the caller; refusing by name beats a judge that silently answers
		// from the default model instead.
		return opts, "", fmt.Errorf("classifier %q: backend %q is not callable here (want typesafe or openrouter)", c.Name, c.Backend)
	}
	if c.APIKeyEnv != "" {
		keyEnv = c.APIKeyEnv
	}
	return opts, keyEnv, nil
}

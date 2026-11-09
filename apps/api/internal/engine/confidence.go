package engine

import (
	"strings"
	"encoding/json"
	"fmt"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// CONFIDENCE — every agent step says how sure it is.
//
// The output contract of every agent step is widened, automatically, with two
// required fields: `confidence` (0-1) and `why` (one line of evidence). Below
// the step's `min_confidence` (default 0.6) the run does not act on the output
// by itself: it waits for approval, with the output kept for a person to read.
// Approving accepts that output as it stands.

// DefaultMinConfidence is the bar an agent output must clear to continue alone.
const DefaultMinConfidence = 0.6

// withConfidence returns a copy of an output schema with confidence + why
// added to its properties and required list. The definition is never mutated.
func withConfidence(schema map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range schema {
		out[k] = v
	}
	if out["type"] == nil {
		out["type"] = "object"
	}
	props := map[string]any{}
	if p, ok := schema["properties"].(map[string]any); ok {
		for k, v := range p {
			props[k] = v
		}
	}
	props["confidence"] = map[string]any{"type": "number", "minimum": 0, "maximum": 1,
		"description": "How sure you are that this output is correct and safe to act on, 0-1. Be honest: below 0.6 a person reviews it."}
	props["why"] = map[string]any{"type": "string",
		"description": "One line: the evidence for that confidence (what you checked, what you could not)."}
	out["properties"] = props
	req := []any{}
	seen := map[string]bool{}
	switch r := schema["required"].(type) {
	case []any:
		for _, x := range r {
			req = append(req, x)
			seen[fmt.Sprint(x)] = true
		}
	case []string:
		for _, x := range r {
			req = append(req, x)
			seen[x] = true
		}
	}
	for _, k := range []string{"confidence", "why"} {
		if !seen[k] {
			req = append(req, k)
		}
	}
	out["required"] = req
	return out
}

// ensureConfidence fills a missing confidence/why as "not reported" (= 0).
func ensureConfidence(out map[string]any) map[string]any {
	if out == nil {
		return out
	}
	if _, ok := out["confidence"].(float64); !ok {
		if _, isInt := out["confidence"].(int); !isInt {
			out["confidence"] = 0.0
		}
	}
	if s, _ := out["why"].(string); s == "" {
		out["why"] = "not reported"
	}
	return out
}

func confidenceOf(out map[string]any) (float64, bool) {
	switch v := out["confidence"].(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

func minConfidence(step *workflow.Step) float64 {
	if step.MinConfidence != nil {
		return *step.MinConfidence
	}
	return DefaultMinConfidence
}

// lowConfidence reports whether an accepted agent output must wait for a
// person, and the reason shown to them. A step already behind an approval
// gate is not gated twice.
func lowConfidence(step *workflow.Step, out map[string]any) (string, bool) {
	if out == nil || step.RequiresApproval || !takesAction(step) {
		return "", false
	}
	c, _ := confidenceOf(out)
	min := minConfidence(step)
	if c >= min {
		return "", false
	}
	why, _ := out["why"].(string)
	return fmt.Sprintf("low confidence %.2f < %.2f: %s", c, min, why), true
}

// approvedLowConfidence is the output of a step a person approved after it
// paused on low confidence: approved, with an output already recorded.
func approvedLowConfidence(step *workflow.Step, st *model.StepRun) (map[string]any, bool) {
	if st == nil || st.Status != "approved" || len(st.Output) == 0 || string(st.Output) == "null" {
		return nil, false
	}
	var out map[string]any
	if json.Unmarshal(st.Output, &out) != nil || out == nil {
		return nil, false
	}
	if _, ok := out["confidence"]; !ok {
		return nil, false
	}
	return out, true
}

// actionTools are tools that change something outside the agent's head.
var actionTools = map[string]bool{"bash": true, "shell": true, "write": true, "edit": true, "multi_edit": true, "apply_patch": true}

// actionWords mark a tool or module command with side effects by its name.
var actionWords = []string{"send", "create", "deploy", "publish", "post", "delete", "write", "merge", "push", "comment", "reply", "upload", "put"}

// takesAction reports whether an agent step can change the world: a shell or
// file-writing tool, or a command whose name says it sends, creates, deploys.
// Only such a step is worth a person's attention when it is unsure or stuck;
// an analysis step records its confidence/question and the run goes on.
func takesAction(step *workflow.Step) bool {
	if step.RequiresApproval {
		return true
	}
	for _, t := range step.Tools {
		n := strings.ToLower(t)
		if actionTools[n] {
			return true
		}
		for _, w := range actionWords {
			if strings.Contains(n, w) {
				return true
			}
		}
	}
	return false
}

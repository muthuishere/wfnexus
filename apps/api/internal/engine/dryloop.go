package engine

import (
	"fmt"
	"regexp"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// DryLoop is what a loop step would do, said before it does it.
type DryLoop struct {
	Mode string `json:"mode"` // foreach | until | while
	// Over is the foreach path, or "literal" for an inline list.
	Over string `json:"over,omitempty"`
	// Items is how many foreach iterations the sample data yields; -1 when
	// the list only exists at run time (an earlier step's output).
	Items         int    `json:"items"`
	MaxIterations int    `json:"maxIterations,omitempty"`
	Parallel      int    `json:"parallel,omitempty"`
	Until         string `json:"until,omitempty"`
	// Iterations are the run-row ids the loop would create, as far as they
	// are known now — every one for a foreach over known data, up to
	// max_iterations for until/while.
	Iterations []string `json:"iterations"`
	// Body is each body step, checked as iteration 0 would run it.
	Body []DryRunStep `json:"body,omitempty"`
}

// dryLoop checks a loop step: its own template (single-step loop) or each
// body step's, rendered as the FIRST iteration would see them, and lists the
// iterations the run would plan.
func (e *Engine) dryLoop(def *workflow.Definition, s *workflow.Step, sample map[string]any, waveOf map[string]int,
	fail func(DryProblem), checkStep func(*workflow.Step, workflow.TemplateData, *workflow.Definition, map[string]int) DryRunStep) DryRunStep {

	td := dryTemplateData(def, sample, waveOf, s.ID)
	loop := &DryLoop{Mode: s.LoopMode(), Items: -1, MaxIterations: s.MaxIterations, Parallel: s.Parallel}

	switch loop.Mode {
	case "foreach":
		if lit := s.ForeachLiteral(); lit != nil {
			loop.Over, loop.Items = "literal", len(lit)
			td.Item = lit[0]
		} else {
			loop.Over = s.ForeachPath()
			if v, ok := workflow.ResolvePath(loop.Over, sample, td.Steps); ok {
				if l, ok := v.([]any); ok {
					// Only input lists are known before the run; a step's
					// list is a sample of one and says nothing about size.
					if _, inInput := workflow.ResolvePath(loop.Over, sample, nil); inInput {
						loop.Items = len(l)
					}
					if len(l) > 0 {
						td.Item = l[0]
					}
				}
			}
			if td.Item == nil {
				td.Item = "(dry run item)"
			}
		}
		n := loop.Items
		if n < 0 {
			n = 1 // at least the first, to show the shape
		}
		if s.MaxIterations > 0 && loop.Items > s.MaxIterations {
			fail(DryProblem{Step: s.ID, Field: "foreach", Fatal: true,
				Message: fmt.Sprintf("foreach yields %d items, over max_iterations %d — the run would refuse it", loop.Items, s.MaxIterations)})
		}
		if loop.Items == 0 {
			fail(DryProblem{Step: s.ID, Field: "foreach", Fatal: false,
				Message: fmt.Sprintf("foreach %s is empty for this input — the loop would run zero times", loop.Over)})
		}
		loop.Iterations = plannedIDs(s, n)
	default:
		loop.Until = guardText(s.LoopGuards())
		loop.Iterations = plannedIDs(s, s.MaxIterations)
	}

	// The reference check for a body runs against a view where the body's
	// steps exist: outer steps keep their order, the loop's body sits at the
	// loop's position, and in an until/while loop every body step counts as
	// earlier — the next iteration reads the previous one's outputs.
	refDef := &workflow.Definition{Name: def.Name, InputSchema: def.InputSchema}
	refWave := map[string]int{}
	for i := range def.Steps {
		refDef.Steps = append(refDef.Steps, def.Steps[i])
		refWave[def.Steps[i].ID] = waveOf[def.Steps[i].ID] * 1000
	}
	base := waveOf[s.ID] * 1000

	if !s.IsGroup() {
		ds := checkStep(s, td, def, waveOf)
		ds.Loop = loop
		return ds
	}

	ds := DryRunStep{ID: s.ID, Kind: "loop", Wave: waveOf[s.ID], Loop: loop}
	for k := range s.Steps {
		b := &s.Steps[k]
		refDef.Steps = append(refDef.Steps, *b)
		if loop.Mode == "foreach" {
			refWave[b.ID] = base + 1 + k
		} else {
			refWave[b.ID] = base // all "earlier" than any body step below
		}
	}
	bodyTD := td
	bodyTD.Steps = map[string]any{}
	for k, v := range td.Steps {
		bodyTD.Steps[k] = v
	}
	if loop.Mode != "foreach" {
		for k := range s.Steps {
			bodyTD.Steps[s.Steps[k].ID] = sampleOutput(&s.Steps[k])
		}
	}
	for k := range s.Steps {
		b := &s.Steps[k]
		wave := refWave[b.ID]
		refWave[b.ID] = base + 1 + k
		bd := checkStep(b, bodyTD, refDef, refWave)
		refWave[b.ID] = wave
		bd.ID, bd.Wave = workflow.IterationID(s.ID, 0, b.ID), waveOf[s.ID]
		loop.Body = append(loop.Body, bd)
		bodyTD.Steps[b.ID] = sampleOutput(b)
	}
	return ds
}

func plannedIDs(s *workflow.Step, n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if !s.IsGroup() {
			out = append(out, workflow.IterationID(s.ID, i, ""))
			continue
		}
		for _, b := range s.Steps {
			out = append(out, workflow.IterationID(s.ID, i, b.ID))
		}
	}
	return out
}

func guardText(gs []workflow.Guard) string {
	out := ""
	for i, g := range gs {
		if i > 0 {
			out += " and "
		}
		if g.Exists != nil {
			if *g.Exists {
				out += g.Path + " exists"
			} else {
				out += g.Path + " is absent"
			}
			continue
		}
		out += fmt.Sprintf("%s = %s", g.Path, mustJSON(g.Equals))
	}
	return out
}

var loopVarRef = regexp.MustCompile(`\.(Item|Index)\b`)

func usesLoopVars(text string) bool { return loopVarRef.MatchString(text) }

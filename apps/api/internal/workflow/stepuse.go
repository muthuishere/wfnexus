package workflow

import "fmt"

// Step-level module calls (issue #82), the GitHub Actions shape:
//
//	steps:
//	  - id: ctx
//	    uses: load-context@1
//	    with: { channel: ops, limit: 5 }
//	  - id: reply
//	    needs: [ctx]
//	    prompt: ...
//
// A step with `uses:` is a call, not work: at load time it is replaced IN
// PLACE by the task's steps (ids `ctx.<task step>`), and `with:` holds the
// typed input VALUES, validated against the task's `inputs:` exactly like a
// top-level `use:` with `inputs:`. A later step's `needs: [ctx]` is rewritten
// to the call's last steps, and the call's own `needs:` attach to its first
// steps, so the call behaves as one node in the graph.
//
// (Top-level and job-level `uses:` lists keep `with:` = harness overrides keyed
// by step id. On a STEP there is nothing else `with:` could mean, so it means
// what it means in Actions: the inputs.)

func (d *Definition) expandStepUses(tasks map[string]*Task, opt loadOptions) error {
	steps, err := expandSteps(d, "", d.Steps, tasks, opt)
	if err != nil {
		return err
	}
	d.Steps = steps
	return nil
}

// expandSteps returns steps with every `uses:` step expanded. label is "job X"
// inside a job, where steps chain sequentially and the task's own needs are
// dropped (the job's chaining already orders them).
func expandSteps(d *Definition, label string, steps []Step, tasks map[string]*Task, opt loadOptions) ([]Step, error) {
	has := false
	for _, s := range steps {
		if s.Uses != "" {
			has = true
		} else if len(s.With) > 0 {
			return nil, fmt.Errorf("%s/%s: `with:` on a step needs `uses:` (it holds the module's inputs)", d.Name, s.ID)
		}
	}
	if !has {
		return steps, nil
	}
	inJob := label != ""
	var out []Step
	tails := map[string][]string{} // call id -> its last step ids
	for _, s := range steps {
		if s.Uses == "" {
			out = append(out, s)
			continue
		}
		if s.ID == "" {
			return nil, fmt.Errorf("%s: a step with `uses: %s` needs an id", d.Name, s.Uses)
		}
		if s.Prompt != "" || s.Run != "" || s.Judge != nil || s.Send != nil || s.Start != nil || len(s.Steps) > 0 {
			return nil, fmt.Errorf("%s/%s: a `uses:` step is a module call — it may not also carry prompt, run, judge, send, start or steps", d.Name, s.ID)
		}
		tmp := &Definition{Name: d.Name, useLabel: joinLabel(label, "step "+s.ID),
			Uses: []Use{{Task: s.Uses, As: s.ID, Inputs: s.With}}}
		if err := tmp.expandUses(tasks, opt); err != nil {
			return nil, err
		}
		d.RemotePins = append(d.RemotePins, tmp.RemotePins...)
		exp := tmp.Steps
		needed := map[string]bool{}
		for _, e := range exp {
			for _, n := range e.Needs {
				needed[n] = true
			}
		}
		for i := range exp {
			if inJob {
				exp[i].Needs = nil
			} else if len(exp[i].Needs) == 0 {
				exp[i].Needs = append([]string(nil), s.Needs...)
			}
		}
		for i := range exp {
			exp[i].When = append(append([]Guard(nil), s.When...), exp[i].When...)
		}
		for _, e := range exp {
			if !needed[e.ID] {
				tails[s.ID] = append(tails[s.ID], e.ID)
			}
		}
		out = append(out, exp...)
	}
	if !inJob {
		for i := range out {
			var ns []string
			for _, n := range out[i].Needs {
				if t, ok := tails[n]; ok {
					ns = append(ns, t...)
				} else {
					ns = append(ns, n)
				}
			}
			out[i].Needs = ns
		}
	}
	return out, nil
}

func joinLabel(a, b string) string {
	if a == "" {
		return b
	}
	return a + " " + b
}

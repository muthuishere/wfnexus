// Package planner derives a workflow's execution order instead of taking it
// from hand-wired edges.
//
// A step declares what it CONSUMES and what it PRODUCES — facts, not step ids —
// and the workflow declares the GOAL fact it must reach. The planner works out
// which steps can run, runs every ready one concurrently, and RE-PLANS after
// each completion against the facts that now exist. A step may also carry a
// `when` guard evaluated against the real values produced so far, so the plan
// responds to what the agents actually found rather than to what the author
// guessed at authoring time.
//
// This is goal-oriented planning in the Embabel sense — the plan is formulated
// by the system, not the programmer — mapped onto what we already had: a step's
// JSON-schema output contract is its postcondition, so the facts are typed and
// validated before they can satisfy anything.
//
// It is deliberately NOT a search. There is no cost model and no backtracking:
// planning is "every step whose preconditions hold, now", re-evaluated each
// time the world changes. That is enough for a workflow and it keeps the
// failure mode explainable, which a search does not.
package planner

import (
	"fmt"
	"sort"
	"strings"
)

// Fact is a name that becomes true when a step produces it. Values live
// alongside, so a guard can read `triage.valid` as well as know `triage` exists.
type Fact = string

// World is what is known right now: which facts hold, and their values.
//
// NOT SAFE FOR CONCURRENT USE. The engine mutates it from the goroutine that
// completes a step, and reads it when planning the next wave, so every access
// is made under the engine's own mutex. If a caller ever plans from more than
// one goroutine, this needs its own lock — it does not have one on purpose,
// because a second lock would invite the two to disagree about ordering.
type World struct {
	facts  map[Fact]bool
	values map[Fact]any
}

func NewWorld() *World {
	return &World{facts: map[Fact]bool{}, values: map[Fact]any{}}
}

// Assert records a fact and its value.
func (w *World) Assert(name Fact, value any) {
	w.facts[name] = true
	w.values[name] = value
}

func (w *World) Holds(name Fact) bool { return w.facts[name] }

// Value resolves a dotted path — `triage.valid` reads into the object a step
// produced, so a guard can branch on a field rather than only on existence.
func (w *World) Value(path string) (any, bool) {
	head, rest, _ := strings.Cut(path, ".")
	cur, ok := w.values[head]
	if !ok {
		return nil, false
	}
	for rest != "" {
		var key string
		key, rest, _ = strings.Cut(rest, ".")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[key]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func (w *World) Facts() []Fact {
	out := make([]Fact, 0, len(w.facts))
	for f := range w.facts {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// Action is one planned unit — a workflow step, reduced to what planning needs.
type Action struct {
	ID       string
	Consumes []Fact
	Produces []Fact
	// Guard must hold for the action to be applicable, beyond its preconditions.
	Guard func(*World) bool
}

// Plan is a static view of a workflow, used for validation and explanation.
type Plan struct {
	Goal    Fact
	Actions []Action
	// Given are the facts present before anything runs (the run's input).
	Given []Fact
}

// Ready returns every action whose preconditions and guard hold and which has
// not run, sorted for determinism. This is called again after each completion —
// that re-evaluation IS the replanning.
func (p Plan) Ready(w *World, done map[string]bool) []Action {
	var out []Action
	for _, a := range p.Actions {
		if done[a.ID] || !applicable(a, w) {
			continue
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func applicable(a Action, w *World) bool {
	for _, c := range a.Consumes {
		if !w.Holds(c) {
			return false
		}
	}
	return a.Guard == nil || a.Guard(w)
}

// Done reports whether the goal has been reached. A workflow with no goal runs
// until nothing is left, which is the behaviour of an ordinary pipeline.
func (p Plan) Done(w *World, done map[string]bool) bool {
	if p.Goal != "" {
		return w.Holds(p.Goal)
	}
	return len(done) == len(p.Actions)
}

// Stuck explains why nothing can run — the error a planner owes its user. It
// names the missing fact AND whether any step could ever produce it, because
// "blocked" and "impossible" need different fixes.
func (p Plan) Stuck(w *World, done map[string]bool) error {
	if p.Done(w, done) {
		return nil
	}
	producers := map[Fact][]string{}
	for _, a := range p.Actions {
		for _, f := range a.Produces {
			producers[f] = append(producers[f], a.ID)
		}
	}
	var lines []string
	for _, a := range p.Actions {
		if done[a.ID] {
			continue
		}
		var missing []string
		for _, c := range a.Consumes {
			if !w.Holds(c) {
				who := producers[c]
				if len(who) == 0 {
					missing = append(missing, fmt.Sprintf("%s (nothing produces it)", c))
				} else {
					missing = append(missing, fmt.Sprintf("%s (from %s, which has not run)", c, strings.Join(who, " or ")))
				}
			}
		}
		switch {
		case len(missing) > 0:
			lines = append(lines, fmt.Sprintf("  %s needs %s", a.ID, strings.Join(missing, ", ")))
		case a.Guard != nil && !a.Guard(w):
			lines = append(lines, fmt.Sprintf("  %s is applicable but its `when` guard does not hold", a.ID))
		}
	}
	goal := p.Goal
	if goal == "" {
		goal = "(every step)"
	}
	if len(lines) == 0 {
		return fmt.Errorf("goal %q was not reached and no step remains to run", goal)
	}
	return fmt.Errorf("goal %q cannot be reached — nothing is runnable:\n%s\nknown facts: %v",
		goal, strings.Join(lines, "\n"), w.Facts())
}

// Validate checks the plan can work at all, before anything runs: every
// consumed fact must be produced by some step or supplied as input, the goal
// must be producible, and no step may be unreachable.
func (p Plan) Validate() error {
	produced := map[Fact][]string{}
	for _, a := range p.Actions {
		for _, f := range a.Produces {
			produced[f] = append(produced[f], a.ID)
		}
	}
	given := map[Fact]bool{}
	for _, f := range p.Given {
		given[f] = true
	}
	for _, a := range p.Actions {
		for _, c := range a.Consumes {
			if len(produced[c]) == 0 && !given[c] {
				return fmt.Errorf("step %q consumes %q, which no step produces and the input does not supply", a.ID, c)
			}
		}
	}
	if p.Goal != "" && len(produced[p.Goal]) == 0 && !given[p.Goal] {
		return fmt.Errorf("goal %q is not produced by any step", p.Goal)
	}
	// A step that can never become applicable is a typo, not a design.
	reach := map[Fact]bool{}
	for f := range given {
		reach[f] = true
	}
	for progress := true; progress; {
		progress = false
		for _, a := range p.Actions {
			ok := true
			for _, c := range a.Consumes {
				if !reach[c] {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			for _, f := range a.Produces {
				if !reach[f] {
					reach[f] = true
					progress = true
				}
			}
		}
	}
	for _, a := range p.Actions {
		for _, c := range a.Consumes {
			if !reach[c] {
				return fmt.Errorf("step %q can never run: %q is only produced by a step that itself can never run (a dependency cycle among facts)", a.ID, c)
			}
		}
	}
	if p.Goal != "" && !reach[p.Goal] {
		return fmt.Errorf("goal %q is unreachable from the workflow's input", p.Goal)
	}
	return nil
}

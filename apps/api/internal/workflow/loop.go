package workflow

import (
	"fmt"
	"strconv"
	"strings"
)

// Loops.
//
// A step repeats in one of three ways, each spelled the way a developer who
// has used GitHub Actions' matrix or Argo's withItems / retry-until would
// guess:
//
//	foreach: input.files          # a path to an array, or a literal list
//	parallel: 3                   # optional cap on concurrent iterations
//
//	until:                        # repeat until every guard holds…
//	  - path: check.ok
//	    equals: true
//	max_iterations: 5             # …never more than this (REQUIRED)
//
//	while: [...]                  # repeat while every guard holds (do…while)
//	max_iterations: 5
//
// The repeated thing is either the step itself (it keeps its prompt/run) or a
// `steps:` body run in order once per iteration. Every iteration is its own
// row on the run — `review[2]`, `fix-loop[1].check` — so it is persisted,
// resumable, approvable and visible exactly like any step. The loop step's own
// output is the aggregate: {iterations: [...], count, last}.
//
// There is no unbounded loop: until/while without max_iterations is refused at
// load, and a loop that exhausts its bound without its condition settling
// FAILS rather than quietly carrying on (invariant 3: never silently succeed).

// MaxLoopIterations is the hard ceiling on any one loop, and MaxLoopBody on the
// size of its body. Both follow from how iteration rows are positioned (see
// IterationPosition) and are far beyond what a sane workflow needs.
const (
	MaxLoopIterations = 999
	MaxLoopBody       = 99
)

// LoopMode names how a step repeats: "foreach", "until", "while" or "".
func (s *Step) LoopMode() string {
	switch {
	case s.Foreach != nil:
		return "foreach"
	case len(s.Until) > 0:
		return "until"
	case len(s.While) > 0:
		return "while"
	}
	return ""
}

// IsLoop reports whether the step repeats.
func (s *Step) IsLoop() bool { return s.LoopMode() != "" }

// IsGroup reports whether the loop repeats a `steps:` body rather than itself.
func (s *Step) IsGroup() bool { return len(s.Steps) > 0 }

// ApprovesOnce reports whether this step's requires_approval is asked ONCE,
// before it starts. True for every ordinary step and for a loop over a body;
// false for a single-step loop, whose approval is asked per iteration — each
// iteration is the step, so each one is what is being approved.
func (s *Step) ApprovesOnce() bool {
	return s.RequiresApproval && !(s.IsLoop() && !s.IsGroup())
}

// IterationID is the run-row id of one iteration: `review[2]` for a step that
// repeats itself, `fix-loop[1].check` for a body step.
func IterationID(parent string, index int, body string) string {
	id := parent + "[" + strconv.Itoa(index) + "]"
	if body != "" {
		id += "." + body
	}
	return id
}

// IsIterationID reports whether a run-row id names an iteration rather than a
// step of the definition. Step ids may not contain '[' (validated), so the
// bracket is unambiguous.
func IsIterationID(id string) bool { return strings.Contains(id, "[") }

// SplitIterationID is IterationID's inverse.
func SplitIterationID(id string) (parent string, index int, body string, ok bool) {
	open := strings.Index(id, "[")
	if open < 0 {
		return "", 0, "", false
	}
	close := strings.Index(id[open:], "]")
	if close < 0 {
		return "", 0, "", false
	}
	close += open
	n, err := strconv.Atoi(id[open+1 : close])
	if err != nil {
		return "", 0, "", false
	}
	rest := id[close+1:]
	if rest != "" {
		if !strings.HasPrefix(rest, ".") {
			return "", 0, "", false
		}
		rest = rest[1:]
	}
	return id[:open], n, rest, true
}

// IterationPosition orders an iteration's row after every top-level step and
// grouped under its parent: parent position, then iteration, then body index
// (0 for a single-step loop, k+1 for body step k).
func IterationPosition(parentPos, index, bodyIdx int) int {
	return 1_000_000 + parentPos*100_000 + index*100 + bodyIdx
}

// IterationBase is the first position any iteration of the step at parentPos
// can have — what a retry of that step resets from.
func IterationBase(parentPos int) int { return IterationPosition(parentPos, 0, 0) }

// Lookup finds the definition behind a run-row id: a step, or — for an
// iteration row — the step (or body step) it is an iteration of. It is what
// lets an approval inside a loop be checked against the right `approvers:`.
func (d *Definition) Lookup(id string) *Step {
	if _, s := d.Step(id); s != nil {
		return s
	}
	parent, _, body, ok := SplitIterationID(id)
	if !ok {
		return nil
	}
	_, p := d.Step(parent)
	if p == nil {
		return nil
	}
	if body == "" {
		return p
	}
	for i := range p.Steps {
		if p.Steps[i].ID == body {
			return &p.Steps[i]
		}
	}
	return nil
}

// IterationSchema is the schema of ONE iteration's output: the step's own
// for a single-step loop, an object of body-step outputs for a group.
func (s *Step) IterationSchema() map[string]any {
	if !s.IsGroup() {
		return s.OutputSchema
	}
	props := map[string]any{}
	for i := range s.Steps {
		b := &s.Steps[i]
		sch := b.OutputSchema
		if sch == nil {
			sch = map[string]any{"type": "object"}
		}
		props[b.ID] = sch
	}
	return map[string]any{"type": "object", "properties": props}
}

// ResultSchema is what later steps read from this step: its output schema,
// or for a loop the aggregate {iterations, count, last}.
func (s *Step) ResultSchema() map[string]any {
	if !s.IsLoop() {
		return s.OutputSchema
	}
	it := s.IterationSchema()
	if it == nil {
		it = map[string]any{"type": "object"}
	}
	return LoopOutputSchema(it)
}

// LoopOutputSchema wraps one iteration's schema in the aggregate a loop step
// produces.
func LoopOutputSchema(iteration map[string]any) map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"iterations", "count"},
		"properties": map[string]any{
			"iterations": map[string]any{"type": "array", "items": iteration},
			"count":      map[string]any{"type": "integer"},
			"last":       iteration,
		},
	}
}

// LoopGuards are the guards that decide whether an until/while loop goes on.
func (s *Step) LoopGuards() []Guard {
	if len(s.Until) > 0 {
		return s.Until
	}
	return s.While
}

// ForeachPath is the path a foreach reads, or "" for a literal list.
func (s *Step) ForeachPath() string {
	p, _ := s.Foreach.(string)
	return strings.TrimSpace(p)
}

// ForeachLiteral is a literal foreach list, or nil when it is a path.
func (s *Step) ForeachLiteral() []any {
	l, _ := s.Foreach.([]any)
	return l
}

// ResolvePath walks a dotted path through the run's facts: `input.x` reads
// the run input, anything else a step's output. Step ids may contain dots (a
// job's steps are `job.step`), so at every level the LONGEST key that exists
// wins — the same rule as the `stepval` template helper.
func ResolvePath(path string, input map[string]any, steps map[string]any) (any, bool) {
	segs := strings.Split(path, ".")
	if len(segs) == 0 || path == "" {
		return nil, false
	}
	var cur any
	if segs[0] == "input" {
		if _, clash := steps["input"]; !clash {
			cur, segs = input, segs[1:]
		}
	}
	if cur == nil {
		cur = steps
	}
	for i := 0; i < len(segs); {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		matched := false
		for j := len(segs); j > i; j-- {
			if v, ok := m[strings.Join(segs[i:j], ".")]; ok {
				cur, i, matched = v, j, true
				break
			}
		}
		if !matched {
			return nil, false
		}
	}
	return cur, true
}

// validateLoop checks a loop's shape and references at load time. known maps
// every top-level step id to its position.
func (d *Definition) validateLoop(s *Step, pos map[string]int) error {
	where := d.Name + "/" + s.ID
	if len(s.Steps) > 0 && !s.IsLoop() {
		return fmt.Errorf("%s: `steps:` inside a step is a loop body — add `foreach`, `until` or `while`", where)
	}
	if !s.IsLoop() {
		if s.Parallel != 0 || s.MaxIterations != 0 {
			return fmt.Errorf("%s: `parallel` and `max_iterations` only apply to a loop (`foreach`, `until` or `while`)", where)
		}
		return nil
	}
	modes := 0
	for _, on := range []bool{s.Foreach != nil, len(s.Until) > 0, len(s.While) > 0} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		return fmt.Errorf("%s: a loop is ONE of `foreach`, `until` or `while`", where)
	}
	if s.MaxIterations < 0 || s.MaxIterations > MaxLoopIterations {
		return fmt.Errorf("%s: max_iterations must be 1..%d", where, MaxLoopIterations)
	}
	if s.Parallel < 0 {
		return fmt.Errorf("%s: parallel must be >= 0", where)
	}
	for _, g := range s.Gates {
		if g.Action == "skip_to" {
			return fmt.Errorf("%s: skip_to cannot be used on a loop — a jump out of the middle of a loop has no single meaning", where)
		}
	}
	if s.IsGroup() {
		if s.Prompt != "" || s.Run != "" || s.Judge != nil || s.Send != nil {
			return fmt.Errorf("%s: a loop with a `steps:` body runs those steps — it has no prompt/run/judge/send of its own", where)
		}
		if len(s.Skills) > 0 || len(s.Tools) > 0 || len(s.Gates) > 0 || s.Decide != nil || len(s.State) > 0 {
			return fmt.Errorf("%s: a loop body's steps carry their own skills, tools, gates, decide and state — not the loop step", where)
		}
		if len(s.Steps) > MaxLoopBody {
			return fmt.Errorf("%s: a loop body may hold at most %d steps", where, MaxLoopBody)
		}
	}
	// The body: plain steps, unique ids that do not shadow the outer ones, no
	// ordering of their own (they run in order) and no loop inside a loop.
	body := map[string]bool{}
	for i := range s.Steps {
		b := &s.Steps[i]
		bw := where + "." + b.ID
		if b.ID == "" {
			return fmt.Errorf("%s: body step %d needs an id", where, i)
		}
		if strings.ContainsAny(b.ID, "[]. ") {
			return fmt.Errorf("%s: body step id %q may not contain '.', '[', ']' or spaces", bw, b.ID)
		}
		if body[b.ID] {
			return fmt.Errorf("%s: duplicate body step id %q", where, b.ID)
		}
		if _, clash := pos[b.ID]; clash {
			return fmt.Errorf("%s: body step id %q shadows the top-level step of the same name — rename one", where, b.ID)
		}
		body[b.ID] = true
		if b.IsLoop() || len(b.Steps) > 0 {
			return fmt.Errorf("%s: a loop inside a loop is not supported — split it into two steps", bw)
		}
		if len(b.Needs) > 0 || len(b.Consumes) > 0 || len(b.Produces) > 0 {
			return fmt.Errorf("%s: a body step runs in order within its iteration — needs/consumes/produces have no meaning there", bw)
		}
		for _, g := range b.Gates {
			if g.Action == "skip_to" {
				return fmt.Errorf("%s: skip_to cannot be used inside a loop body", bw)
			}
		}
	}

	switch s.LoopMode() {
	case "foreach":
		if len(s.Until) > 0 || len(s.While) > 0 {
			return fmt.Errorf("%s: a loop is ONE of `foreach`, `until` or `while`", where)
		}
		switch v := s.Foreach.(type) {
		case string:
			if err := d.validateForeachPath(s, strings.TrimSpace(v), pos); err != nil {
				return fmt.Errorf("%s: foreach: %w", where, err)
			}
		case []any:
			if len(v) == 0 {
				return fmt.Errorf("%s: foreach over an empty literal list never runs — remove the step or give it items", where)
			}
			if len(v) > MaxLoopIterations || (s.MaxIterations > 0 && len(v) > s.MaxIterations) {
				return fmt.Errorf("%s: foreach lists %d items, over max_iterations", where, len(v))
			}
		default:
			return fmt.Errorf("%s: foreach wants a path (input.files, list.items) or a list, got %T", where, s.Foreach)
		}
	case "until", "while":
		if s.MaxIterations < 1 {
			return fmt.Errorf("%s: an `%s` loop needs max_iterations — an unbounded loop is a runaway", where, s.LoopMode())
		}
		if s.Parallel > 1 {
			return fmt.Errorf("%s: parallel applies to foreach; `%s` iterations depend on the one before", where, s.LoopMode())
		}
		inside := false
		for _, g := range s.LoopGuards() {
			if strings.TrimSpace(g.Path) == "" {
				return fmt.Errorf("%s: %s guard needs a path", where, s.LoopMode())
			}
			if g.Equals == nil && g.Exists == nil {
				return fmt.Errorf("%s: %s guard on %q needs equals or exists", where, s.LoopMode(), g.Path)
			}
			head, _, _ := strings.Cut(g.Path, ".")
			switch {
			case head == "input":
			case body[head], head == s.ID, strings.HasPrefix(g.Path, s.ID+"."):
				inside = true
			default:
				if _, ok := longestStep(g.Path, pos); !ok {
					return fmt.Errorf("%s: %s guard reads %q, which names no step", where, s.LoopMode(), g.Path)
				}
			}
		}
		if !inside {
			return fmt.Errorf("%s: an `%s` guard must read something the loop produces (%s) — otherwise nothing changes between iterations",
				where, s.LoopMode(), loopProducers(s))
		}
	}
	return nil
}

func loopProducers(s *Step) string {
	if !s.IsGroup() {
		return s.ID + ".<field>"
	}
	ids := make([]string, 0, len(s.Steps))
	for _, b := range s.Steps {
		ids = append(ids, b.ID+".<field>")
	}
	return strings.Join(ids, ", ")
}

// longestStep finds the step a dotted path starts with, longest id first.
func longestStep(path string, pos map[string]int) (string, bool) {
	segs := strings.Split(path, ".")
	for j := len(segs); j > 0; j-- {
		id := strings.Join(segs[:j], ".")
		if _, ok := pos[id]; ok {
			return id, true
		}
	}
	return "", false
}

// validateForeachPath checks that a foreach path names something that exists,
// runs before the loop, and — where a schema says — is an array.
func (d *Definition) validateForeachPath(s *Step, path string, pos map[string]int) error {
	if path == "" {
		return fmt.Errorf("an empty path")
	}
	if strings.Contains(path, "{{") {
		return fmt.Errorf("%q is a template; foreach takes a plain path like input.files or list.items", path)
	}
	if rest, ok := strings.CutPrefix(path, "input."); ok {
		props, _ := d.InputSchema["properties"].(map[string]any)
		if len(props) == 0 {
			return nil // an undeclared input schema accepts anything
		}
		field, _, _ := strings.Cut(rest, ".")
		p, ok := props[field].(map[string]any)
		if !ok {
			return fmt.Errorf("reads input.%s, which the input schema does not declare", field)
		}
		if t, _ := p["type"].(string); t != "" && t != "array" && !strings.Contains(rest, ".") {
			return fmt.Errorf("input.%s is a %s, not an array", field, t)
		}
		return nil
	}
	id, ok := longestStep(path, pos)
	if !ok {
		return fmt.Errorf("%q names no step (or input.*)", path)
	}
	if id == s.ID {
		return fmt.Errorf("a loop cannot iterate over its own output")
	}
	if !d.IsPlanned() {
		if d.IsDAG() {
			if !d.dependsOn(s.ID, id) {
				return fmt.Errorf("reads %s, but %s does not `needs` it — it may not have run yet", id, s.ID)
			}
		} else if pos[id] > pos[s.ID] {
			return fmt.Errorf("reads %s, which runs AFTER this step", id)
		}
	}
	field := strings.TrimPrefix(strings.TrimPrefix(path, id), ".")
	if field == "" {
		return fmt.Errorf("%q names a step but no field — a step's output is an object; name its array field", path)
	}
	_, other := d.Step(id)
	props, _ := other.ResultSchema()["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	head, _, _ := strings.Cut(field, ".")
	p, ok := props[head].(map[string]any)
	if !ok {
		return fmt.Errorf("reads %s.%s, which is not in %q's output schema", id, head, id)
	}
	if t, _ := p["type"].(string); t != "" && t != "array" && !strings.Contains(field, ".") {
		return fmt.Errorf("%s.%s is a %s, not an array", id, head, t)
	}
	return nil
}

// dependsOn reports whether step a transitively needs step b.
func (d *Definition) dependsOn(a, b string) bool {
	deps := map[string][]string{}
	for _, s := range d.Steps {
		deps[s.ID] = s.Needs
	}
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(id string) bool {
		for _, n := range deps[id] {
			if n == b {
				return true
			}
			if !seen[n] {
				seen[n] = true
				if walk(n) {
					return true
				}
			}
		}
		return false
	}
	return walk(a)
}

// bodyRoutedExits marks the steps whose result a loop guard reads, or whose
// exitCode/ok a body step's `when` reads, so `until: check.ok = true` routes
// on a red check instead of failing the run on the first one.
func bodyRoutedExits(s *Step) map[string]bool {
	out := map[string]bool{}
	mark := func(gs []Guard) {
		for _, g := range gs {
			if id, field, ok := strings.Cut(g.Path, "."); ok && (field == "exitCode" || field == "ok") {
				out[id] = true
			}
		}
	}
	// The loop condition reading a step's result at all means that result is
	// an ANSWER — a red check is why the loop goes round again, not a failure.
	for _, g := range s.LoopGuards() {
		if id, _, ok := strings.Cut(g.Path, "."); ok {
			out[id] = true
		}
	}
	for _, b := range s.Steps {
		mark(b.When)
	}
	return out
}

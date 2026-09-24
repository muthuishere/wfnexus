package engine

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Checking what a template REFERENCES, rather than only whether it renders.
//
// Rendering catches very little, because both failure modes are silent by
// design:
//
//   - `missingkey=zero` turns a missing input into empty text, so a prompt with
//     a typo just loses that sentence and the agent reads a question with a
//     hole in it.
//   - `.Steps.<id>.<field>` is rewritten to a `stepval` helper that returns ""
//     for anything it cannot find, so it cannot error even under
//     `missingkey=error` — a step may be skipped, and a template referencing a
//     skipped step must render empty rather than abort the run.
//
// So the references are read out of the template text and checked against the
// things that actually exist: the input schema, and each step's DECLARED output
// schema. That is a stronger check than rendering — it compares the prompt
// against the contract, which is the thing the contract is for.

var (
	// index .Steps "id" "field" — what an author writes for a hyphenated id.
	stepIndexRef = regexp.MustCompile(`index\s+\.Steps\s+"([^"]+)"\s+"([^"]+)"`)
	// .Input.<field>
	inputRef = regexp.MustCompile(`\.Input\.([A-Za-z0-9_]+)`)
	// A reference guarded by `if` or `with` is an explicit statement that the
	// value may be absent, so it is not a missing-field problem.
	optionalRef = regexp.MustCompile(`\{\{-?\s*(?:if|with)\s+[^}]*?\.(?:Input|Steps)\.([A-Za-z0-9_.-]+)`)
)

// checkRefs reports references that cannot resolve.
func checkRefs(def *workflow.Definition, s *workflow.Step, text string, waveOf map[string]int) []DryProblem {
	if text == "" {
		return nil
	}
	var out []DryProblem

	optional := map[string]bool{}
	for _, m := range optionalRef.FindAllStringSubmatch(text, -1) {
		// Both `.Input.x` and `.Steps.a.x` land here; the last segment is the
		// field, and the whole path is recorded so either lookup matches.
		optional[m[1]] = true
		if i := strings.LastIndex(m[1], "."); i >= 0 {
			optional[m[1][i+1:]] = true
		}
	}

	inputProps, _ := def.InputSchema["properties"].(map[string]any)
	for _, m := range inputRef.FindAllStringSubmatch(text, -1) {
		field := m[1]
		if optional[field] {
			continue
		}
		if _, ok := inputProps[field]; !ok {
			out = append(out, DryProblem{
				Step: s.ID, Field: fieldName(s), Fatal: false,
				Message: fmt.Sprintf("references .Input.%s, which the input schema does not declare — "+
					"at run time it renders as nothing and the step never knows", field),
			})
		}
	}

	byID := map[string]*workflow.Step{}
	for i := range def.Steps {
		byID[def.Steps[i].ID] = &def.Steps[i]
	}

	seen, seenBad := map[string]bool{}, map[string]bool{}

	// Matched against the KNOWN step ids rather than split by regex. A job's
	// steps are flattened to `job.step`, so an id contains dots — and a pattern
	// that guessed where the id ended read `.Steps.suite.test.ok` as step
	// "suite", field "test", and reported a step that plainly exists as
	// undefined. Longest id first, so `a.b` wins over `a`.
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return len(ids[i]) > len(ids[j]) })

	var refs [][]string
	for _, id := range ids {
		pat := regexp.MustCompile(`\.Steps\.` + regexp.QuoteMeta(id) + `\.([A-Za-z0-9_]+)`)
		for _, m := range pat.FindAllStringSubmatch(text, -1) {
			refs = append(refs, []string{"", id, m[1]})
		}
	}
	refs = append(refs, stepIndexRef.FindAllStringSubmatch(text, -1)...)

	// A `.Steps.` reference naming nothing we know is a step that does not
	// exist — checked separately, because the loop above only finds known ids.
	for _, m := range regexp.MustCompile(`\.Steps\.([A-Za-z0-9_-]+)`).FindAllStringSubmatch(text, -1) {
		if _, ok := byID[m[1]]; ok {
			continue
		}
		known := false
		for _, id := range ids {
			if strings.HasPrefix(id, m[1]+".") {
				known = true // a job prefix, handled above
				break
			}
		}
		if !known && !optional[m[1]] && !seenBad[m[1]] {
			seenBad[m[1]] = true
			out = append(out, DryProblem{
				Step: s.ID, Field: fieldName(s), Fatal: true,
				Message: fmt.Sprintf("references step %q, which this workflow does not define", m[1]),
			})
		}
	}

	for _, m := range refs {
		id, field := m[1], m[2]
		if seen[id+"."+field] || optional[id] || optional[field] {
			continue
		}
		seen[id+"."+field] = true

		other, ok := byID[id]
		if !ok {
			continue // reported by the unknown-id sweep above
		}
		// A step cannot read a step that has not run. This one is FATAL: it is
		// never intentional and it renders empty, so the prompt is quietly
		// wrong on every single run.
		if waveOf[id] >= waveOf[s.ID] {
			when := "runs at the same time as"
			if waveOf[id] > waveOf[s.ID] {
				when = "runs AFTER"
			}
			out = append(out, DryProblem{
				Step: s.ID, Field: fieldName(s), Fatal: true,
				Message: fmt.Sprintf("reads %s.%s, but %q %s this step — it renders as nothing, every run",
					id, field, id, when),
			})
			continue
		}
		props, _ := other.OutputSchema["properties"].(map[string]any)
		if _, ok := props[field]; !ok {
			out = append(out, DryProblem{
				Step: s.ID, Field: fieldName(s), Fatal: false,
				Message: fmt.Sprintf("reads %s.%s, which is not in %q's output schema — it renders as nothing",
					id, field, id),
			})
		}
	}
	return out
}

func fieldName(s *workflow.Step) string {
	if s.Prompt == "" && s.Run != "" {
		return "run"
	}
	return "prompt"
}

// stateRef matches a state reference in either spelling: the field form
// `.Workflow.last_id` and the index form rewriteStatePaths produces for a key
// holding a dot or a hyphen.
var (
	stateRef      = regexp.MustCompile(`\.(Step|Workflow|Project|Global)\.([A-Za-z0-9_.-]+)`)
	stateIndexRef = regexp.MustCompile(`index\s+\.(Step|Workflow|Project|Global)\s+"([^"]+)"`)
)

// checkStateRefs reports a reference to a state key that nothing holds and
// nothing in this workflow writes.
//
// It is NOT fatal. State is written by whatever ran last — another workflow, a
// person at a terminal, a run that has not happened yet — so "nobody has put
// this here" is a warning about a likely typo, not a verdict. The first run of
// a correct incremental workflow legitimately reads a key that is not there
// yet, and it renders empty, which is the documented behaviour.
func checkStateRefs(s *workflow.Step, text string, known map[string]map[string]string) []DryProblem {
	var out []DryProblem
	seen := map[string]bool{}
	// `ns` is the namespace as the AUTHOR wrote it (.Workflow); the scope is
	// its lower-case name in the store. The message quotes the author's.
	report := func(ns, key string) {
		scope := strings.ToLower(ns)
		if seen[scope+"."+key] {
			return
		}
		seen[scope+"."+key] = true
		if _, ok := known[scope][key]; ok {
			return
		}
		out = append(out, DryProblem{
			Step: s.ID, Field: fieldName(s), Fatal: false,
			Message: fmt.Sprintf("references .%s.%s, which nothing holds and no step writes — "+
				"at run time it renders as nothing (state is four separate namespaces; .%s.%s does not fall back to a wider scope)",
				ns, key, ns, key),
		})
	}
	for _, m := range stateIndexRef.FindAllStringSubmatch(text, -1) {
		report(m[1], m[2])
	}
	for _, m := range stateRef.FindAllStringSubmatch(text, -1) {
		if strings.ContainsAny(m[2], ".-") {
			continue // handled by the index form above
		}
		report(m[1], m[2])
	}
	return out
}

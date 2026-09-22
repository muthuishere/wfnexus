package workflow

import (
	"fmt"
	"sort"
	"strings"
)

// GitHub Actions' shape, because it is the workflow syntax most engineers
// already read fluently — jobs run in parallel, `needs` orders them, `if`
// guards them, `uses`/`with` reuse someone else's work. Adopting a vocabulary
// people know is worth more than a vocabulary we prefer.
//
// What we add on top of it, and do not give up:
//
//   - a step is an AGENT, not a shell line: it carries a soul, scoped skills
//     and tools, a sub-agent team, guardrails and a budget;
//   - `outputs` are SCHEMA-VALIDATED OBJECTS, not strings, so a downstream job
//     reads `needs.triage.outputs.valid` as a real boolean and the workflow
//     cannot advance until the contract is met;
//   - a workflow may declare a GOAL and let the plan be derived instead of
//     writing `needs` at all — which Actions cannot do, because a GHA graph is
//     fixed before the run starts and ours is re-derived after every step.
//
// A Job is the unit of SCHEDULING; a Step inside it is the unit of WORK. Jobs
// run concurrently subject to `needs`; steps inside one job run in order and
// share the job's harness defaults. That is exactly Actions' semantics, and it
// is the grouping a workflow author reaches for when one logical task takes
// three prompts.
type Job struct {
	ID          string `yaml:"-" json:"id"`
	Name        string `yaml:"name,omitempty" json:"name,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Needs are the jobs that must finish first. Same word, same meaning as
	// Actions.
	Needs []string `yaml:"needs,omitempty" json:"needs,omitempty"`
	// RunsOn is the runner label this job's steps execute on — Actions'
	// `runs-on`. It is a LABEL, matched against the workers that have
	// registered, exactly as Actions matches a runner pool: the workflow says
	// what it needs, not which machine. Empty ⇒ the workflow's `runs-on`, then
	// this process.
	RunsOn string `yaml:"runs-on,omitempty" json:"runsOn,omitempty"`
	// If guards the whole job, in the same shape as a step's `when`.
	If []Guard `yaml:"if,omitempty" json:"if,omitempty"`
	// Defaults are applied to every step in this job that does not set them —
	// Actions' `defaults:`, carrying our harness instead of a shell.
	Defaults *Override `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	// TimeoutSec bounds the whole job; it becomes each step's timeout when the
	// step does not set a shorter one.
	TimeoutSec int `yaml:"timeout_sec,omitempty" json:"timeoutSec,omitempty"`
	// Steps run in order within the job.
	Steps []Step `yaml:"steps" json:"steps"`
	// Uses expands a reusable task into this job's steps.
	Uses []Use `yaml:"uses,omitempty" json:"uses,omitempty"`
	// Consumes / Produces let a job take part in a derived plan.
	Consumes []string `yaml:"consumes,omitempty" json:"consumes,omitempty"`
	Produces []string `yaml:"produces,omitempty" json:"produces,omitempty"`
}

// expandJobs flattens jobs into the engine's step list. Everything downstream —
// planner, scheduler, store, UI, CLI — keeps seeing plain steps, so jobs cost
// nothing below this function.
//
// The flattening rules are the interesting part:
//   - a step's id becomes `job.step`, so two jobs may reuse a step name;
//   - steps inside a job chain sequentially via `needs`;
//   - the job's `needs` attaches to its FIRST step, and other jobs depending on
//     it attach to its LAST step — which is what makes a job behave as one node
//     in the graph while being several underneath;
//   - the job's `if` guard is applied to every step in it, because a job that
//     is skipped must skip whole.
func (d *Definition) expandJobs(tasks map[string]*Task) error {
	if len(d.Jobs) == 0 {
		return nil
	}
	if len(d.Steps) > 0 {
		return fmt.Errorf("%s: a workflow declares `jobs` OR a flat `steps` list, not both", d.Name)
	}
	names, err := jobOrder(d.Jobs)
	if err != nil {
		return fmt.Errorf("%s: %w", d.Name, err)
	}

	last := map[string]string{}   // job id -> its final step id
	firsts := map[string]string{} // job id -> its first step id
	var out []Step

	for _, id := range names {
		job := d.Jobs[id]
		job.ID = id
		steps := job.Steps
		if len(job.Uses) > 0 {
			expanded, err := expandInto(d.Name, id, job.Uses, tasks)
			if err != nil {
				return err
			}
			steps = append(expanded, steps...)
		}
		if len(steps) == 0 {
			return fmt.Errorf("%s: job %q has no steps", d.Name, id)
		}
		for i := range steps {
			s := steps[i]
			if s.ID == "" {
				return fmt.Errorf("%s: job %q has a step with no id", d.Name, id)
			}
			s.JobID = id
			s.ID = id + "." + s.ID
			if job.Defaults != nil {
				applyDefaults(&s, *job.Defaults)
			}
			if job.TimeoutSec > 0 && (s.TimeoutSec == 0 || s.TimeoutSec > job.TimeoutSec) {
				s.TimeoutSec = job.TimeoutSec
			}
			// runs-on cascades workflow → job → step, each level only filling
			// what the level below left empty. Same direction as Actions'
			// `defaults:`, and the same as TimeoutSec above.
			if s.RunsOn == "" {
				s.RunsOn = job.RunsOn
			}
			s.When = append(append([]Guard(nil), job.If...), s.When...)
			if i == 0 {
				firsts[id] = s.ID
			} else {
				s.Needs = append(s.Needs, out[len(out)-1].ID) // chain within the job
			}
			out = append(out, s)
			last[id] = s.ID
		}
		// a job's facts belong to its boundary: consumed by the first step,
		// produced by the last, so the job is one node to the planner
		if len(job.Consumes) > 0 {
			attach(out, firsts[id], func(s *Step) { s.Consumes = addUnique(s.Consumes, job.Consumes) })
		}
		if len(job.Produces) > 0 {
			attach(out, last[id], func(s *Step) { s.Produces = addUnique(s.Produces, job.Produces) })
		}
	}

	// job-level `needs` becomes a step-level edge from the dependency's LAST
	// step to this job's FIRST — one node in, one node out
	for _, id := range names {
		if len(d.Jobs[id].Needs) > 0 {
			d.authoredNeeds = true
		}
		for _, dep := range d.Jobs[id].Needs {
			tail, ok := last[dep]
			if !ok {
				return fmt.Errorf("%s: job %q needs unknown job %q", d.Name, id, dep)
			}
			attach(out, firsts[id], func(s *Step) { s.Needs = append(s.Needs, tail) })
		}
	}
	d.Steps = out
	return nil
}

// jobOrder returns the jobs in DEPENDENCY order, ties broken by name. The
// graph would be correct either way — every edge is explicit — but a reader
// scanning a flattened workflow, the CLI's `workflows show`, and the UI's step
// list all read top to bottom, and a list that contradicts the execution order
// is a small lie told on every screen.
func jobOrder(jobs map[string]*Job) ([]string, error) {
	names := make([]string, 0, len(jobs))
	for id := range jobs {
		names = append(names, id)
	}
	sort.Strings(names)

	const (
		white = 0
		grey  = 1
		black = 2
	)
	colour := map[string]int{}
	var out []string
	var path []string
	var visit func(string) error
	visit = func(id string) error {
		switch colour[id] {
		case grey:
			return fmt.Errorf("job dependency cycle: %s", strings.Join(append(path, id), " → "))
		case black:
			return nil
		}
		colour[id] = grey
		path = append(path, id)
		deps := append([]string(nil), jobs[id].Needs...)
		sort.Strings(deps)
		for _, dep := range deps {
			if _, ok := jobs[dep]; !ok {
				return fmt.Errorf("job %q needs unknown job %q", id, dep)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		colour[id] = black
		out = append(out, id)
		return nil
	}
	for _, id := range names {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func attach(steps []Step, id string, fn func(*Step)) {
	for i := range steps {
		if steps[i].ID == id {
			fn(&steps[i])
			return
		}
	}
}

// applyDefaults fills what a step did not set. Unlike an Override it never
// replaces a step's own choice — defaults are a floor, not an instruction.
func applyDefaults(s *Step, o Override) {
	s.Skills = addUnique(s.Skills, o.AddSkills)
	s.Tools = addUnique(s.Tools, o.AddTools)
	s.MCP = addUnique(s.MCP, o.AddMCP)
	s.Guardrails = append(s.Guardrails, o.AddGuardrails...)
	if s.Provider == "" {
		s.Provider = o.Provider
	}
	if s.Classifier == "" {
		s.Classifier = o.Classifier
	}
	if s.Model == "" {
		s.Model = o.Model
	}
	if s.Soul == "" {
		s.Soul = o.Soul
	}
	if s.Budget == nil {
		s.Budget = o.Budget
	}
}

// expandInto is expandUses for a job, prefixing with the job id.
func expandInto(wf, jobID string, uses []Use, tasks map[string]*Task) ([]Step, error) {
	tmp := &Definition{Name: wf, Uses: uses}
	if err := tmp.expandUses(tasks); err != nil {
		return nil, err
	}
	_ = jobID
	return tmp.Steps, nil
}

var _ = strings.TrimSpace

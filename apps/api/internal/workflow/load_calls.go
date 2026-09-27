package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// A workflow may `use:` another workflow and run its steps as its own. The
// callee may live in the same directory or in ANY other loaded source, so
// loading is two-phase: every source is parsed first, then each workflow is
// expanded against the whole set. Expansion is lazy and memoised, so file and
// source order never matter and a cycle is named instead of overflowing.
//
// A bare `use: name` resolves in this order: a task, a workflow in the
// caller's own source, then the first source that claimed that name (the same
// rule LoadSources applies to short names). `use: source/name` is exact.

type loadGroup struct {
	name  string // the source name; "" for a lone directory
	defs  []*Definition
	tasks map[string]*Task
	err   error
}

// parseDir reads every workflow in dir without expanding or validating it.
func parseDir(dir string) ([]*Definition, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*Definition
	for _, e := range entries {
		// A workflow is either a file or a DIRECTORY holding workflow.yaml plus
		// the files that travel with it (files.go).
		var (
			p     string
			raw   []byte
			files []File
			err   error
		)
		if e.IsDir() {
			raw, p, files, err = loadWorkflowDir(filepath.Join(dir, e.Name()))
			if err != nil {
				return nil, err
			}
			if raw == nil {
				continue // a directory with no workflow.yaml is not ours
			}
		} else {
			ext := filepath.Ext(e.Name())
			if ext != ".yaml" && ext != ".yml" {
				continue
			}
			p = filepath.Join(dir, e.Name())
			raw, err = os.ReadFile(p)
			if err != nil {
				return nil, err
			}
		}
		d := &Definition{}
		if err := yaml.Unmarshal(raw, d); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		d.Path = p
		d.Files = files
		for _, st := range d.Steps {
			if len(st.Needs) > 0 {
				d.authoredNeeds = true
			}
		}
		out = append(out, d)
	}
	return out, nil
}

type expander struct {
	groups   []*loadGroup
	opt      loadOptions
	owner    map[*Definition]*loadGroup
	done     map[*Definition]bool
	visiting map[*Definition]bool
	// uses records which groups a workflow pulled steps from, so a group that
	// is later skipped takes its dependents with it instead of leaving them
	// running steps from a source the platform reports as broken.
	uses map[*Definition]map[*loadGroup]bool
}

func newExpander(groups []*loadGroup, opt loadOptions) *expander {
	x := &expander{
		groups: groups, opt: opt,
		owner:    map[*Definition]*loadGroup{},
		done:     map[*Definition]bool{},
		visiting: map[*Definition]bool{},
		uses:     map[*Definition]map[*loadGroup]bool{},
	}
	for _, g := range groups {
		for _, d := range g.defs {
			x.owner[d] = g
		}
	}
	return x
}

func label(g *loadGroup, d *Definition) string {
	if g.name == "" {
		return d.Name
	}
	return g.name + "/" + d.Name
}

func (x *expander) lookup(from *loadGroup, name string) *Definition {
	find := func(g *loadGroup, n string) *Definition {
		if g.err != nil {
			return nil
		}
		for _, d := range g.defs {
			if d.Name == n {
				return d
			}
		}
		return nil
	}
	if d := find(from, name); d != nil {
		return d
	}
	if src, n, ok := strings.Cut(name, "/"); ok {
		for _, g := range x.groups {
			if g.name == src {
				return find(g, n)
			}
		}
		return nil
	}
	for _, g := range x.groups {
		if d := find(g, name); d != nil {
			return d
		}
	}
	return nil
}

func (x *expander) expand(d *Definition, chain []string) error {
	if x.done[d] {
		return nil
	}
	g := x.owner[d]
	me := label(g, d)
	if x.visiting[d] {
		return fmt.Errorf("%s: workflows use each other in a cycle: %s", me, strings.Join(append(chain, me), " → "))
	}
	x.visiting[d] = true
	defer delete(x.visiting, d)

	o := x.opt
	o.workflow = func(name string) (*Task, bool, error) {
		c := x.lookup(g, name)
		if c == nil {
			return nil, false, nil
		}
		if c == d {
			return nil, true, fmt.Errorf("%s: a workflow cannot use itself", me)
		}
		if !c.On.Allows(TriggerWorkflowCall) {
			return nil, true, fmt.Errorf("%s: use %q names a workflow that does not declare `on: workflow_call` "+
				"(it declares %v) — add workflow_call to its `on:` so it may be called", me, name, c.On.Names())
		}
		if err := x.expand(c, append(chain, me)); err != nil {
			return nil, true, err
		}
		dep := x.uses[d]
		if dep == nil {
			dep = map[*loadGroup]bool{}
			x.uses[d] = dep
		}
		dep[x.owner[c]] = true
		for cg := range x.uses[c] {
			dep[cg] = true
		}
		return workflowAsTask(c), true, nil
	}
	if err := d.expandUses(g.tasks, o); err != nil {
		return err
	}
	if err := d.expandJobs(g.tasks, o); err != nil {
		return err
	}
	x.done[d] = true
	return nil
}

// run expands and validates every group, setting g.err on the first failure
// in a group, then propagates skips to groups that used a skipped one.
func (x *expander) run(cat Catalog) {
	for _, g := range x.groups {
		if g.err != nil {
			continue
		}
		for _, d := range g.defs {
			if err := x.expand(d, nil); err != nil {
				g.err = err
				break
			}
		}
	}
	for _, g := range x.groups {
		if g.err != nil {
			continue
		}
		for _, d := range g.defs {
			normalize(d)
			if err := d.validate(cat); err != nil {
				g.err = err
				break
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, g := range x.groups {
			if g.err != nil {
				continue
			}
			for _, d := range g.defs {
				for dg := range x.uses[d] {
					if dg != g && dg.err != nil {
						g.err = fmt.Errorf("%s: uses a workflow from source %q, which did not load: %v", label(g, d), dg.name, dg.err)
						changed = true
						break
					}
				}
				if g.err != nil {
					break
				}
			}
		}
	}
}

// workflowAsTask presents an expanded workflow as a task, so a caller's
// `use:` walks the same prefix/override/rename path either way. The callee's
// workflow-level env is carried onto its steps, because normalize — which
// would otherwise apply it — runs on the caller, not the callee.
func workflowAsTask(d *Definition) *Task {
	steps := make([]Step, len(d.Steps))
	for i, s := range d.Steps {
		s.Env = MergeEnv(d.Env, s.Env)
		s.Needs = append([]string(nil), s.Needs...)
		s.Skills = append([]string(nil), s.Skills...)
		s.Tools = append([]string(nil), s.Tools...)
		s.MCP = append([]string(nil), s.MCP...)
		s.Guardrails = append([]Guardrail(nil), s.Guardrails...)
		steps[i] = s
	}
	return &Task{Name: d.Name, Description: d.Description, Steps: steps, Path: d.Path}
}

package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// A Task is a REUSABLE BUNDLE OF STEPS — "reproduce a bug", "review a diff" —
// named once and used by many workflows. It is the unit above a step and below
// a workflow.
//
// A workflow uses a task by name and may override what each step is given, so
// the same bundle runs with different skills, tools, MCP servers or provider in
// different workflows. Expansion happens at LOAD time, so everything downstream
// — planner, scheduler, UI, CLI — sees ordinary steps and needs no knowledge of
// tasks at all.
type Task struct {
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Steps       []Step `yaml:"steps" json:"steps"`
	Path        string `yaml:"-" json:"path"`
}

func (t Task) EntryName() string { return t.Name }

// Use is a workflow entry that expands into a task's steps.
type Use struct {
	// Task is the registry name of the task to expand.
	Task string `yaml:"use" json:"use"`
	// As prefixes the expanded step ids (default: the task name), so the same
	// task can be used twice in one workflow without an id collision.
	As string `yaml:"as,omitempty" json:"as,omitempty"`
	// With overrides fields on the expanded steps. A key of "*" applies to every
	// step; otherwise the key is the task's own step id.
	With map[string]Override `yaml:"with,omitempty" json:"with,omitempty"`
	// Consumes / Produces rename the facts at the boundary, so a task written
	// against generic fact names plugs into a workflow's vocabulary.
	Consumes map[string]string `yaml:"consumes,omitempty" json:"consumes,omitempty"`
	Produces map[string]string `yaml:"produces,omitempty" json:"produces,omitempty"`
}

// Override is what a workflow may change about a task's step without editing
// the task. ADD semantics for the lists, so a use can GRANT capability but the
// task keeps what it declared it needs.
type Override struct {
	AddSkills  []string `yaml:"add_skills,omitempty" json:"addSkills,omitempty"`
	AddTools   []string `yaml:"add_tools,omitempty" json:"addTools,omitempty"`
	AddMCP     []string `yaml:"add_mcp,omitempty" json:"addMcp,omitempty"`
	Provider   string   `yaml:"provider,omitempty" json:"provider,omitempty"`
	Classifier string   `yaml:"classifier,omitempty" json:"classifier,omitempty"`
	Model      string   `yaml:"model,omitempty" json:"model,omitempty"`
	Soul       string   `yaml:"soul,omitempty" json:"soul,omitempty"`
	Budget     *Budget  `yaml:"budget,omitempty" json:"budget,omitempty"`
	// AddGuardrails are appended; a use can tighten policy, never loosen it,
	// because first-deny-wins means an added rule can only deny more.
	AddGuardrails    []Guardrail `yaml:"add_guardrails,omitempty" json:"addGuardrails,omitempty"`
	RequiresApproval *bool       `yaml:"requires_approval,omitempty" json:"requiresApproval,omitempty"`
}

// LoadTasks reads every task file in dir. Absent dir ⇒ no tasks.
func LoadTasks(dir string) (map[string]*Task, error) {
	out := map[string]*Task{}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if e.IsDir() || (ext != ".yaml" && ext != ".yml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		t := &Task{}
		if err := yaml.Unmarshal(raw, t); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		t.Path = p
		if t.Name == "" {
			return nil, fmt.Errorf("%s: a task needs a name", p)
		}
		if len(t.Steps) == 0 {
			return nil, fmt.Errorf("%s: task %q has no steps", p, t.Name)
		}
		if _, dup := out[t.Name]; dup {
			return nil, fmt.Errorf("%s: duplicate task %q", p, t.Name)
		}
		out[t.Name] = t
	}
	return out, nil
}

// SortedTasks returns tasks name-sorted.
func SortedTasks(m map[string]*Task) []*Task {
	out := make([]*Task, 0, len(m))
	for _, t := range m {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// expandUses replaces every `use:` entry with the task's steps, applying the
// prefix, the overrides and the fact renames. Called before validation, so a
// task's steps are checked exactly like hand-written ones.
func (d *Definition) expandUses(tasks map[string]*Task, opt loadOptions) error {
	if len(d.Uses) == 0 {
		return nil
	}
	var expanded []Step
	for _, u := range d.Uses {
		var t *Task
		if IsRemoteUse(u.Task) {
			// A REMOTE use resolves here, at load time, and then walks the
			// same path a local task does — the prefix, the overrides, the
			// fact renames, the validation that follows. A remote task that
			// behaved differently from a local one would be a bug.
			rt, err := d.resolveRemote(u.Task, opt)
			if err != nil {
				return err
			}
			t = rt
		} else {
			local, ok := tasks[u.Task]
			if !ok {
				known := make([]string, 0, len(tasks))
				for n := range tasks {
					known = append(known, n)
				}
				sort.Strings(known)
				return fmt.Errorf("%s: unknown task %q — known: %v", d.Name, u.Task, known)
			}
			t = local
		}
		prefix := u.As
		if prefix == "" {
			prefix = t.Name
		}
		for _, s := range t.Steps {
			step := s // copy; a task is used by many workflows
			step.ID = prefix + "." + s.ID
			step.Needs = prefixAll(prefix, s.Needs)
			step.Consumes = renameAll(u.Consumes, s.Consumes)
			step.Produces = renameAll(u.Produces, s.Produces)
			applyOverride(&step, u.With["*"])
			applyOverride(&step, u.With[s.ID])
			expanded = append(expanded, step)
		}
	}
	// Hand-written steps keep their position after the expanded ones; ordering
	// here is cosmetic because the planner and the DAG both derive it.
	d.Steps = append(expanded, d.Steps...)
	return nil
}

// resolveRemote fetches the bundle a remote reference names and records the
// pin. Every failure names the reference and the remote: an unresolvable
// reference is a LOAD error, never a degraded run and never a local task of
// the same trailing name.
func (d *Definition) resolveRemote(raw string, opt loadOptions) (*Task, error) {
	ref, err := ParseRef(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: use: %w", d.Name, err)
	}
	if opt.remote == nil {
		return nil, fmt.Errorf("%s: use %q names the git remote %s, and remote references are not available in this loader",
			d.Name, raw, ref.Remote)
	}
	t, pin, err := opt.remote.ResolveRemoteUse(ref)
	if err != nil {
		return nil, fmt.Errorf("%s: use %q: cannot resolve ref %q from %s: %w", d.Name, raw, ref.Ref, ref.Remote, err)
	}
	d.RemotePins = append(d.RemotePins, pin)
	return t, nil
}

func prefixAll(prefix string, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, prefix+"."+id)
	}
	return out
}

// renameAll maps a task's generic fact names onto the workflow's vocabulary.
// An unmapped fact keeps its own name.
func renameAll(m map[string]string, facts []string) []string {
	if len(facts) == 0 {
		return nil
	}
	out := make([]string, 0, len(facts))
	for _, f := range facts {
		if to, ok := m[f]; ok {
			out = append(out, to)
			continue
		}
		out = append(out, f)
	}
	return out
}

func applyOverride(s *Step, o Override) {
	s.Skills = addUnique(s.Skills, o.AddSkills)
	s.Tools = addUnique(s.Tools, o.AddTools)
	s.MCP = addUnique(s.MCP, o.AddMCP)
	s.Guardrails = append(s.Guardrails, o.AddGuardrails...)
	if o.Provider != "" {
		s.Provider = o.Provider
	}
	if o.Classifier != "" {
		s.Classifier = o.Classifier
	}
	if o.Model != "" {
		s.Model = o.Model
	}
	if o.Soul != "" {
		s.Soul = o.Soul
	}
	if o.Budget != nil {
		s.Budget = o.Budget
	}
	if o.RequiresApproval != nil {
		s.RequiresApproval = *o.RequiresApproval
	}
}

func addUnique(have, add []string) []string {
	if len(add) == 0 {
		return have
	}
	seen := map[string]bool{}
	for _, v := range have {
		seen[v] = true
	}
	for _, v := range add {
		if !seen[v] {
			have = append(have, v)
			seen[v] = true
		}
	}
	return have
}

var _ = strings.TrimSpace

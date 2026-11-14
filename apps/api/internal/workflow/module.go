package workflow

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// A MODULE is a task with a manifest (issue #83, ADR 0028): a directory holding
// `module.yaml`, found anywhere inside an imported registry source (the same
// git import, pin and Sync the skill registry already does). A workflow calls
// it like any task — `uses: name@1` with typed `with:` — and the platform can
// say, before a run, which env vars and secrets it still needs.
//
//	name: apl
//	version: 0.2.0
//	description: ...
//	requires:
//	  env:     [APL_BIN]
//	binary:                  # optional: the CLI it needs, delivered by carry
//	  carry: deemwar/wfx-wa-monitor@0.2.0
//	inputs: { ... }          # optional: the module's OWN command (steps: below,
//	steps:  [ ... ]          #   or task.yaml), called as `uses: apl@0`
//	commands:                # named commands, called as `uses: apl.whatsapp-send@0`
//	  whatsapp-send: { description, inputs, outputs, steps }
//	services:                # supervised processes, one per account (#103)
//	  whatsapp:
//	    command: wfx-wa-monitor
//	    args: [--label, "{{ .Inputs.label }}"]
//	    inputs: { label: { type: string, default: biz } }
//
// A command is reachable by `<module>.<command>`, a service by
// `<module>.<service>`.
//
// A module named like a local task loses: a repository's own tasks always win,
// so importing a registry never changes what an existing name does.

const ModuleFile = "module.yaml"

// ModuleCommandSource names the generated workflows that run a module command
// on its own (`wfx module call`); `uses:` never resolves to them.
const ModuleCommandSource = "modules"

// Module is one parsed manifest.
type Module struct {
	Task     `yaml:",inline"`
	Requires ModuleNeeds          `yaml:"requires,omitempty" json:"requires"`
	Binary   *ModuleBinary        `yaml:"binary,omitempty" json:"binary,omitempty"`
	Commands map[string]Task      `yaml:"commands,omitempty" json:"commands,omitempty"`
	Services map[string]ModuleSvc `yaml:"services,omitempty" json:"services,omitempty"`
	Source   string               `yaml:"-" json:"source,omitempty"`
	Dir      string               `yaml:"-" json:"dir"`
}

// CommandNames is every callable name the module offers: its own (when it has
// steps) and `<module>.<command>` for each named command, sorted.
func (m *Module) CommandNames() []string {
	var out []string
	if len(m.Steps) > 0 {
		out = append(out, m.Name)
	}
	for c := range m.Commands {
		out = append(out, m.Name+"."+c)
	}
	sort.Strings(out)
	return out
}

// Callable is the task behind a callable name, with the module's version.
func (m *Module) Callable(name string) (*Task, bool) {
	var t Task
	if name == m.Name && len(m.Steps) > 0 {
		t = m.Task
	} else if c, ok := strings.CutPrefix(name, m.Name+"."); ok {
		ct, found := m.Commands[c]
		if !found {
			return nil, false
		}
		t = ct
		t.Name = name
	} else {
		return nil, false
	}
	t.Version, t.Module = m.Version, m.Name
	return &t, true
}

// ModuleNeeds is what must be configured once before the module can run.
type ModuleNeeds struct {
	Env     []string `yaml:"env,omitempty" json:"env,omitempty"`
	Secrets []string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
	// CLI are the command-line tools the module wraps: how to check one is
	// there (its stdout is the version) and how to install it on the server.
	CLI []ModuleCLI `yaml:"cli,omitempty" json:"cli,omitempty"`
}

// ModuleCLI is one CLI a module needs.
type ModuleCLI struct {
	Name    string `yaml:"name" json:"name"`
	Check   string `yaml:"check" json:"check"`
	Install string `yaml:"install,omitempty" json:"install,omitempty"`
}

// ModuleBinary names the CLI a module runs. Delivery through carry is a
// follow-up; today the binary must be on PATH (or named by Path).
type ModuleBinary struct {
	Carry string `yaml:"carry,omitempty" json:"carry,omitempty"`
	Path  string `yaml:"path,omitempty" json:"path,omitempty"`
}

// ModuleSvc is how one of a module's services is started.
type ModuleSvc struct {
	Description string               `yaml:"description,omitempty" json:"description,omitempty"`
	Command     string               `yaml:"command" json:"command"`
	Args        []string             `yaml:"args,omitempty" json:"args,omitempty"`
	Inputs      map[string]InputSpec `yaml:"inputs,omitempty" json:"inputs,omitempty"`
}

// LoadModule parses one module directory.
func LoadModule(dir string) (*Module, error) {
	p := filepath.Join(dir, ModuleFile)
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	m := &Module{}
	if err := dec.Decode(m); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	m.Dir, m.Path = dir, p
	if m.Name == "" || m.Version == "" {
		return nil, fmt.Errorf("%s: a module needs a name and a version", p)
	}
	if !safeModuleName(m.Name) {
		return nil, fmt.Errorf("%s: module name %q: letters, digits, dot, dash, underscore", p, m.Name)
	}
	if _, ok := majorOf(m.Version); !ok {
		return nil, fmt.Errorf("%s: version %q must start with a major number (1, 1.2.0, v2)", p, m.Version)
	}
	if len(m.Steps) == 0 {
		if b, err := os.ReadFile(filepath.Join(dir, "task.yaml")); err == nil {
			var t Task
			if err := yaml.Unmarshal(b, &t); err != nil {
				return nil, fmt.Errorf("%s/task.yaml: %w", dir, err)
			}
			m.Steps = t.Steps
		}
	}
	if len(m.Steps) == 0 && len(m.Commands) == 0 && len(m.Services) == 0 {
		return nil, fmt.Errorf("%s: a module needs steps, commands or services", p)
	}
	if err := checkSpecs(p, m.Inputs); err != nil {
		return nil, err
	}
	for c, t := range m.Commands {
		if !safeModuleName(c) || strings.Contains(c, ".") {
			return nil, fmt.Errorf("%s: command name %q: letters, digits, dash, underscore", p, c)
		}
		if len(t.Steps) == 0 {
			return nil, fmt.Errorf("%s: command %s needs steps", p, c)
		}
		if err := checkSpecs(p+" command "+c, t.Inputs); err != nil {
			return nil, err
		}
		if err := writeLoops(t); err != nil {
			return nil, fmt.Errorf("%s: command %s: %w", p, c, err)
		}
	}
	if err := writeLoops(m.Task); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	for n, s := range m.Services {
		if !safeModuleName(n) || strings.Contains(n, ".") {
			return nil, fmt.Errorf("%s: service name %q: letters, digits, dash, underscore", p, n)
		}
		if s.Command == "" {
			return nil, fmt.Errorf("%s: service %s needs a command", p, n)
		}
		if err := checkSpecs(p+" service "+n, s.Inputs); err != nil {
			return nil, err
		}
	}
	for _, n := range append(append([]string(nil), m.Requires.Env...), m.Requires.Secrets...) {
		if !envName(n) {
			return nil, fmt.Errorf("%s: requires %q is not an env var name", p, n)
		}
	}
	return m, nil
}

// FindModules walks root (bounded depth) for module.yaml files. A broken
// module is returned as an error beside the good ones, never fatal.
func FindModules(source, root string) ([]*Module, []error) {
	var mods []*Module
	var errs []error
	base := strings.Count(filepath.Clean(root), string(filepath.Separator))
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" ||
				strings.Count(p, string(filepath.Separator))-base > 4 {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != ModuleFile || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		m, err := LoadModule(filepath.Dir(p))
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		m.Source = source
		mods = append(mods, m)
		return nil
	})
	sort.Slice(mods, func(i, j int) bool { return mods[i].Name < mods[j].Name })
	return mods, errs
}

// WithModules makes modules callable by name (`uses: name@1`). Local tasks
// and workflows of the same name win.
func WithModules(mods []*Module) LoadOption {
	return func(o *loadOptions) {
		if o.modules == nil {
			o.modules = map[string]*Task{}
		}
		for _, m := range mods {
			// A service is configured, not expanded into steps.
			for _, n := range m.CommandNames() {
				if _, dup := o.modules[n]; !dup {
					o.modules[n], _ = m.Callable(n)
				}
			}
		}
	}
}

func safeModuleName(s string) bool {
	for i, r := range s {
		ok := r == '-' || r == '_' || r == '.' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok || (i == 0 && (r == '-' || r == '.')) {
			return false
		}
	}
	return s != ""
}

func envName(s string) bool {
	for i, r := range s {
		ok := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return s != ""
}

// writeLoops refuses a loop (foreach, until, while) in a write command: the
// pause and its pin are taken once, on the command's first step, and a retry
// restarts the call from there. A loop as the first step would repeat past
// the pause, and an iteration retried on its own would skip the restart.
func writeLoops(t Task) error {
	if t.Effect != "write" {
		return nil
	}
	var walk func(steps []Step) error
	walk = func(steps []Step) error {
		for i := range steps {
			if steps[i].IsLoop() {
				return fmt.Errorf("step %s loops (foreach, until or while); a write command's steps may not loop — call the command once per item instead", steps[i].ID)
			}
			if err := walk(steps[i].Steps); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(t.Steps)
}

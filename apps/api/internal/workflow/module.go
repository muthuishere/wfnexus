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
//	name: whatsapp-group-monitor
//	version: 1.0.0
//	kind: service            # command (default): steps run in a workflow
//	                         # service: a long-running process (service: below)
//	description: ...
//	inputs:  { ... }         # same InputSpec as a task
//	outputs: { ... }
//	requires:
//	  env:     [APL_BIN]
//	  secrets: [WFX_HOOK_TOKEN]
//	binary:                  # optional: the CLI it needs, delivered by carry
//	  carry: deemwar/wa-monitor@1.0.0
//	service:                 # kind: service only
//	  command: wfx-wa-monitor
//	  args: [--interval, 30s]
//	steps: [ ... ]           # the task; or put them in task.yaml beside it
//
// A module named like a local task loses: a repository's own tasks always win,
// so importing a registry never changes what an existing name does.

const ModuleFile = "module.yaml"

// Module is one parsed manifest.
type Module struct {
	Task     `yaml:",inline"`
	Kind     string        `yaml:"kind,omitempty" json:"kind"`
	Requires ModuleNeeds   `yaml:"requires,omitempty" json:"requires"`
	Binary   *ModuleBinary `yaml:"binary,omitempty" json:"binary,omitempty"`
	Service  *ModuleSvc    `yaml:"service,omitempty" json:"service,omitempty"`
	Source   string        `yaml:"-" json:"source,omitempty"`
	Dir      string        `yaml:"-" json:"dir"`
}

// ModuleNeeds is what must be configured once before the module can run.
type ModuleNeeds struct {
	Env     []string `yaml:"env,omitempty" json:"env,omitempty"`
	Secrets []string `yaml:"secrets,omitempty" json:"secrets,omitempty"`
}

// ModuleBinary names the CLI a module runs. Delivery through carry is a
// follow-up; today the binary must be on PATH (or named by Path).
type ModuleBinary struct {
	Carry string `yaml:"carry,omitempty" json:"carry,omitempty"`
	Path  string `yaml:"path,omitempty" json:"path,omitempty"`
}

// ModuleSvc is how a service module's process is started.
type ModuleSvc struct {
	Command string   `yaml:"command" json:"command"`
	Args    []string `yaml:"args,omitempty" json:"args,omitempty"`
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
	switch m.Kind {
	case "":
		m.Kind = "command"
	case "command", "service":
	default:
		return nil, fmt.Errorf("%s: kind %q is command or service", p, m.Kind)
	}
	if m.Kind == "service" && (m.Service == nil || m.Service.Command == "") {
		return nil, fmt.Errorf("%s: a service module needs service.command", p)
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
	if m.Kind == "command" && len(m.Steps) == 0 {
		return nil, fmt.Errorf("%s: a command module needs steps (in module.yaml or task.yaml)", p)
	}
	if err := checkSpecs(p, m.Inputs); err != nil {
		return nil, err
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
			if m.Kind != "command" {
				continue // a service is configured, not expanded into steps
			}
			if _, dup := o.modules[m.Name]; !dup {
				t := m.Task
				o.modules[m.Name] = &t
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

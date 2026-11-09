package engine

import (
	"context"
	"os"
	"sort"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Modules (ADR 0028): a module.yaml anywhere in an imported registry source
// is a module. Install, pin and Sync are the skill sources' own (skillsources.go)
// — a module rides the same git checkout, so there is no second registry.

// ModuleView is a module plus what it still needs configured.
type ModuleView struct {
	*workflow.Module
	// Missing are required env vars / secrets set neither in the platform's
	// system env (`wfx env`) nor in the server's process env.
	Missing []string `json:"missing"`
	Ready   bool     `json:"ready"`
	// Services are this module's supervised instances, one per account
	// (services.go). Empty for a command module.
	Services []ServiceView `json:"instances"`
	// CLIs are the required CLIs as checked on this server just now.
	CLIs []CLIStatus `json:"clis"`
}

// Modules scans every skill source checkout. Broken manifests are returned as
// errors beside the good modules.
func (e *Engine) Modules() ([]*workflow.Module, []error) {
	list, err := e.readSkillSources()
	if err != nil {
		return nil, []error{err}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	var mods []*workflow.Module
	var errs []error
	seen := map[string]bool{}
	// The modules the platform ships (apl) are preinstalled and win a name.
	if e.cfg.ModulesDir != "" {
		ms, es := workflow.FindModules("builtin", e.cfg.ModulesDir)
		errs = append(errs, es...)
		for _, m := range ms {
			seen[m.Name] = true
			mods = append(mods, m)
		}
	}
	for _, s := range list {
		ms, es := workflow.FindModules(s.Name, e.skillSourceDir(s.Name))
		errs = append(errs, es...)
		for _, m := range ms {
			if seen[m.Name] {
				continue // first source (by name) keeps a module name
			}
			seen[m.Name] = true
			mods = append(mods, m)
		}
	}
	return mods, errs
}

// ModuleViews is Modules with the configuration check.
func (e *Engine) ModuleViews(ctx context.Context) ([]ModuleView, []string) {
	mods, errs := e.Modules()
	have := map[string]bool{}
	if vars, err := e.ListEnvVars(ctx, store.ScopeSystem, ""); err == nil {
		for _, v := range vars {
			have[v.Key] = true
		}
	}
	svcs, _ := e.Services(ctx)
	out := make([]ModuleView, 0, len(mods))
	for _, m := range mods {
		v := ModuleView{Module: m, Missing: []string{}, Services: []ServiceView{}}
		for _, sv := range svcs {
			if strings.HasPrefix(sv.Module, m.Name+".") {
				v.Services = append(v.Services, sv)
			}
		}
		for _, n := range append(append([]string(nil), m.Requires.Env...), m.Requires.Secrets...) {
			if !have[n] && os.Getenv(n) == "" {
				v.Missing = append(v.Missing, n)
			}
		}
		v.CLIs = e.ModuleCLIs(ctx, v)
		v.Ready = len(v.Missing) == 0
		for _, c := range v.CLIs {
			v.Ready = v.Ready && c.Installed
		}
		out = append(out, v)
	}
	msgs := make([]string, 0, len(errs))
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	return out, msgs
}

func (e *Engine) moduleLoadOption() workflow.LoadOption {
	mods, _ := e.Modules()
	return workflow.WithModules(mods)
}

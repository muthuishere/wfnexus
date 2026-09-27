package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/devinadapter"
)

// Adapter is one agent wfnexus can drive over ACP, and whether this machine
// has it.
type Adapter struct {
	Name      string   `json:"name"`
	Bin       string   `json:"bin"`
	Argv      []string `json:"argv"`
	ModelFlag string   `json:"modelFlag,omitempty"`
	Mode      string   `json:"mode"`
	Install   string   `json:"install"`
	Installed bool     `json:"installed"`
	Path      string   `json:"path,omitempty"`
	// Providers are the registry entries that use this adapter.
	Providers []string `json:"providers"`
}

// Adapters lists every ACP adapter, sorted by name.
func (e *Engine) Adapters() []Adapter {
	var out []Adapter
	for name, pr := range devinadapter.ACPPresets {
		a := Adapter{Name: name, Bin: pr.Bin, Argv: pr.Argv, ModelFlag: pr.ModelFlag, Mode: pr.Mode, Install: pr.Install, Providers: []string{}}
		if path, err := exec.LookPath(pr.Bin); err == nil {
			a.Installed, a.Path = true, path
		}
		for _, p := range e.catalog.Providers.List() {
			preset := p.Preset
			if preset == "" && len(p.Command) == 0 {
				preset = "devin"
			}
			if p.Kind == catalog.KindACP && preset == name {
				a.Providers = append(a.Providers, p.Name)
			}
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ProviderModels is what an acp provider's agent offers, asked live: the agent
// is started, a session opened, the advertised models read, and the process
// stopped. Current is the model a step on this provider would run on.
type ProviderModels struct {
	Provider string                  `json:"provider"`
	Current  string                  `json:"current"`
	Models   []devinadapter.ACPModel `json:"models"`
	// Configured is the provider's `model:`; Offered says whether the agent
	// lists it, so a step's failure to select it is visible before any run.
	Configured string `json:"configured,omitempty"`
	Offered    bool   `json:"offered"`
}

func (e *Engine) ProviderModels(ctx context.Context, name string) (ProviderModels, error) {
	p, ok := e.catalog.Providers.Get(name)
	if !ok {
		return ProviderModels{}, fmt.Errorf("no provider named %q", name)
	}
	if p.Kind != catalog.KindACP {
		return ProviderModels{}, fmt.Errorf("%s is a %s provider; only acp agents are asked for their models (an http provider's model is its `model:`)", name, p.Kind)
	}
	sys, _ := e.platformEnv(ctx, "")
	var env []string
	for k, v := range sys {
		env = append(env, k+"="+v)
	}
	dir, err := os.MkdirTemp("", "wfx-models-")
	if err != nil {
		return ProviderModels{}, err
	}
	defer os.RemoveAll(dir)
	// Listing never selects: a configured model the agent lacks must be
	// reported, not turned into a failure to list.
	a := devinadapter.NewACP(acpConfig(p, "", dir, env))
	defer a.Close()
	ms, cur, err := a.Models(ctx, dir)
	if err != nil {
		return ProviderModels{}, err
	}
	if ms == nil {
		ms = []devinadapter.ACPModel{}
	}
	out := ProviderModels{Provider: name, Current: cur, Models: ms, Configured: p.Model}
	for _, m := range ms {
		if m.ID == p.Model {
			out.Offered = true
		}
	}
	return out, nil
}

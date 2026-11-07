package engine

import (
	"fmt"
	"sort"

	"github.com/muthuishere/wfnexus/apps/api/internal/bundle"
	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
)

// preferredAgents is the order a default is picked in when the operator named
// none: claude first, codex second, then every other agent CLI or ACP entry
// the registry knows, by name. Only local agent programs qualify — a default
// never resolves to an API endpoint that needs a key.
var preferredAgents = []string{"claude-cli", "codex-cli"}

// pickDefaultProvider sets the default provider to the first agent CLI that is
// on this machine, when config said nothing about a default model.
func (e *Engine) pickDefaultProvider() {
	if !e.cfg.AutoProvider || e.cfg.DefaultProvider != "" {
		return
	}
	if name := e.firstAvailableAgent(); name != "" {
		e.cfg.DefaultProvider, e.autoProvider = name, name
	}
}

func (e *Engine) firstAvailableAgent() string {
	var rest []string
	for _, p := range e.Catalog().Providers.List() {
		if p.Kind != catalog.KindCLI && p.Kind != catalog.KindACP {
			continue
		}
		if p.Name != preferredAgents[0] && p.Name != preferredAgents[1] {
			rest = append(rest, p.Name)
		}
	}
	sort.Strings(rest)
	for _, n := range append(append([]string{}, preferredAgents...), rest...) {
		p, ok := e.Catalog().Providers.Get(n)
		if !ok {
			continue
		}
		if c := checkProviderWith(p, nil); c.Ready || c.State == bundle.StatePresent {
			return n
		}
	}
	return ""
}

// AutoProvider is the agent CLI the engine picked as its default, "" if none.
func (e *Engine) AutoProvider() string { return e.autoProvider }

// errNoAgentCLI is what a step with no provider gets when no default was named
// and no agent CLI is installed. It is refused, never sent to an API.
func errNoAgentCLI() error {
	return fmt.Errorf("no agent CLI is available on this machine (claude, codex, opencode, …): " +
		"install and log in to one, or name a provider — wfnexus never falls back to an API key")
}

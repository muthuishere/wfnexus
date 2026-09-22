package engine

import (
	"fmt"
	"os"

	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// resolveLLM turns a step's `provider:` — a NAME in the registry (ADR 0011) —
// into the options the agent actually runs against.
//
// It exists because the name was previously validated at load time and then
// dropped on the floor: the engine read only `model:` and every step ran on the
// process-wide default. A step saying `provider: devin` was accepted, reported
// as accepted, and silently executed on Sonnet. That is the failure ADR 0004
// names in another domain — a parameter that expresses intent is not a control
// — and the fix is the same one: make the name select something, or refuse.
//
// The returned label is what the run log and the UI show, so an operator can
// see which provider a step actually used rather than which one it asked for.
func (e *Engine) resolveLLM(step *workflow.Step) (*agents.LLMOptions, string, error) {
	// No provider named: the process-wide default, with `model:` as an override
	// of the model id only. This is the path every workflow used before
	// providers existed and it keeps working unchanged.
	if step.Provider == "" {
		model := step.Model
		if model == "" {
			model = e.cfg.Model
		}
		return &agents.LLMOptions{
			BaseURL: e.cfg.LLMBaseURL,
			Style:   tn.ClientStyle(e.cfg.LLMStyle),
			Model:   model,
			APIKey:  os.Getenv(e.cfg.LLMAPIKeyEnv),
		}, model, nil
	}

	p, err := e.catalog.Providers.Require(step.Provider)
	if err != nil {
		return nil, "", err
	}

	switch p.Kind {
	case catalog.KindHTTP:
		model := p.Model
		// `model:` beside a provider selects a model WITHIN that provider — a
		// step may want haiku's endpoint with a different model id — and does
		// not reach past it to the default endpoint.
		if step.Model != "" {
			model = step.Model
		}
		key := os.Getenv(p.APIKeyEnv)
		if key == "" {
			// Named, not held. Said plainly here rather than discovered as a
			// 401 twenty turns in. The variable's NAME is safe to print; its
			// value never appears anywhere.
			return nil, "", fmt.Errorf("provider %q needs %s and it is not set in this process's environment",
				p.Name, p.APIKeyEnv)
		}
		return &agents.LLMOptions{
			BaseURL: p.BaseURL,
			Style:   tn.ClientStyle(p.Style),
			Model:   model,
			APIKey:  key,
		}, p.Name + "/" + model, nil

	case catalog.KindCLI, catalog.KindACP:
		// The adapter that drives a local agent CLI as the model is written and
		// tested (internal/devinadapter). It reaches the agent runtime through
		// agents.Options.Transport, which needs toolnexus to export its
		// in-process round tripper — present on the upstream branch (issue #95),
		// absent from the pinned v0.18.1 (ADR 0007), which is why the package
		// is behind the `toolnexus_inprocess` build tag.
		//
		// This refuses rather than falling back to the default provider. A
		// silent fallback is precisely the bug this file was written to fix.
		return nil, "", fmt.Errorf("provider %q is kind %q, which needs toolnexus to export InProcessTransport "+
			"(upstream #95, not in the pinned v0.18.1); the step is refused rather than run on a different model",
			p.Name, p.Kind)
	}
	return nil, "", fmt.Errorf("provider %q has unknown kind %q", p.Name, p.Kind)
}

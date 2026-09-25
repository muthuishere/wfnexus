package engine

import (
	"fmt"
	"net/http"
	"os"
	"time"

	tn "github.com/muthuishere/toolnexus/golang"
	"github.com/muthuishere/toolnexus/golang/agents"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/devinadapter"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// resolved is everything a step needs to reach its model.
//
// Transport is non-nil only for a local-process provider (`cli` / `acp`), where
// the "endpoint" is a program on this machine rather than a URL. Close releases
// it — an ACP provider holds a long-lived child process — and is never nil.
type resolved struct {
	LLM       *agents.LLMOptions
	Transport http.RoundTripper
	Label     string
	Close     func()
	// Price is the registry entry's per-million token price, and whether one
	// is known at all (ADR 0020). The zero value means unknown.
	Price pricing
}

func noClose() {}

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
// resolveLLM uses the step's own env block. Callers that have the whole
// cascade — the engine, about to run a step — use resolveLLMWithEnv instead.
func (e *Engine) resolveLLM(step *workflow.Step, workdir string) (resolved, error) {
	return e.resolveLLMWithEnv(step, workdir, step.Env)
}

func (e *Engine) resolveLLMWithEnv(step *workflow.Step, workdir string, stepEnv map[string]string) (resolved, error) {
	// No provider named: the process-wide default, with `model:` as an override
	// of the model id only. This is the path every workflow used before
	// providers existed and it keeps working unchanged.
	if step.Provider == "" {
		model := step.Model
		if model == "" {
			model = e.cfg.Model
		}
		return resolved{LLM: &agents.LLMOptions{
			BaseURL: e.cfg.LLMBaseURL,
			Style:   tn.ClientStyle(e.cfg.LLMStyle),
			Model:   model,
			APIKey:  os.Getenv(e.cfg.LLMAPIKeyEnv),
		}, Label: model, Close: noClose}, nil
	}

	p, err := e.catalog.Providers.Require(step.Provider)
	if err != nil {
		return resolved{}, err
	}

	switch p.Kind {
	case catalog.KindMock:
		// An in-process endpoint on loopback. It speaks the openai wire shape
		// because that is what the agent loop already sends, so the mock exercises
		// the SAME code path a paid provider does — including the tool-call
		// plumbing that submit_output rides on. A mock wired in further up would
		// skip exactly the part worth testing.
		url, err := e.mock.URL()
		if err != nil {
			return resolved{}, err
		}
		return resolved{LLM: &agents.LLMOptions{
			BaseURL: url,
			Style:   tn.ClientStyle("openai"),
			Model:   "mock",
		}, Label: "mock", Close: noClose}, nil
	case catalog.KindHTTP:
		model := p.Model
		// `model:` beside a provider selects a model WITHIN that provider — a
		// step may want haiku's endpoint with a different model id — and does
		// not reach past it to the default endpoint.
		if step.Model != "" {
			model = step.Model
		}
		// NO apiKeyEnv MEANS NO KEY IS NEEDED, and that is the self-hosted
		// case, not an oversight: an Ollama, vLLM, LM Studio or llama.cpp
		// endpoint on your own network authenticates nobody. `apiKeyEnv` is
		// already `omitempty` in the catalog, so an entry that omits it is
		// well-formed — requiring one anyway failed those providers at boot
		// with "needs  and it is not set", naming nothing, and took the
		// bring-your-own-model path down with it. That path is the whole
		// enterprise argument: the model is theirs and the data never leaves.
		var key string
		if p.APIKeyEnv != "" {
			// The key may come from the platform's env store as well as this
			// process's environment: a system-scope entry is exactly how an
			// operator supplies one without putting it in the unit file.
			key = stepEnv[p.APIKeyEnv]
			if key == "" {
				key = os.Getenv(p.APIKeyEnv)
			}
		}
		if p.APIKeyEnv != "" && key == "" {
			// Named, not held. Said plainly here rather than discovered as a
			// 401 twenty turns in. The variable's NAME is safe to print; its
			// value never appears anywhere.
			return resolved{}, fmt.Errorf("provider %q needs %s and it is not set in this process's environment",
				p.Name, p.APIKeyEnv)
		}
		return resolved{LLM: &agents.LLMOptions{
			BaseURL: p.BaseURL,
			Style:   tn.ClientStyle(p.Style),
			Model:   model,
			APIKey:  key,
		}, Label: p.Name + "/" + model, Close: noClose, Price: priceOf(p)}, nil

	case catalog.KindCLI, catalog.KindACP:
		// The step's env reaches the CLI as well. An agent CLI is a program on
		// this machine, and a skill that tells it to call an API needs that
		// API's token in the process that actually makes the call.
		env, err := workflow.ResolveEnv(stepEnv)
		if err != nil {
			return resolved{}, fmt.Errorf("step %s: %w", step.ID, err)
		}
		return localProvider(p, step.Model, workdir, env)
	}
	return resolved{}, fmt.Errorf("provider %q has unknown kind %q", p.Name, p.Kind)
}

// localProvider resolves a provider whose model is a PROGRAM ON THIS MACHINE:
// a coding-agent CLI (`kind: cli`) or one speaking the Agent Client Protocol
// (`kind: acp`).
//
// The agent still runs toolnexus's own loop — the tools, the skills, the
// guardrails, the completion gate and the typed hand-off are all unchanged and
// none of them know the difference. Only the turn is produced differently: the
// assembled request goes to the process and its reply comes back as the
// assistant message, through toolnexus's in-process round tripper
// (v0.19.0 exports it; before that this needed a hand-copied shadow of an
// unexported upstream file).
//
// A step therefore runs on Devin or a local `claude` by naming it, with no API
// key: the credential is the one the CLI already holds.
func localProvider(p catalog.Provider, stepModel, workdir string, env []string) (resolved, error) {
	model := p.Model
	if stepModel != "" {
		model = stepModel
	}

	agent, closeAgent, err := localAgent(p, model, workdir, env)
	if err != nil {
		return resolved{}, err
	}
	ad := devinadapter.New(devinadapter.Options{
		Agent:   agent,
		Model:   model,
		Workdir: workdir,
		Timeout: time.Duration(p.TimeoutSec) * time.Second,
		Repairs: p.Repairs,
	})

	// The base URL is a sentinel and is never dialled; the round tripper
	// answers before the network is reached. The key is a sentinel too, because
	// the client resolves one from the environment and fails when it finds
	// none — there is no endpoint here to authenticate to.
	baseURL, apiKey, label := ad.AgentsLLM()
	return resolved{
		LLM:       &agents.LLMOptions{BaseURL: baseURL, APIKey: apiKey, Model: label, Style: tn.StyleOpenAI},
		Transport: ad.Transport(),
		Label:     p.Name + "/" + label,
		Close:     closeAgent,
		Price:     priceOf(p),
	}, nil
}

// localAgent builds the backend that executes one turn: a persistent ACP
// process, or a fresh one-shot command per turn.
func localAgent(p catalog.Provider, model, workdir string, env []string) (devinadapter.Agent, func(), error) {
	// KIND FIRST, then command. `kind: acp` says the process speaks the Agent
	// Client Protocol; the command only says which binary to start. Testing
	// the command first meant an `acp` entry that named its binary was run as
	// a one-shot `bin <prompt>` and never spoke ACP at all — the wrong backend,
	// silently, with the entry's own `kind` ignored. An acp command is
	// therefore argv (bin + args), never a prompt template.
	if p.Kind == catalog.KindACP {
		// One ACP process is one conversation, so it is per-step, not shared:
		// two steps on one session would interleave into the same transcript.
		bin, extra := p.Preset, append([]string{}, p.Args...)
		if len(p.Command) > 0 {
			bin, extra = p.Command[0], append(append([]string{}, p.Command[1:]...), p.Args...)
		}
		// "devin" is the adapter's own default binary, and NewACP spells it
		// itself; an empty preset means the same thing.
		if bin == "devin" {
			bin = ""
		}
		a := devinadapter.NewACP(devinadapter.ACP{
			Bin: bin, Model: model, Cwd: workdir, ExtraArgs: extra,
			StartTimeout: time.Duration(p.TimeoutSec) * time.Second,
		})
		return a, func() { _ = a.Close() }, nil
	}

	// An explicit command in the registry wins over a preset: presets are
	// conveniences, and the generic argv template is what makes any agent CLI
	// usable without this package learning its name.
	if len(p.Command) > 0 {
		return &devinadapter.CommandAgent{
			Label: p.Name,
			Env:   env,
			Bin:   p.Command[0],
			Args:  append(append([]string{}, p.Command[1:]...), p.Args...),
			// Without this the model the step asked for never reaches the CLI,
			// and a CLI whose own default is broken fails for a reason that
			// looks nothing like the cause: `opencode run` with no -m returns
			// "Unexpected server error".
			ModelFlag: p.ModelFlag,
		}, noClose, nil
	}

	cli := devinadapter.CLI{Model: model}
	// A preset builds its own CommandAgent, so the step's env is layered on
	// after: the preset decides the argv, the step decides the environment.
	withEnv := func(a *devinadapter.CommandAgent) (devinadapter.Agent, func(), error) {
		a.Env = append(append([]string{}, a.Env...), env...)
		return a, noClose, nil
	}
	switch p.Preset {
	case "devin", "":
		return withEnv(devinadapter.Devin(cli))
	case "claude":
		return withEnv(devinadapter.Claude(cli))
	case "copilot":
		return withEnv(devinadapter.Copilot(cli))
	}
	// Refused rather than guessed. A wrong argv template fails as a parse error
	// twenty turns in, which is the worst place to learn the name was unknown.
	return nil, nil, fmt.Errorf("provider %q: no preset named %q — presets are devin, claude and copilot; "+
		"for any other CLI give an explicit `command` in the registry", p.Name, p.Preset)
}

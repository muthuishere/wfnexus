// Package catalog holds everything a workflow step can name, in the one
// registry type: providers, classifiers and MCP servers.
//
// A step names an entry; the entry is defined once, here. That is what makes a
// workflow portable — the names travel between machines, the endpoints and
// credentials do not. Credentials are never values in this file: a provider
// names the ENV VAR that holds its key.
package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/registry"
)

// ProviderKind is how a provider produces a turn.
type ProviderKind string

const (
	// KindHTTP is an OpenAI- or Anthropic-style endpoint.
	KindHTTP ProviderKind = "http"
	// KindCLI drives a coding agent's command line (devin, claude, copilot,
	// codex, opencode) as the model.
	KindCLI ProviderKind = "cli"
	// KindACP speaks the Agent Client Protocol to an agent process.
	KindACP ProviderKind = "acp"
)

// Provider is a source of model turns.
type Provider struct {
	// Name is the map KEY on disk, and is filled in by Load. omitempty matters:
	// a rewrite re-marshals every entry, and without it a single edit stamps
	// `"name": ""` onto all of them.
	Name        string       `json:"name,omitempty"`
	Kind        ProviderKind `json:"kind"`
	Description string       `json:"description,omitempty"`

	// http
	BaseURL string `json:"baseUrl,omitempty"`
	Style   string `json:"style,omitempty"` // openai | anthropic
	Model   string `json:"model,omitempty"`
	// APIKeyEnv is the NAME of the env var holding the key, never the key.
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`

	// cli / acp
	Preset string `json:"preset,omitempty"` // devin | claude | copilot
	// Command is the argv template for any CLI without a preset. It MUST carry
	// a placeholder for the prompt — `{{prompt}}` (argv) or `{{file}}` (a file,
	// which has no length limit and no quoting hazard, so prefer it where the
	// CLI can read one). Without one the command runs with no prompt at all.
	Command []string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// ModelFlag is the CLI's model flag, e.g. "--model". When set, it and the
	// model are appended. Needed because a CLI's own default model may not
	// work: `opencode run` with no -m returns a server error.
	ModelFlag string `json:"modelFlag,omitempty"`
	// Repairs is how many extra attempts a turn gets when the reply fails
	// validation; the bad output and the parse error go back so it can correct
	// itself. Never a silent fallback.
	Repairs    int `json:"repairs,omitempty"`
	TimeoutSec int `json:"timeoutSec,omitempty"`

	// Price, per MILLION tokens, in USD (ADR 0020). Pointers because the
	// three states are distinct: unset on an `http` provider means the price
	// is UNKNOWN and cost must render as such, while a `cli` / `acp` entry
	// bills no tokens and its cost is a known $0.00. An explicit 0 here is
	// also a known zero — that is what a self-hosted OpenAI-style endpoint is.
	PricePerMIn  *float64 `json:"pricePerMIn,omitempty"`
	PricePerMOut *float64 `json:"pricePerMOut,omitempty"`
}

func (p Provider) EntryName() string { return p.Name }

// Classifier is a judge backend: the cheap, typed decision tier.
type Classifier struct {
	// Name is the map KEY on disk; see Provider.Name on omitempty.
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// Backend: typesafe | openrouter | llm | static. typesafe and openrouter set
	// base, model and key env as a unit — assembling them by hand is how a
	// caller ends up with TypeSafe's model spelling against OpenRouter's base.
	Backend   string `json:"backend"`
	BaseURL   string `json:"baseUrl,omitempty"`
	Model     string `json:"model,omitempty"`
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
}

func (c Classifier) EntryName() string { return c.Name }

// McpServer is one entry of an mcp.json `mcpServers` block.
type McpServer struct {
	// Name is the map KEY on disk; see Provider.Name on omitempty.
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Type        string            `json:"type,omitempty"` // local | remote
	Command     []string          `json:"command,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
}

func (m McpServer) EntryName() string { return m.Name }

// Disabled reports an explicitly switched-off server.
func (m McpServer) Disabled() bool { return m.Enabled != nil && !*m.Enabled }

// Notifier is a place a PAUSE is announced (ADR 0021): a run stopped and needs
// a person. It is a registry entry rather than Go for the same reason a
// provider is — Slack, Telegram or an operator's own system is an endpoint,
// not a package we ship.
//
// It carries a POINTER to the pause and never a way to resolve it: see the
// notify package's doc comment.
type Notifier struct {
	// Name is the map KEY on disk; see Provider.Name on omitempty.
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	// Kind is `webhook` — the one generic adapter. A second kind should be a
	// very good argument, not a convenience.
	Kind    string            `json:"kind"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// SecretEnv is the NAME of an env var holding a bearer token for the
	// RECEIVER, never the token.
	SecretEnv string `json:"secretEnv,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

func (n Notifier) EntryName() string { return n.Name }

// Disabled reports an explicitly switched-off notifier.
func (n Notifier) Disabled() bool { return n.Enabled != nil && !*n.Enabled }

// Catalog is the whole set, one registry per kind.
type Catalog struct {
	Providers   *registry.Registry[Provider]
	Classifiers *registry.Registry[Classifier]
	Mcp         *registry.Registry[McpServer]
	Notifiers   *registry.Registry[Notifier]
}

// file is the on-disk shape of registries.json.
type file struct {
	Providers   map[string]Provider   `json:"providers"`
	Classifiers map[string]Classifier `json:"classifiers"`
	// McpServers matches mcp.json's own key, so one file can serve both.
	McpServers map[string]McpServer `json:"mcpServers"`
	Notifiers  map[string]Notifier  `json:"notifiers"`
}

// Load reads registries.json and merges mcp.json's servers. Neither file is
// required: an absent file contributes nothing rather than failing the boot,
// because a workflow that names nothing needs neither.
func Load(registriesPath, mcpPath string) (*Catalog, error) {
	c := &Catalog{
		Providers:   registry.New[Provider]("provider"),
		Classifiers: registry.New[Classifier]("classifier"),
		Mcp:         registry.New[McpServer]("mcp server"),
		Notifiers:   registry.New[Notifier]("notifier"),
	}
	main, err := readFile(registriesPath)
	if err != nil {
		return nil, err
	}
	for name, p := range main.Providers {
		p.Name = name
		if err := validateProvider(p); err != nil {
			c.Providers.Skipped(registriesPath+"#"+name, err.Error())
			continue
		}
		c.Providers.Add(p, registriesPath+"#"+name)
	}
	for name, cl := range main.Classifiers {
		cl.Name = name
		if err := validateClassifier(cl); err != nil {
			c.Classifiers.Skipped(registriesPath+"#"+name, err.Error())
			continue
		}
		c.Classifiers.Add(cl, registriesPath+"#"+name)
	}
	addMcp := func(src string, servers map[string]McpServer) {
		for name, m := range servers {
			m.Name = name
			if m.Disabled() {
				c.Mcp.Skipped(src+"#"+name, "disabled")
				continue
			}
			c.Mcp.Add(m, src+"#"+name)
		}
	}
	for name, n := range main.Notifiers {
		n.Name = name
		if n.Disabled() {
			c.Notifiers.Skipped(registriesPath+"#"+name, "disabled")
			continue
		}
		if err := validateNotifier(n); err != nil {
			c.Notifiers.Skipped(registriesPath+"#"+name, err.Error())
			continue
		}
		c.Notifiers.Add(n, registriesPath+"#"+name)
	}
	addMcp(registriesPath, main.McpServers)
	if mcpPath != "" && filepath.Clean(mcpPath) != filepath.Clean(registriesPath) {
		side, err := readFile(mcpPath)
		if err != nil {
			return nil, err
		}
		addMcp(mcpPath, side.McpServers)
	}
	return c, nil
}

func readFile(path string) (file, error) {
	var f file
	if path == "" {
		return f, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	return f, nil
}

// httpStyles is the set of wire formats an http provider may name. It is not a
// taste list: it is exactly the set toolnexus's client implements
// (tn.StyleOpenAI / tn.StyleAnthropic in client.go), because the engine passes
// the string straight through as tn.ClientStyle and the client only ever tests
// it against StyleAnthropic. So an unrecognised style is not rejected
// downstream — it is silently framed as OpenAI, which is the quiet wrong-answer
// failure ADR 0016 refuses for presets. Same rule here: refuse the name rather
// than guess it.
//
// "gemini" is deliberately ABSENT. toolnexus exports ToGemini, but that maps
// TOOL SCHEMAS only; there is no Gemini ClientStyle, so nothing would speak
// generateContent. Google's own OpenAI-compatible endpoint is how a Gemini
// model is reached from here, with style "openai".
var httpStyles = map[string]bool{"openai": true, "anthropic": true}

// HTTPStyles lists the accepted styles, sorted, for error messages and the UI.
func HTTPStyles() []string {
	out := make([]string, 0, len(httpStyles))
	for s := range httpStyles {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func validateProvider(p Provider) error {
	switch p.Kind {
	case KindHTTP:
		if p.BaseURL == "" || p.Model == "" {
			return fmt.Errorf("an http provider needs baseUrl and model")
		}
		if !httpStyles[p.Style] {
			return fmt.Errorf("style must be one of %s, got %q", strings.Join(HTTPStyles(), ", "), p.Style)
		}
	case KindCLI, KindACP:
		if p.Preset == "" && len(p.Command) == 0 {
			return fmt.Errorf("a %s provider needs a preset or a command", p.Kind)
		}
		// An explicit command must say WHERE the prompt goes. Without a
		// placeholder the CLI is invoked with no prompt at all, gets a server
		// error or an empty answer, and the failure looks like the model's
		// rather than the registry's. Caught by running `opencode run` for
		// real with exactly this mistake in the entry.
		joined := strings.Join(append(append([]string{}, p.Command...), p.Args...), " ")
		hasPlaceholder := strings.Contains(joined, "{{prompt}}") || strings.Contains(joined, "{{file}}")
		if len(p.Command) > 0 && p.Kind == KindCLI && !hasPlaceholder {
			return fmt.Errorf("the command needs {{prompt}} or {{file}} to say where the prompt goes; got %q", joined)
		}
		// The mirror image for acp. An ACP command is argv for a process that
		// receives its prompts over the protocol, so a placeholder there is a
		// sign the entry was written as a one-shot CLI and given the wrong
		// kind. Substituting it would pin turn one's prompt into the argv of a
		// process that then runs every later turn — said here rather than
		// discovered as an agent answering the first question forever.
		if p.Kind == KindACP && hasPlaceholder {
			return fmt.Errorf("an acp command is argv, not a prompt template: the prompt travels over the protocol, so {{prompt}}/{{file}} do not belong in %q", joined)
		}
	case "":
		return fmt.Errorf("kind is required (http, cli or acp)")
	default:
		return fmt.Errorf("unknown kind %q", p.Kind)
	}
	return nil
}

func validateClassifier(c Classifier) error {
	switch c.Backend {
	case "typesafe", "openrouter", "llm", "static":
		return nil
	case "":
		return fmt.Errorf("backend is required (typesafe, openrouter, llm or static)")
	default:
		return fmt.Errorf("unknown backend %q", c.Backend)
	}
}

func validateNotifier(n Notifier) error {
	switch n.Kind {
	case "webhook":
		if n.URL == "" {
			return fmt.Errorf("a webhook notifier needs a url")
		}
		return nil
	case "":
		return fmt.Errorf("kind is required (webhook)")
	default:
		return fmt.Errorf("unknown kind %q", n.Kind)
	}
}

// MissingProviders / MissingClassifiers / MissingMcp let the Catalog stand in
// as a workflow validator.
func (c *Catalog) MissingProviders(names []string) []string   { return c.Providers.Missing(names) }
func (c *Catalog) MissingClassifiers(names []string) []string { return c.Classifiers.Missing(names) }
func (c *Catalog) MissingMcp(names []string) []string         { return c.Mcp.Missing(names) }

// SkillSource is the part of the skill registry a workflow is validated
// against. It is an interface so catalog does not import skills, which keeps
// the dependency pointing one way.
type SkillSource interface {
	Missing(names []string) []string
	MissingBuiltins(names []string) []string
}

// Validator is every registry a workflow can name, as one value. It satisfies
// workflow.Catalog by composition rather than by a fourth copy of the same
// five methods.
type Validator struct {
	SkillSource
	*Catalog
}

// NewValidator pairs the skill registry with the rest of the catalog.
func NewValidator(skills SkillSource, c *Catalog) Validator {
	return Validator{SkillSource: skills, Catalog: c}
}

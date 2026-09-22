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
	Preset  string   `json:"preset,omitempty"` // devin | claude | copilot | codex | opencode
	Command []string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// Repairs is how many extra attempts a turn gets when the reply fails
	// validation; the bad output and the parse error go back so it can correct
	// itself. Never a silent fallback.
	Repairs    int `json:"repairs,omitempty"`
	TimeoutSec int `json:"timeoutSec,omitempty"`
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

// Catalog is the whole set, one registry per kind.
type Catalog struct {
	Providers   *registry.Registry[Provider]
	Classifiers *registry.Registry[Classifier]
	Mcp         *registry.Registry[McpServer]
}

// file is the on-disk shape of registries.json.
type file struct {
	Providers   map[string]Provider   `json:"providers"`
	Classifiers map[string]Classifier `json:"classifiers"`
	// McpServers matches mcp.json's own key, so one file can serve both.
	McpServers map[string]McpServer `json:"mcpServers"`
}

// Load reads registries.json and merges mcp.json's servers. Neither file is
// required: an absent file contributes nothing rather than failing the boot,
// because a workflow that names nothing needs neither.
func Load(registriesPath, mcpPath string) (*Catalog, error) {
	c := &Catalog{
		Providers:   registry.New[Provider]("provider"),
		Classifiers: registry.New[Classifier]("classifier"),
		Mcp:         registry.New[McpServer]("mcp server"),
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

func validateProvider(p Provider) error {
	switch p.Kind {
	case KindHTTP:
		if p.BaseURL == "" || p.Model == "" {
			return fmt.Errorf("an http provider needs baseUrl and model")
		}
		if p.Style != "openai" && p.Style != "anthropic" {
			return fmt.Errorf("style must be openai or anthropic, got %q", p.Style)
		}
	case KindCLI, KindACP:
		if p.Preset == "" && len(p.Command) == 0 {
			return fmt.Errorf("a %s provider needs a preset or a command", p.Kind)
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

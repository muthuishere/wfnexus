// Package config reads the process configuration from the environment.
// Secrets (API keys) are never read into config — toolnexus reads
// OPENROUTER_API_KEY / OPENAI_API_KEY itself at call time.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// splitList reads a comma-separated env var into trimmed, non-empty entries.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type Config struct {
	Addr         string
	DatabaseURL  string
	S3Endpoint   string
	S3AccessKey  string
	S3SecretKey  string
	S3Bucket     string
	S3UseSSL     bool
	WorkflowsDir string
	// TemplatesDir holds workflows that exist to be copied. Separate from
	// WorkflowsDir so a template is never mistaken for something to run, and so
	// `wfx apply` cannot quietly overwrite one.
	TemplatesDir string
	SkillsDir    string
	McpConfig    string
	// RegistriesPath holds providers, classifiers and MCP servers — everything a
	// step can name.
	RegistriesPath string
	WorkDir        string // runtime dir for repo checkouts (outside the repo)
	UIDir          string // built React bundle, served if present
	LLMBaseURL     string
	LLMStyle       string
	Model          string
	// MaxConcurrentRuns bounds how many runs execute at once; the rest wait in
	// queued. Each run drives several agents and a repo worktree, so this is the
	// knob that keeps a burst of reports from thrashing the machine.
	MaxConcurrentRuns int
	// RunnerLabels are the labels THIS process serves itself. A step whose
	// `runs-on` names one of them runs here, in process, exactly as before
	// workers existed; anything else is queued for a worker that holds the
	// label. So a single-machine install needs no workers at all, and adding
	// one is additive rather than a migration.
	// The default is `local` ALONE, deliberately. `self-hosted` is the label a
	// worker conventionally advertises — it is what the join command suggests,
	// and what GitHub's own self-hosted runners carry. If the platform served
	// it too, the first machine anyone joined would sit idle forever while the
	// server quietly took its work.
	RunnerLabels []string
	// RunnerToken is the registration token a machine presents to join. Empty
	// means "generate one and keep it", which is what a fresh install does.
	RunnerToken string
	// SecretKeyEnv names the variable holding the env store's encryption key;
	// SecretKeyPath is where one is written on first boot if that is unset.
	// The key is never stored beside the values it protects.
	SecretKeyEnv  string
	SecretKeyPath string
	// PublicURL is the address a worker can reach this server on, used to build
	// the join command shown in the dashboard. Empty ⇒ inferred per request.
	PublicURL string
	// LLMAPIKeyEnv names the env var holding the provider key — the NAME, never
	// the value, so a key cannot end up in config, logs or an event.
	LLMAPIKeyEnv string
	// Classifier (the judge tier). systemone over OpenRouter is what the spikes
	// verified; the library's default base returns 400 Unknown model.
	ClassifierBaseURL   string
	ClassifierModel     string
	ClassifierAPIKeyEnv string
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func Load() Config {
	home, _ := os.UserHomeDir()
	root := env("WFX_ROOT", ".")
	return Config{
		Addr:           env("WFX_ADDR", ":8090"),
		DatabaseURL:    env("DATABASE_URL", "postgres://bfp:bfp@127.0.0.1:5460/bfp?sslmode=disable"),
		S3Endpoint:     env("S3_ENDPOINT", "127.0.0.1:9030"),
		S3AccessKey:    env("S3_ACCESS_KEY", "bfp"),
		S3SecretKey:    env("S3_SECRET_KEY", "bfpbfpbfp"),
		S3Bucket:       env("S3_BUCKET", "bfp-artifacts"),
		S3UseSSL:       env("S3_USE_SSL", "false") == "true",
		WorkflowsDir:   env("WFX_WORKFLOWS_DIR", filepath.Join(root, "workflows")),
		TemplatesDir:   env("WFX_TEMPLATES_DIR", filepath.Join(root, "templates")),
		SkillsDir:      env("WFX_SKILLS_DIR", filepath.Join(root, "skills")),
		McpConfig:      env("WFX_MCP_CONFIG", filepath.Join(root, "mcp.json")),
		RegistriesPath: env("WFX_REGISTRIES", filepath.Join(root, "registries.json")),
		WorkDir:        env("WFX_WORKDIR", filepath.Join(home, ".local", "share", "wfnexus", "runs")),
		UIDir:          env("WFX_UI_DIR", filepath.Join(root, "apps", "ui", "dist")),
		LLMBaseURL:     env("LLM_BASE_URL", "https://openrouter.ai/api/v1"),
		LLMStyle:       env("LLM_STYLE", "openai"),
		Model:          env("WFX_MODEL", "anthropic/claude-sonnet-4.5"),

		MaxConcurrentRuns: envInt("WFX_MAX_CONCURRENT_RUNS", 4),
		RunnerLabels:      splitList(env("WFX_RUNNER_LABELS", "local")),
		RunnerToken:       env("WFX_RUNNER_TOKEN", ""),
		SecretKeyEnv:      env("WFX_SECRET_KEY_ENV", "WFX_SECRET_KEY"),
		SecretKeyPath:     env("WFX_SECRET_KEY_PATH", filepath.Join(home, ".config", "wfnexus", "secret.key")),
		PublicURL:         strings.TrimRight(env("WFX_PUBLIC_URL", ""), "/"),
		LLMAPIKeyEnv:      env("WFX_LLM_API_KEY_ENV", "OPENROUTER_API_KEY"),

		ClassifierBaseURL:   env("WFX_CLASSIFIER_BASE_URL", "https://openrouter.ai/api/v1"),
		ClassifierModel:     env("WFX_CLASSIFIER_MODEL", "typesafe/jev-1.13"),
		ClassifierAPIKeyEnv: env("WFX_CLASSIFIER_API_KEY_ENV", "OPENROUTER_API_KEY"),
	}
}

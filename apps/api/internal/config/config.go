// Package config reads the process configuration from the environment.
// Secrets (API keys) are never read into config — toolnexus reads
// OPENROUTER_API_KEY / OPENAI_API_KEY itself at call time.
package config

import (
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	Addr         string
	DatabaseURL  string
	S3Endpoint   string
	S3AccessKey  string
	S3SecretKey  string
	S3Bucket     string
	S3UseSSL     bool
	WorkflowsDir string
	SkillsDir    string
	McpConfig    string
	WorkDir      string // runtime dir for repo checkouts (outside the repo)
	UIDir        string // built React bundle, served if present
	LLMBaseURL   string
	LLMStyle     string
	Model        string
	// MaxConcurrentRuns bounds how many runs execute at once; the rest wait in
	// queued. Each run drives several agents and a repo worktree, so this is the
	// knob that keeps a burst of reports from thrashing the machine.
	MaxConcurrentRuns int
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
	root := env("BFP_ROOT", ".")
	return Config{
		Addr:         env("BFP_ADDR", ":8090"),
		DatabaseURL:  env("DATABASE_URL", "postgres://bfp:bfp@127.0.0.1:5460/bfp?sslmode=disable"),
		S3Endpoint:   env("S3_ENDPOINT", "127.0.0.1:9030"),
		S3AccessKey:  env("S3_ACCESS_KEY", "bfp"),
		S3SecretKey:  env("S3_SECRET_KEY", "bfpbfpbfp"),
		S3Bucket:     env("S3_BUCKET", "bfp-artifacts"),
		S3UseSSL:     env("S3_USE_SSL", "false") == "true",
		WorkflowsDir: env("BFP_WORKFLOWS_DIR", filepath.Join(root, "workflows")),
		SkillsDir:    env("BFP_SKILLS_DIR", filepath.Join(root, "skills")),
		McpConfig:    env("BFP_MCP_CONFIG", filepath.Join(root, "mcp.json")),
		WorkDir:      env("BFP_WORKDIR", filepath.Join(home, ".local", "share", "bug-fixer-platform", "runs")),
		UIDir:        env("BFP_UI_DIR", filepath.Join(root, "apps", "ui", "dist")),
		LLMBaseURL:   env("LLM_BASE_URL", "https://openrouter.ai/api/v1"),
		LLMStyle:     env("LLM_STYLE", "openai"),
		Model:        env("BFP_MODEL", "anthropic/claude-sonnet-4.5"),

		MaxConcurrentRuns: envInt("BFP_MAX_CONCURRENT_RUNS", 4),
		LLMAPIKeyEnv:      env("BFP_LLM_API_KEY_ENV", "OPENROUTER_API_KEY"),

		ClassifierBaseURL:   env("BFP_CLASSIFIER_BASE_URL", "https://openrouter.ai/api/v1"),
		ClassifierModel:     env("BFP_CLASSIFIER_MODEL", "typesafe/jev-1.13"),
		ClassifierAPIKeyEnv: env("BFP_CLASSIFIER_API_KEY_ENV", "OPENROUTER_API_KEY"),
	}
}

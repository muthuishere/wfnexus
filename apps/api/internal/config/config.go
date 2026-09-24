// Package config reads the process configuration from the environment.
// Secrets (API keys) are never read into config — toolnexus reads
// OPENROUTER_API_KEY / OPENAI_API_KEY itself at call time.
package config

import (
	"fmt"
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
	Addr string
	// StorageDriver is "postgres" or "sqlite"; DatabaseURL is its DSN — a URL
	// for postgres, a file path for sqlite.
	StorageDriver string
	DatabaseURL   string
	// ArtifactDriver is "s3" or "folder"; ArtifactDir is where a folder driver
	// writes. A laptop wants a folder; a deployment wants a bucket.
	ArtifactDriver string
	ArtifactDir    string
	S3Endpoint     string
	S3AccessKey    string
	S3SecretKey    string
	S3Bucket       string
	S3UseSSL       bool
	WorkflowsDir   string
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
	// Explicit records which paths the operator actually STATED (env var or
	// config file) rather than inheriting from the built-in default. It only
	// matters for the embedded defaults: a stated path that does not exist is
	// a mistake worth shouting about, while an unstated one that does not
	// exist is just a fresh install with nothing beside the binary.
	Explicit Explicit
}

// Explicit is the set of path settings that were stated rather than defaulted.
type Explicit struct {
	Workflows  bool
	Templates  bool
	Skills     bool
	Registries bool
	UI         bool
}

// stated reports whether a setting was given by the environment or the file.
func stated(envKey, fromFile string) bool {
	return os.Getenv(envKey) != "" || fromFile != ""
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

// Load builds the configuration from the file, then the environment, then the
// built-in defaults — in that order of increasing precedence.
//
// The environment WINS over the file, which is the direction that stays correct
// under a container: an image may carry a config file and its operator sets env
// vars, and the operator must win. A person on a laptop edits the file, sets no
// env vars, and never meets the rule.
func Load() Config {
	cfg, _ := LoadWithFile(DefaultPath())
	return cfg
}

// LoadWithFile is Load against a named file, returning the parse error rather
// than swallowing it. A malformed config is worth refusing to start over.
func LoadWithFile(path string) (Config, error) {
	f, ferr := LoadFile(path)
	if f == nil {
		f = &File{}
	}
	// merr collects a stated-but-wrong value. It is returned rather than
	// silently repaired: a value the operator wrote and got wrong must fail
	// loudly, never fall back to the laptop default.
	var merr error
	home, _ := os.UserHomeDir()
	root := env("WFX_ROOT", ".")

	// `mode` is a shorthand for a set of defaults, never a separate code path.
	// Anything stated explicitly still wins over it.
	//
	// The UNSET mode is `local`. A binary someone just downloaded, with nothing
	// beside it, must boot — so the no-config default is the one that needs
	// nothing running: sqlite in a file and artifacts in a folder. A deployment
	// says `mode: server` (or sets the drivers itself), which is one line in the
	// file a deployment already has, whereas a laptop has no file at all.
	mode := strings.ToLower(strings.TrimSpace(env("WFX_MODE", f.Mode)))
	defStorage, defArtifacts := "sqlite", "folder"
	switch mode {
	case "", "local":
	case "server":
		defStorage, defArtifacts = "postgres", "s3"
	default:
		// A misspelled mode must not quietly select a set of defaults.
		merr = fmt.Errorf("mode %q is not one of local, server", mode)
	}

	// The DSN default follows the EFFECTIVE driver, not the mode, so
	// `mode: server` with `storage.driver: sqlite` still gets a file path.
	storageDriver := env("WFX_STORAGE_DRIVER", or(f.Storage.Driver, defStorage))
	defDSN := "postgres://bfp:bfp@127.0.0.1:5460/bfp?sslmode=disable"
	if strings.EqualFold(storageDriver, "sqlite") || strings.EqualFold(storageDriver, "sqlite3") {
		defDSN = filepath.Join(home, ".local", "share", "wfnexus", "wfnexus.db")
	}
	artifactDriver := env("WFX_ARTIFACT_DRIVER", or(f.Artifacts.Driver, defArtifacts))

	cfg := Config{
		Addr:           env("WFX_ADDR", or(f.Addr, ":8090")),
		StorageDriver:  storageDriver,
		DatabaseURL:    env("DATABASE_URL", or(f.Storage.DSN, defDSN)),
		ArtifactDriver: artifactDriver,
		ArtifactDir:    env("WFX_ARTIFACT_DIR", or(f.Artifacts.Dir, filepath.Join(home, ".local", "share", "wfnexus", "artifacts"))),
		S3Endpoint:     env("S3_ENDPOINT", or(f.Artifacts.Endpoint, "127.0.0.1:9030")),
		S3AccessKey:    env("S3_ACCESS_KEY", or(f.Artifacts.AccessKey, "bfp")),
		S3SecretKey:    env("S3_SECRET_KEY", or(f.Artifacts.SecretKey, "bfpbfpbfp")),
		S3Bucket:       env("S3_BUCKET", or(f.Artifacts.Bucket, "bfp-artifacts")),
		S3UseSSL:       env("S3_USE_SSL", boolStr(f.Artifacts.UseSSL, false)) == "true",
		WorkflowsDir:   env("WFX_WORKFLOWS_DIR", or(f.Paths.Workflows, filepath.Join(root, "workflows"))),
		TemplatesDir:   env("WFX_TEMPLATES_DIR", or(f.Paths.Templates, filepath.Join(root, "templates"))),
		SkillsDir:      env("WFX_SKILLS_DIR", or(f.Paths.Skills, filepath.Join(root, "skills"))),
		McpConfig:      env("WFX_MCP_CONFIG", or(f.Paths.Mcp, filepath.Join(root, "mcp.json"))),
		RegistriesPath: env("WFX_REGISTRIES", or(f.Paths.Registries, filepath.Join(root, "registries.json"))),
		WorkDir:        env("WFX_WORKDIR", or(f.Paths.Work, filepath.Join(home, ".local", "share", "wfnexus", "runs"))),
		UIDir:          env("WFX_UI_DIR", or(f.Paths.UI, filepath.Join(root, "apps", "ui", "dist"))),
		LLMBaseURL:     env("LLM_BASE_URL", or(f.Model.BaseURL, "https://openrouter.ai/api/v1")),
		LLMStyle:       env("LLM_STYLE", or(f.Model.Style, "openai")),
		Model:          env("WFX_MODEL", or(f.Model.Model, "anthropic/claude-sonnet-4.5")),

		MaxConcurrentRuns: envInt("WFX_MAX_CONCURRENT_RUNS", orInt(f.MaxConcurrentRuns, 4)),
		RunnerLabels:      splitList(env("WFX_RUNNER_LABELS", or(join(f.Runners.Labels), "local"))),
		RunnerToken:       env("WFX_RUNNER_TOKEN", f.Runners.Token),
		SecretKeyEnv:      env("WFX_SECRET_KEY_ENV", or(f.Secrets.KeyEnv, "WFX_SECRET_KEY")),
		SecretKeyPath:     env("WFX_SECRET_KEY_PATH", or(f.Secrets.KeyPath, filepath.Join(home, ".config", "wfnexus", "secret.key"))),
		PublicURL:         strings.TrimRight(env("WFX_PUBLIC_URL", f.PublicURL), "/"),
		LLMAPIKeyEnv:      env("WFX_LLM_API_KEY_ENV", or(f.Model.APIKeyEnv, "OPENROUTER_API_KEY")),

		ClassifierBaseURL:   env("WFX_CLASSIFIER_BASE_URL", or(f.Classifier.BaseURL, "https://openrouter.ai/api/v1")),
		ClassifierModel:     env("WFX_CLASSIFIER_MODEL", or(f.Classifier.Model, "typesafe/jev-1.13")),
		ClassifierAPIKeyEnv: env("WFX_CLASSIFIER_API_KEY_ENV", or(f.Classifier.APIKeyEnv, "OPENROUTER_API_KEY")),

		Explicit: Explicit{
			Workflows:  stated("WFX_WORKFLOWS_DIR", f.Paths.Workflows),
			Templates:  stated("WFX_TEMPLATES_DIR", f.Paths.Templates),
			Skills:     stated("WFX_SKILLS_DIR", f.Paths.Skills),
			Registries: stated("WFX_REGISTRIES", f.Paths.Registries),
			UI:         stated("WFX_UI_DIR", f.Paths.UI),
		},
	}
	if ferr == nil {
		ferr = merr
	}
	if ferr == nil {
		ferr = cfg.validate()
	}
	return cfg, ferr
}

// validate refuses a driver nobody implements. Without it a typo in
// `storage.driver` lands on postgres and an artifacts typo lands on s3 — the
// operator asked for one thing, got another, and the only symptom is a
// connection error to something they never named.
func (c Config) validate() error {
	switch strings.ToLower(c.StorageDriver) {
	case "sqlite", "sqlite3", "postgres", "postgresql", "pgx":
	default:
		return fmt.Errorf("storage driver %q is not one of sqlite, postgres", c.StorageDriver)
	}
	switch strings.ToLower(c.ArtifactDriver) {
	case "folder", "s3":
	default:
		return fmt.Errorf("artifact driver %q is not one of folder, s3", c.ArtifactDriver)
	}
	return nil
}

// or is the file's value, or the built-in default when the file said nothing.
func or(fromFile, def string) string {
	if fromFile != "" {
		return fromFile
	}
	return def
}

func orInt(fromFile, def int) int {
	if fromFile > 0 {
		return fromFile
	}
	return def
}

func boolStr(b *bool, def bool) string {
	v := def
	if b != nil {
		v = *b
	}
	if v {
		return "true"
	}
	return "false"
}

func join(list []string) string { return strings.Join(list, ",") }

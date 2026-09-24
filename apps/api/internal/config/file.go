package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ONE FILE THAT SAYS EVERYTHING.
//
// Until now every setting was an environment variable, which is right for a
// container and wrong for a person: nobody wants a twelve-line `export` block
// in their shell profile to run something on their laptop. So there is a file,
// and the file can say all of it.
//
//	~/.config/wfx/config.yaml
//
// Precedence, and the direction matters: an ENVIRONMENT VARIABLE WINS over the
// file. A container sets env vars and must not be overridden by a file that
// happened to be baked into an image; a person edits the file and sets no env
// vars, so they never meet the rule. The loser of a precedence fight should be
// the one that is easier to change, and that is the file.
//
// Nothing here is required. An absent file is not an error — it is the normal
// state of a fresh install, which runs on the defaults.

// File is the on-disk shape. Every field is optional and an omitted one falls
// through to the environment, then to the built-in default.
type File struct {
	// Mode is a shorthand, not a separate code path: `local` selects the
	// defaults a person wants on a laptop (sqlite, a folder for artifacts),
	// `server` the ones a deployment wants (postgres, s3). Anything set
	// explicitly below still wins over the shorthand.
	Mode string `yaml:"mode,omitempty"`

	Addr string `yaml:"addr,omitempty"`
	// PublicURL is the address workers are told to reach, when this sits
	// behind an ingress.
	PublicURL string `yaml:"publicUrl,omitempty"`

	Storage struct {
		// Driver is "sqlite" or "postgres".
		Driver string `yaml:"driver,omitempty"`
		// DSN is the connection string: a file path for sqlite, a URL for
		// postgres.
		DSN string `yaml:"dsn,omitempty"`
	} `yaml:"storage,omitempty"`

	Artifacts struct {
		// Driver is "folder" or "s3".
		Driver string `yaml:"driver,omitempty"`
		// Dir is where a folder driver writes.
		Dir string `yaml:"dir,omitempty"`
		// The s3 driver's settings. A key here is a real credential in a file
		// on disk, so prefer the environment for these on anything shared.
		Endpoint  string `yaml:"endpoint,omitempty"`
		Bucket    string `yaml:"bucket,omitempty"`
		AccessKey string `yaml:"accessKey,omitempty"`
		SecretKey string `yaml:"secretKey,omitempty"`
		UseSSL    *bool  `yaml:"useSsl,omitempty"`
	} `yaml:"artifacts,omitempty"`

	Model struct {
		BaseURL string `yaml:"baseUrl,omitempty"`
		Style   string `yaml:"style,omitempty"`
		Model   string `yaml:"model,omitempty"`
		// APIKeyEnv is the NAME of the variable holding the key. The key
		// itself is never a field here, and that is deliberate: this file is
		// the one people paste into an issue when something does not work.
		APIKeyEnv string `yaml:"apiKeyEnv,omitempty"`
	} `yaml:"model,omitempty"`

	Classifier struct {
		BaseURL   string `yaml:"baseUrl,omitempty"`
		Model     string `yaml:"model,omitempty"`
		APIKeyEnv string `yaml:"apiKeyEnv,omitempty"`
	} `yaml:"classifier,omitempty"`

	Paths struct {
		Workflows  string `yaml:"workflows,omitempty"`
		Templates  string `yaml:"templates,omitempty"`
		Skills     string `yaml:"skills,omitempty"`
		Registries string `yaml:"registries,omitempty"`
		Mcp        string `yaml:"mcp,omitempty"`
		Work       string `yaml:"work,omitempty"`
		UI         string `yaml:"ui,omitempty"`
	} `yaml:"paths,omitempty"`

	Runners struct {
		// Labels this process serves itself.
		Labels []string `yaml:"labels,omitempty"`
		// Token a machine presents to join. Generated and kept if empty.
		Token string `yaml:"token,omitempty"`
	} `yaml:"runners,omitempty"`

	// MaxConcurrentRuns bounds how many runs execute at once.
	MaxConcurrentRuns int `yaml:"maxConcurrentRuns,omitempty"`

	// Secrets is the env store's key. Only the variable NAME or a path — the
	// key is never a value in this file, for the same reason as the model key.
	Secrets struct {
		KeyEnv  string `yaml:"keyEnv,omitempty"`
		KeyPath string `yaml:"keyPath,omitempty"`
	} `yaml:"secrets,omitempty"`
}

// DefaultPath is where the file lives unless WFX_CONFIG says otherwise.
func DefaultPath() string {
	if v := os.Getenv("WFX_CONFIG"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "wfx", "config.yaml")
}

// LoadFile reads the config file. An absent file is not an error: it is the
// normal state of a fresh install.
func LoadFile(path string) (*File, error) {
	if path == "" {
		return &File{}, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &File{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f File
	// Strict: a misspelled key in a config file is silence, and silence here
	// means running with a default the person believes they overrode.
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

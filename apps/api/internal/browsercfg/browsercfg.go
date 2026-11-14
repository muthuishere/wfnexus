// Package browsercfg reads the browser block: named browser profiles on the
// machine that runs steps. The file is runner-local (delivered as a carry file,
// for example), never stored by the server:
//
//	$WFX_BROWSER_CONFIG, default ~/.config/wfx/browser.json
//
//	{"default": "us",
//	 "profiles": {"us": {"command": ["some-browser-cli"], "folder": "/data/profiles/us",
//	                     "proxy": "http://127.0.0.1:8881"}}}
//
// command is an argv (a plain string is split on spaces); proxy is optional.
// The browser module (modules/browser) runs the command with a profile.
package browsercfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Profile is one named browser profile.
type Profile struct {
	Command []string `json:"command"`
	Folder  string   `json:"folder"`
	Proxy   string   `json:"proxy,omitempty"`
}

// UnmarshalJSON accepts command as an argv array or a space-separated string.
func (p *Profile) UnmarshalJSON(b []byte) error {
	var raw struct {
		Command json.RawMessage `json:"command"`
		Folder  string          `json:"folder"`
		Proxy   string          `json:"proxy"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	p.Folder, p.Proxy, p.Command = raw.Folder, raw.Proxy, nil
	if len(raw.Command) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw.Command, &s); err == nil {
		p.Command = strings.Fields(s)
		return nil
	}
	return json.Unmarshal(raw.Command, &p.Command)
}

// Config is the browser block.
type Config struct {
	Default  string             `json:"default"`
	Profiles map[string]Profile `json:"profiles"`
}

// ErrNotConfigured: there is no config file, or it names no profile.
var ErrNotConfigured = errors.New("no browser profiles configured")

// Path is $WFX_BROWSER_CONFIG or ~/.config/wfx/browser.json.
func Path() string {
	if p := os.Getenv("WFX_BROWSER_CONFIG"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "wfx", "browser.json")
}

// Load reads and validates the file. A missing file is ErrNotConfigured.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotConfigured
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(c.Profiles) == 0 {
		return nil, ErrNotConfigured
	}
	if c.Default != "" {
		if _, ok := c.Profiles[c.Default]; !ok {
			return nil, fmt.Errorf("%s: default %q is not one of the profiles (%s)", path, c.Default, strings.Join(c.Names(), ", "))
		}
	}
	for n, p := range c.Profiles {
		if len(p.Command) == 0 {
			return nil, fmt.Errorf("%s: profile %q has no command", path, n)
		}
	}
	return &c, nil
}

// Names are the profile names, sorted.
func (c *Config) Names() []string {
	n := make([]string, 0, len(c.Profiles))
	for k := range c.Profiles {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// Status is one profile as found on this machine.
type Status struct {
	Name          string `json:"name"`
	Default       bool   `json:"default"`
	Command       string `json:"command"`
	CommandFound  bool   `json:"commandFound"`
	Folder        string `json:"folder"`
	FolderPresent bool   `json:"folderPresent"`
	Proxy         string `json:"proxy,omitempty"`
	Usable        bool   `json:"usable"`
	Problem       string `json:"problem,omitempty"`
}

// Check reports every profile: command on PATH, folder present.
func (c *Config) Check() []Status {
	var out []Status
	for _, n := range c.Names() {
		p := c.Profiles[n]
		s := Status{Name: n, Default: n == c.Default, Command: strings.Join(p.Command, " "), Folder: p.Folder, Proxy: p.Proxy}
		_, err := exec.LookPath(p.Command[0])
		s.CommandFound = err == nil
		if fi, err := os.Stat(p.Folder); err == nil && fi.IsDir() {
			s.FolderPresent = true
		}
		var probs []string
		if !s.CommandFound {
			probs = append(probs, "command "+p.Command[0]+" not found")
		}
		if p.Folder == "" {
			probs = append(probs, "no folder")
		} else if !s.FolderPresent {
			probs = append(probs, "folder "+p.Folder+" missing")
		}
		s.Usable = len(probs) == 0
		s.Problem = strings.Join(probs, "; ")
		out = append(out, s)
	}
	return out
}

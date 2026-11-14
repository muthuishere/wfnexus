// Package browsercfg reads the browser block: named browser profiles on the
// machine that runs steps. The file is runner-local (delivered as a carry file,
// for example), never stored by the server:
//
//	$WFX_BROWSER_CONFIG, default ~/.config/wfx/browser.json
//
//	{"default": "us",
//	 "profiles": {"us": {"command": ["some-browser-cli"], "folder": "/data/profiles/us",
//	                     "proxy": "http://127.0.0.1:8881", "read_actions": ["open", "text"]}}}
//
// command is an argv (a plain string is split on spaces); proxy is optional
// and must not carry credentials; read_actions lists the actions browser.read
// may run with this profile (none listed = no reads).
// The browser module (modules/browser) runs the command with a profile.
package browsercfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Profile is one named browser profile.
type Profile struct {
	Command     []string `json:"command"`
	Folder      string   `json:"folder"`
	Proxy       string   `json:"proxy,omitempty"`
	ReadActions []string `json:"read_actions,omitempty"`
}

// UnmarshalJSON accepts command as an argv array or a space-separated string.
func (p *Profile) UnmarshalJSON(b []byte) error {
	var raw struct {
		Command     json.RawMessage `json:"command"`
		Folder      string          `json:"folder"`
		Proxy       string          `json:"proxy"`
		ReadActions []string        `json:"read_actions"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	p.Folder, p.Proxy, p.ReadActions, p.Command = raw.Folder, raw.Proxy, raw.ReadActions, nil
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
		for _, a := range p.ReadActions {
			if strings.TrimSpace(a) == "" {
				return nil, fmt.Errorf("%s: profile %q has an empty read_actions entry", path, n)
			}
		}
		if HasUserinfo(p.Proxy) {
			// The message never repeats the URL: it holds the credential.
			return nil, fmt.Errorf("%s: profile %q proxy carries credentials (user@ or user:pass@); use a local proxy without them", path, n)
		}
	}
	return &c, nil
}

// HasUserinfo is whether a proxy address carries a user or user:pass part.
func HasUserinfo(proxy string) bool {
	if proxy == "" {
		return false
	}
	if u, err := url.Parse(proxy); err == nil && u.User != nil {
		return true
	}
	// No scheme ("user:pass@host:port") parses without User: check by hand.
	host := proxy
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	return strings.Contains(host, "@")
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
	Name          string   `json:"name"`
	Default       bool     `json:"default"`
	Command       string   `json:"command"`
	CommandFound  bool     `json:"commandFound"`
	Folder        string   `json:"folder"`
	FolderPresent bool     `json:"folderPresent"`
	Proxy         string   `json:"proxy,omitempty"`
	ReadActions   []string `json:"readActions"`
	Usable        bool     `json:"usable"`
	Problem       string   `json:"problem,omitempty"`
}

// Check reports every profile: command on PATH, folder present.
func (c *Config) Check() []Status {
	var out []Status
	for _, n := range c.Names() {
		p := c.Profiles[n]
		s := Status{Name: n, Default: n == c.Default, Command: strings.Join(p.Command, " "), Folder: p.Folder, Proxy: p.Proxy,
			ReadActions: append([]string{}, p.ReadActions...)}
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

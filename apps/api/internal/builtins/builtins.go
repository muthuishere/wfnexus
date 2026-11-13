// Package builtins provides the tools wfnexus ships with on every machine
// that runs steps — the server's local runner, every wfx-runner, the Docker
// image: aos, ctx-optimize, jevx, tmvp and gh, at pinned versions.
//
// tmvp and gh are GitHub release binaries (Release set): downloaded into <dir>/bin
// and verified against the release's checksums.txt. The rest come from npm (@muthuishere/<name>) installed into a private prefix
// (<dir>/tools), whose node_modules/.bin is put FIRST on this process's PATH —
// so every step it starts finds them with no user step. npm picks the right
// binary per OS/arch (Windows included). Node/npm itself is the runner's:
// when it is missing, every tool reports so. Language toolchains (Go, Node, …)
// are bring-your-own.
package builtins

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Tool is one pinned built-in.
type Tool struct {
	Name, Package, Version string
	// Release is the GitHub owner/repo of a release-binary tool; empty = npm.
	Release string
	// Asset names the release files for a version on this OS/arch: the
	// download, its checksums file, and the binary inside an archive ("" =
	// the download is the binary). nil = <name>_<os>_<arch>[.exe] + checksums.txt.
	Asset func(version string) (file, sums, member string)
}

// Pinned is what every runner carries: the floor and the default. Bump here.
var Pinned = []Tool{
	{Name: "aos", Package: "@muthuishere/aos", Version: "0.12.0"},
	{Name: "ctx-optimize", Package: "@muthuishere/ctx-optimize", Version: "0.15.5"},
	{Name: "jevx", Package: "@muthuishere/jevx", Version: "0.12.13"},
	{Name: "tmvp", Package: "github.com/muthuishere/tmvp", Version: "0.1.0", Release: "muthuishere/tmvp"},
	{Name: "gh", Package: "github.com/cli/cli", Version: "2.104.0", Release: "cli/cli", Asset: ghAsset},
}

// Status is one tool on this machine.
type Status struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path,omitempty"`
	Error   string `json:"error,omitempty"`
}

// NoNpm is the error every tool reports on a runner without Node.js.
const NoNpm = "node/npm is unavailable on this runner and could not be bundled — see the node row"

var (
	mu   sync.Mutex
	last []Status
)

// Last is the result of the most recent Ensure (for doctor/worker reports).
func Last() []Status {
	mu.Lock()
	defer mu.Unlock()
	return append([]Status(nil), last...)
}

// Summary is "aos 0.12.0, ctx-optimize 0.15.5, jevx ✗" for one-line reports.
func Summary(st []Status) string {
	var p []string
	for _, s := range st {
		if s.Error != "" {
			p = append(p, s.Name+" ✗ ("+s.Error+")")
		} else {
			p = append(p, s.Name+" "+s.Version)
		}
	}
	return strings.Join(p, ", ")
}

// DefaultDir is ~/.local/share/wfnexus/builtins (WFX_BUILTINS_DIR overrides).
func DefaultDir() string {
	if d := os.Getenv("WFX_BUILTINS_DIR"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "wfnexus", "builtins")
}

func prefix(dir string) string { return filepath.Join(dir, "tools") }

// BinDir is where the tools' launchers live.
func BinDir(dir string) string { return filepath.Join(prefix(dir), "node_modules", ".bin") }

// installed is the version of a package in the prefix, "" when absent.
func installed(dir string, t Tool) string {
	b, err := os.ReadFile(filepath.Join(prefix(dir), "node_modules", filepath.FromSlash(t.Package), "package.json"))
	if err != nil {
		return ""
	}
	var p struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(b, &p)
	return p.Version
}

// Ensure installs every tool at its wanted version (want overrides the pinned
// one: an update or a rollback) and puts the prefix's bin first on PATH. A tool
// already at its version is left alone; a step already running keeps the
// binary it started. Failures are reported, never fatal.
func Ensure(dir string, want map[string]string) []Status {
	var out []Status
	node := EnsureNode(dir)
	npm, npmErr := exec.LookPath("npm")
	_ = os.MkdirAll(prefix(dir), 0o755)
	for _, t := range Pinned {
		if v := strings.TrimPrefix(want[t.Name], "v"); v != "" {
			t.Version = v
		}
		s := Status{Name: t.Name, Version: t.Version}
		if t.Release != "" {
			if err := ensureRelease(dir, t); err != nil {
				s.Error = err.Error()
			} else {
				s.Path = releasePath(dir, t.Name)
			}
			out = append(out, s)
			continue
		}
		switch {
		case installed(dir, t) == t.Version:
		case npmErr != nil:
			s.Error = NoNpm
		default:
			cmd := exec.Command(npm, "install", "--prefix", prefix(dir), "--no-audit", "--no-fund", "--loglevel=error", t.Package+"@"+t.Version)
			if b, err := cmd.CombinedOutput(); err != nil {
				s.Error = fmt.Sprintf("npm install %s@%s: %v %s", t.Package, t.Version, err, lastLine(string(b)))
			} else if got := installed(dir, t); got != t.Version {
				s.Error = fmt.Sprintf("npm installed %q, want %s", got, t.Version)
			}
		}
		if s.Error == "" {
			s.Path = filepath.Join(BinDir(dir), t.Name)
		}
		out = append(out, s)
	}
	ns := Status{Name: "node", Version: node.Version, Error: node.Error}
	if node.Source != "" {
		ns.Path = node.Source
	}
	out = append(out, ns)
	py := Status{Name: "python", Version: PythonVersion()}
	if py.Version == "" {
		py.Version = "not installed (optional)"
	}
	out = append(out, py)
	for _, bin := range []string{RelBinDir(dir), BinDir(dir)} {
		if path := os.Getenv("PATH"); !strings.HasPrefix(path, bin+string(os.PathListSeparator)) {
			_ = os.Setenv("PATH", bin+string(os.PathListSeparator)+path)
		}
	}
	mu.Lock()
	last = out
	mu.Unlock()
	return out
}

func lastLine(s string) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(l[len(l)-1])
}

// Find is the pinned entry for a name.
func Find(name string) (Tool, bool) {
	for _, t := range Pinned {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Latest asks npm (or GitHub, for a release tool) for the newest version.
func Latest(t Tool) (string, error) {
	if t.Release != "" {
		return latestRelease(t)
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		return "", fmt.Errorf("%s", NoNpm)
	}
	b, err := exec.Command(npm, "view", t.Package, "version").Output()
	if err != nil {
		return "", fmt.Errorf("npm view %s: %w", t.Package, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Desired is the server's chosen versions (name -> version), persisted in
// <dir>/desired.json. Every runner converges on it at its next heartbeat.
func Desired(dir string) map[string]string {
	out := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(dir, "desired.json")); err == nil {
		_ = json.Unmarshal(b, &out)
	}
	return out
}

// SetDesired records one tool's version after it installed cleanly.
func SetDesired(dir, name, version string) error {
	d := Desired(dir)
	d[name] = strings.TrimPrefix(version, "v")
	b, _ := json.MarshalIndent(d, "", "  ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "desired.json"), b, 0o644)
}

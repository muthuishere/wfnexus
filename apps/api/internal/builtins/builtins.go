// Package builtins provides the tools wfnexus ships with on every machine
// that runs steps — the server's local runner, every wfx-runner, the Docker
// image: aos, ctx-optimize and jevx, at pinned versions.
//
// They are fetched once per version from their GitHub releases for this
// OS/arch, verified against the release's checksums.txt, kept under
// <dir>/<name>/<version>/, linked into <dir>/bin, and <dir>/bin is put FIRST on
// this process's PATH — so every step it starts finds them with no user step.
// Language toolchains (Go, Node, …) are not ours: bring your own.
package builtins

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Tool is one pinned built-in.
type Tool struct {
	Name, Repo, Version string
	// Raw: the asset is the binary itself (<name>_<os>_<arch>[.exe]);
	// otherwise <name>_<ver>_<os>_<arch>.tar.gz with the binary at its root.
	Raw bool
}

// Pinned is what every runner carries. Bump here, nowhere else.
var Pinned = []Tool{
	{Name: "aos", Repo: "muthuishere/aos", Version: "0.12.0"},
	{Name: "ctx-optimize", Repo: "muthuishere/ctx-optimize", Version: "0.15.5"},
	{Name: "jevx", Repo: "muthuishere/jevx", Version: "0.12.13", Raw: true},
}

// Status is one tool on this machine.
type Status struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Path    string `json:"path,omitempty"`
	Error   string `json:"error,omitempty"`
}

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
			p = append(p, s.Name+" ✗")
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

var base = "https://github.com" // tests point this at a fake server

// Ensure makes every pinned tool present and verified under dir, links it into
// dir/bin and puts dir/bin first on PATH. A tool that cannot be fetched is
// reported, not fatal: the process runs, the step that needs it says so.
//
// want overrides a tool's version (an update or a rollback); a tool missing
// from it runs at its pinned version. Previous versions stay on disk, so a
// rollback is a link swap. A step already running keeps the binary it exec'd.
func Ensure(dir string, want map[string]string) []Status {
	binDir := filepath.Join(dir, "bin")
	_ = os.MkdirAll(binDir, 0o755)
	var out []Status
	for _, t := range Pinned {
		if v := strings.TrimPrefix(want[t.Name], "v"); v != "" {
			t.Version = v
		}
		s := Status{Name: t.Name, Version: t.Version}
		p, err := ensureOne(dir, binDir, t)
		if err != nil {
			s.Error = err.Error()
		} else {
			s.Path = p
		}
		out = append(out, s)
	}
	path := os.Getenv("PATH")
	if !strings.HasPrefix(path, binDir+string(os.PathListSeparator)) {
		_ = os.Setenv("PATH", binDir+string(os.PathListSeparator)+path)
	}
	mu.Lock()
	last = out
	mu.Unlock()
	return out
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func ensureOne(dir, binDir string, t Tool) (string, error) {
	vdir := filepath.Join(dir, t.Name, t.Version)
	target := filepath.Join(vdir, exe(t.Name))
	link := filepath.Join(binDir, exe(t.Name))
	if _, err := os.Stat(target); err != nil {
		if err := fetch(t, vdir, target); err != nil {
			return "", err
		}
	}
	_ = os.Remove(link)
	if err := os.Symlink(target, link); err != nil {
		// Windows without symlink rights: copy.
		b, rerr := os.ReadFile(target)
		if rerr != nil {
			return "", err
		}
		if err := os.WriteFile(link, b, 0o755); err != nil {
			return "", err
		}
	}
	return link, nil
}

func asset(t Tool) string {
	if t.Raw {
		return exe(fmt.Sprintf("%s_%s_%s", t.Name, runtime.GOOS, runtime.GOARCH))
	}
	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s%s", t.Name, t.Version, runtime.GOOS, runtime.GOARCH, ext)
}

var client = &http.Client{Timeout: 5 * time.Minute}

func get(url string) ([]byte, error) {
	r, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, r.Status)
	}
	return io.ReadAll(r.Body)
}

func fetch(t Tool, vdir, target string) error {
	rel := fmt.Sprintf("%s/%s/releases/download/v%s/", base, t.Repo, t.Version)
	name := asset(t)
	if strings.HasSuffix(name, ".zip") {
		return fmt.Errorf("%s: zip assets (windows) are not unpacked yet", name)
	}
	sums, err := get(rel + "checksums.txt")
	if err != nil {
		return err
	}
	want := ""
	for _, l := range strings.Split(string(sums), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("%s v%s: no checksum for %s", t.Name, t.Version, name)
	}
	blob, err := get(rel + name)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(blob)
	if hex.EncodeToString(sum[:]) != want {
		return fmt.Errorf("%s v%s: checksum mismatch for %s", t.Name, t.Version, name)
	}
	bin := blob
	if !t.Raw {
		if bin, err = fromTarGz(blob, exe(t.Name)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		return err
	}
	tmp := target + ".part"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

func fromTarGz(blob []byte, want string) ([]byte, error) {
	zr, err := gzip.NewReader(strings.NewReader(string(blob)))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("no %s in the archive", want)
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(h.Name) == want && h.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
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

// Latest asks GitHub for a tool's newest release version (no leading v).
func Latest(t Tool) (string, error) {
	api := "https://api.github.com"
	if base != "https://github.com" {
		api = base + "/api"
	}
	b, err := get(fmt.Sprintf("%s/repos/%s/releases/latest", api, t.Repo))
	if err != nil {
		return "", err
	}
	var r struct {
		Tag string `json:"tag_name"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.Tag == "" {
		return "", fmt.Errorf("%s: no latest release", t.Repo)
	}
	return strings.TrimPrefix(r.Tag, "v"), nil
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

// SetDesired records one tool's version after it was fetched and verified.
func SetDesired(dir, name, version string) error {
	d := Desired(dir)
	d[name] = strings.TrimPrefix(version, "v")
	b, _ := json.MarshalIndent(d, "", "  ")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "desired.json"), b, 0o644)
}

package builtins

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Release tools: <name>_<os>_<arch>[.exe] plus checksums.txt on the GitHub
// release v<version>. WFX_RELEASE_BASE / WFX_RELEASE_API override the hosts
// (tests, mirrors).

// RelBinDir is where release binaries live.
func RelBinDir(dir string) string { return filepath.Join(dir, "bin") }

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func releasePath(dir, name string) string { return filepath.Join(RelBinDir(dir), exe(name)) }

func versionFile(dir, name string) string { return filepath.Join(RelBinDir(dir), "."+name+".version") }

func releaseBase() string {
	if b := os.Getenv("WFX_RELEASE_BASE"); b != "" {
		return strings.TrimRight(b, "/")
	}
	return "https://github.com"
}

func relGet(url string) ([]byte, error) {
	resp, err := httpc.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// ensureRelease installs t at t.Version unless that version is already there.
func ensureRelease(dir string, t Tool) error {
	if b, err := os.ReadFile(versionFile(dir, t.Name)); err == nil && strings.TrimSpace(string(b)) == t.Version {
		if _, err := os.Stat(releasePath(dir, t.Name)); err == nil {
			return nil
		}
	}
	asset := fmt.Sprintf("%s_%s_%s", t.Name, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}
	url := fmt.Sprintf("%s/%s/releases/download/v%s/", releaseBase(), t.Release, t.Version)
	sums, err := relGet(url + "checksums.txt")
	if err != nil {
		return err
	}
	want := ""
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 && strings.TrimPrefix(f[1], "*") == asset {
			want = f[0]
		}
	}
	if want == "" {
		return fmt.Errorf("%s v%s has no %s in checksums.txt", t.Release, t.Version, asset)
	}
	bin, err := relGet(url + asset)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bin)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("%s checksum mismatch: got %s, want %s", asset, got, want)
	}
	if err := os.MkdirAll(RelBinDir(dir), 0o755); err != nil {
		return err
	}
	tmp := releasePath(dir, t.Name) + ".tmp"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return err
	}
	// Rename over the old binary: a step already running keeps its inode.
	if err := os.Rename(tmp, releasePath(dir, t.Name)); err != nil {
		return err
	}
	return os.WriteFile(versionFile(dir, t.Name), []byte(t.Version+"\n"), 0o644)
}

func latestRelease(t Tool) (string, error) {
	api := os.Getenv("WFX_RELEASE_API")
	if api == "" {
		api = "https://api.github.com"
	}
	b, err := relGet(strings.TrimRight(api, "/") + "/repos/" + t.Release + "/releases/latest")
	if err != nil {
		return "", err
	}
	var r struct {
		Tag string `json:"tag_name"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.Tag == "" {
		return "", fmt.Errorf("latest release of %s: no tag", t.Release)
	}
	return strings.TrimPrefix(r.Tag, "v"), nil
}

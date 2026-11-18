package builtins

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
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
	asset, sumsFile, member := defaultAsset(t)
	if t.Asset != nil {
		asset, sumsFile, member = t.Asset(t.Version)
	}
	url := fmt.Sprintf("%s/%s/releases/download/v%s/", releaseBase(), t.Release, t.Version)
	sums, err := relGet(url + sumsFile)
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
		return fmt.Errorf("%s v%s has no %s in %s", t.Release, t.Version, asset, sumsFile)
	}
	bin, err := relGet(url + asset)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(bin)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("%s checksum mismatch: got %s, want %s", asset, got, want)
	}
	if member != "" {
		if bin, err = extract(bin, asset, member); err != nil {
			return err
		}
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

func defaultAsset(t Tool) (file, sums, member string) {
	file = fmt.Sprintf("%s_%s_%s", t.Name, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	return file, "checksums.txt", ""
}

// ghAsset: gh_<v>_<linux|macOS|windows>_<arch>.<tar.gz|zip>, bin/gh inside.
func ghAsset(v string) (file, sums, member string) {
	osn, ext := runtime.GOOS, "tar.gz"
	switch osn {
	case "darwin":
		osn, ext = "macOS", "zip"
	case "windows":
		ext = "zip"
	}
	file = fmt.Sprintf("gh_%s_%s_%s.%s", v, osn, runtime.GOARCH, ext)
	return file, fmt.Sprintf("gh_%s_checksums.txt", v), "bin/" + exe("gh")
}

// extract returns the file whose path ends in /member from a .zip or .tar.gz.
func extract(data []byte, name, member string) ([]byte, error) {
	match := func(p string) bool { return p == member || strings.HasSuffix(p, "/"+member) }
	if strings.HasSuffix(name, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if match(f.Name) {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if h.Typeflag == tar.TypeReg && match(h.Name) {
				return io.ReadAll(tr)
			}
		}
	}
	return nil, fmt.Errorf("%s has no %s", name, member)
}

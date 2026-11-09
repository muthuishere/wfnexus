package builtins

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Node.js is a default on every runner. A system node at or above
// MinNodeMajor is used as is; otherwise the current LTS from nodejs.org is
// downloaded for this OS/arch, verified against SHASUMS256.txt, unpacked into
// <dir>/node/<version> and its bin put first on PATH — no sudo.

// MinNodeMajor is LTS-2: the oldest system node accepted.
const MinNodeMajor = 20

var nodeDist = "https://nodejs.org/dist" // tests point this at a fake

var httpc = &http.Client{Timeout: 10 * time.Minute}

// NodeStatus is what ensureNode found or did.
type NodeStatus struct {
	Version string `json:"version"`
	Source  string `json:"source"` // system | bundled
	Error   string `json:"error,omitempty"`
}

func systemNodeMajor() (string, int) {
	b, err := exec.Command("node", "--version").Output()
	if err != nil {
		return "", 0
	}
	v := strings.TrimSpace(string(b))
	maj, _ := strconv.Atoi(strings.SplitN(strings.TrimPrefix(v, "v"), ".", 2)[0])
	return v, maj
}

func prependPath(dir string) {
	if p := os.Getenv("PATH"); !strings.HasPrefix(p, dir+string(os.PathListSeparator)) {
		_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+p)
	}
}

// EnsureNode makes node+npm available to this process and its steps.
func EnsureNode(dir string) NodeStatus {
	if v, maj := systemNodeMajor(); maj >= MinNodeMajor && mustLook("npm") != "" {
		src := "system"
		if strings.HasPrefix(mustLook("node"), filepath.Join(dir, "node")) {
			src = "bundled"
		}
		return NodeStatus{Version: v, Source: src}
	}
	// A node bundled at an earlier start.
	ents, _ := os.ReadDir(filepath.Join(dir, "node"))
	for i := len(ents) - 1; i >= 0; i-- {
		root := filepath.Join(dir, "node", ents[i].Name())
		if ents[i].IsDir() && !strings.HasSuffix(root, ".part") && fileExists(nodeBin(root)) {
			prependPath(filepath.Dir(nodeBin(root)))
			return NodeStatus{Version: ents[i].Name(), Source: "bundled"}
		}
	}
	v, err := installNode(dir)
	if err != nil {
		return NodeStatus{Error: "node: " + err.Error()}
	}
	return NodeStatus{Version: v, Source: "bundled"}
}

func mustLook(n string) string { p, _ := exec.LookPath(n); return p }

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func nodeBin(root string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(root, "node.exe")
	}
	return filepath.Join(root, "bin", "node")
}

func fetchBytes(url string) ([]byte, error) {
	r, err := httpc.Get(url)
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, r.Status)
	}
	return io.ReadAll(r.Body)
}

func installNode(dir string) (string, error) {
	idx, err := fetchBytes(nodeDist + "/index.json")
	if err != nil {
		return "", err
	}
	var rels []struct {
		Version string `json:"version"`
		LTS     any    `json:"lts"`
	}
	if err := json.Unmarshal(idx, &rels); err != nil {
		return "", err
	}
	ver := ""
	for _, r := range rels {
		if b, ok := r.LTS.(bool); !(ok && !b) && r.LTS != nil {
			ver = r.Version
			break
		}
	}
	if ver == "" {
		return "", fmt.Errorf("no LTS release in %s/index.json", nodeDist)
	}
	osn := map[string]string{"darwin": "darwin", "linux": "linux", "windows": "win"}[runtime.GOOS]
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if osn == "" || arch == "" {
		return "", fmt.Errorf("no node build for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	base := fmt.Sprintf("node-%s-%s-%s", ver, osn, arch)
	name := base + ".tar.gz"
	if osn == "win" {
		name = base + ".zip"
	}
	sums, err := fetchBytes(fmt.Sprintf("%s/%s/SHASUMS256.txt", nodeDist, ver))
	if err != nil {
		return "", err
	}
	want := ""
	for _, l := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(l); len(f) == 2 && f[1] == name {
			want = f[0]
		}
	}
	if want == "" {
		return "", fmt.Errorf("no checksum for %s", name)
	}
	blob, err := fetchBytes(fmt.Sprintf("%s/%s/%s", nodeDist, ver, name))
	if err != nil {
		return "", err
	}
	if h := sha256.Sum256(blob); hex.EncodeToString(h[:]) != want {
		return "", fmt.Errorf("checksum mismatch for %s", name)
	}
	dest := filepath.Join(dir, "node", ver)
	tmp := dest + ".part"
	_ = os.RemoveAll(tmp)
	if osn == "win" {
		err = unzip(blob, tmp, base+"/")
	} else {
		err = untgz(blob, tmp, base+"/")
	}
	if err != nil {
		return "", err
	}
	_ = os.RemoveAll(dest)
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	prependPath(filepath.Dir(nodeBin(dest)))
	return ver, nil
}

// untgz unpacks a node tarball, stripping its top directory.
func untgz(blob []byte, dest, strip string) error {
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(h.Name, strip)
		if rel == "" || strings.Contains(rel, "..") {
			continue
		}
		p := filepath.Join(dest, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(p, 0o755)
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.Symlink(h.Linkname, p)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
}

func unzip(blob []byte, dest, strip string) error {
	zr, err := zip.NewReader(bytes.NewReader(blob), int64(len(blob)))
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		rel := strings.TrimPrefix(f.Name, strip)
		if rel == "" || strings.Contains(rel, "..") {
			continue
		}
		p := filepath.Join(dest, filepath.FromSlash(rel))
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(p, 0o755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// PythonVersion reports python3 if present (informational; not required).
func PythonVersion() string {
	for _, n := range []string{"python3", "python"} {
		if b, err := exec.Command(n, "--version").CombinedOutput(); err == nil {
			return strings.TrimSpace(strings.TrimPrefix(string(b), "Python "))
		}
	}
	return ""
}

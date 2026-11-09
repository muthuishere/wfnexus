package builtins

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeNpm puts an `npm` on PATH that "installs" <pkg>@<ver> by writing its
// package.json and a launcher into the prefix, and answers `npm view`.
func fakeNpm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake")
	}
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = view ]; then echo 9.9.9; exit 0; fi
pre=""; spec=""
while [ $# -gt 0 ]; do case "$1" in --prefix) pre="$2"; shift;; @*) spec="$1";; esac; shift; done
pkg="${spec%@*}"; ver="${spec##*@}"; name="${pkg#@muthuishere/}"
mkdir -p "$pre/node_modules/$pkg" "$pre/node_modules/.bin"
echo "{\"version\":\"$ver\"}" > "$pre/node_modules/$pkg/package.json"
printf '#!/bin/sh\necho %s %s\n' "$name" "$ver" > "$pre/node_modules/.bin/$name"
echo "$spec" >> "$pre/installs.log"
`
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("PATH")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+old)
}

func TestEnsureInstallsUpdatesAndRollsBack(t *testing.T) {
	fakeNpm(t)
	dir := t.TempDir()
	for _, s := range Ensure(dir, nil)[:len(Pinned)] {
		if s.Error != "" || !strings.HasSuffix(s.Path, s.Name) {
			t.Fatalf("%+v", s)
		}
	}
	if !strings.HasPrefix(os.Getenv("PATH"), BinDir(dir)) {
		t.Fatal("tools bin is not first on PATH")
	}
	Ensure(dir, nil) // already there: no reinstall
	if b, _ := os.ReadFile(filepath.Join(dir, "tools", "installs.log")); strings.Count(string(b), "\n") != 3 {
		t.Fatalf("reinstalled: %s", b)
	}
	if v, err := Latest(Pinned[0]); err != nil || v != "9.9.9" {
		t.Fatalf("latest %q %v", v, err)
	}
	_ = SetDesired(dir, "aos", "v9.9.9")
	if st := Ensure(dir, Desired(dir)); st[0].Version != "9.9.9" || installed(dir, Pinned[0]) != "9.9.9" {
		t.Fatalf("update: %+v", st[0])
	}
	_ = SetDesired(dir, "aos", Pinned[0].Version)
	if Ensure(dir, Desired(dir)); installed(dir, Pinned[0]) != Pinned[0].Version {
		t.Fatal("rollback did not reinstall the older version")
	}
}

func TestEnsureSaysWhenNodeCannotBeHad(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	old := nodeDist
	nodeDist = srv.URL
	defer func() { nodeDist = old }()
	t.Setenv("PATH", t.TempDir())
	st := Ensure(t.TempDir(), nil)
	for _, s := range st[:len(Pinned)] {
		if s.Error != NoNpm {
			t.Fatalf("%+v", s)
		}
	}
	if n := st[len(Pinned)]; n.Name != "node" || !strings.Contains(n.Error, "node:") {
		t.Fatalf("node row %+v", n)
	}
}

// No usable node: the LTS tarball is fetched, checksum-verified, unpacked
// under <dir>/node/<ver> and put on PATH.
func TestEnsureNodeBundlesLTS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tarball fake")
	}
	osn := map[string]string{"darwin": "darwin", "linux": "linux"}[runtime.GOOS]
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	base := "node-v24.0.0-" + osn + "-" + arch
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	body := []byte("#!/bin/sh\necho v24.0.0\n")
	_ = tw.WriteHeader(&tar.Header{Name: base + "/bin/node", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = zw.Close()
	h := sha256.Sum256(buf.Bytes())
	sums := hex.EncodeToString(h[:]) + "  " + base + ".tar.gz\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/index.json":
			w.Write([]byte(`[{"version":"v25.0.0","lts":false},{"version":"v24.0.0","lts":"Krypton"}]`))
		case strings.HasSuffix(r.URL.Path, "SHASUMS256.txt"):
			w.Write([]byte(sums))
		case strings.HasSuffix(r.URL.Path, base+".tar.gz"):
			w.Write(buf.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := nodeDist
	nodeDist = srv.URL
	defer func() { nodeDist = old }()
	t.Setenv("PATH", "/usr/bin:/bin")
	if _, err := exec.LookPath("node"); err == nil {
		t.Skip("a system node is on /usr/bin")
	}
	dir := t.TempDir()
	ns := EnsureNode(dir)
	if ns.Error != "" || ns.Version != "v24.0.0" || ns.Source != "bundled" {
		t.Fatalf("%+v", ns)
	}
	if p, _ := exec.LookPath("node"); !strings.HasPrefix(p, filepath.Join(dir, "node", "v24.0.0")) {
		t.Fatalf("node on PATH: %q", p)
	}
}

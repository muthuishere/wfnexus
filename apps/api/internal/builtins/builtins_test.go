package builtins

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tgz(name string, body []byte) []byte {
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	tw := tar.NewWriter(zw)
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = zw.Close()
	return b.Bytes()
}

// fakeReleases serves every pinned tool at any version; corrupt names an asset
// whose checksum will not match.
func fakeReleases(t *testing.T, corrupt string) {
	files := map[string][]byte{}
	sums := map[string]string{}
	add := func(dir, name string, blob []byte) {
		files[dir+name] = blob
		h := sha256.Sum256(blob)
		sums[dir] += hex.EncodeToString(h[:]) + "  " + name + "\n"
	}
	for _, tool := range Pinned {
		for _, v := range []string{tool.Version, "9.9.9"} {
			tl := tool
			tl.Version = v
			dir := fmt.Sprintf("/%s/releases/download/v%s/", tool.Repo, v)
			body := []byte("#!/bin/sh\necho " + tool.Name + " " + v + "\n")
			if tool.Raw {
				add(dir, asset(tl), body)
			} else {
				add(dir, asset(tl), tgz(exe(tool.Name), body))
			}
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			w.Write([]byte(`{"tag_name":"v9.9.9"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "checksums.txt") {
			w.Write([]byte(sums[strings.TrimSuffix(r.URL.Path, "checksums.txt")]))
			return
		}
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if corrupt != "" && strings.Contains(r.URL.Path, corrupt) {
			b = append([]byte("x"), b...)
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	old := base
	base = srv.URL
	t.Cleanup(func() { base = old })
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })
}

func TestEnsureFetchesVerifiesLinksAndUpdates(t *testing.T) {
	fakeReleases(t, "")
	dir := t.TempDir()
	st := Ensure(dir, nil)
	for _, s := range st {
		if s.Error != "" {
			t.Fatalf("%s: %s", s.Name, s.Error)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "bin", exe(s.Name)))
		if !strings.Contains(string(b), s.Name+" "+s.Version) {
			t.Fatalf("%s linked %q", s.Name, b)
		}
	}
	if !strings.HasPrefix(os.Getenv("PATH"), filepath.Join(dir, "bin")) {
		t.Fatal("bin dir is not first on PATH")
	}
	// Update one tool; the old version stays for a rollback.
	if v, err := Latest(Pinned[0]); err != nil || v != "9.9.9" {
		t.Fatalf("latest %q %v", v, err)
	}
	if err := SetDesired(dir, Pinned[0].Name, "v9.9.9"); err != nil {
		t.Fatal(err)
	}
	st = Ensure(dir, Desired(dir))
	if st[0].Version != "9.9.9" || st[0].Error != "" {
		t.Fatalf("update: %+v", st[0])
	}
	if _, err := os.Stat(filepath.Join(dir, Pinned[0].Name, Pinned[0].Version)); err != nil {
		t.Fatal("previous version was not kept")
	}
}

func TestEnsureRefusesABadChecksum(t *testing.T) {
	fakeReleases(t, "jevx")
	st := Ensure(t.TempDir(), nil)
	for _, s := range st {
		if s.Name == "jevx" && !strings.Contains(s.Error, "checksum mismatch") {
			t.Fatalf("jevx: %+v", s)
		}
	}
}

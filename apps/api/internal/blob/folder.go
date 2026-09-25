package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A FOLDER INSTEAD OF A BUCKET.
//
// A personal install should not need MinIO running to store a diff. Artifacts
// are a transcript and a patch — small, local, and nobody's idea of object
// storage — so on a laptop they are files in a directory and that is the whole
// implementation.
//
// The bucket driver stays for a deployment, where artifacts outlive the machine
// and several processes read them. Which one runs is one line of config.

// ErrNoPresign means this store cannot hand out a URL another process could
// fetch. It is a fact about the driver, not a failure — the caller serves the
// bytes itself.
var ErrNoPresign = errors.New("this artifact store cannot presign")

// Folder stores artifacts as files under Dir.
type Folder struct{ dir string }

// OpenFolder prepares the directory. It is created if absent, because the
// alternative — failing at the first artifact, an hour into a run — is a worse
// way to learn the directory was missing.
func OpenFolder(dir string) (*Folder, error) {
	if dir == "" {
		return nil, fmt.Errorf("artifact folder: no directory configured")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("artifact folder: %w", err)
	}
	return &Folder{dir: dir}, nil
}

// Dir is where this store keeps its files. A refusal that cannot say where it
// looked is a refusal nobody can act on.
func (f *Folder) Dir() string { return f.dir }

// pathFor maps an object key to a file, refusing anything that would climb out
// of the directory. Keys are built by this codebase, not by a user — but a
// store that can be made to write anywhere is worth closing whether or not the
// current callers would.
func (f *Folder) pathFor(key string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(key))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact key %q escapes the store", key)
	}
	return filepath.Join(f.dir, clean), nil
}

func (f *Folder) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	path, err := f.pathFor(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Written to a temporary file and renamed, so a reader never sees a
	// half-written artifact and a crash leaves no truncated one.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wfx-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (f *Folder) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	path, err := f.pathFor(key)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

// PresignedGet has no meaning for a folder: there is no second service to hand
// a URL to, and inventing one would point the browser at a route that does not
// exist. It says so, and the caller streams the bytes instead.
func (f *Folder) PresignedGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return "", ErrNoPresign
}

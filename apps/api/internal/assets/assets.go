// Package assets carries the defaults a downloaded binary needs to be useful
// on its own: the built UI bundle, and the shipped templates/, skills/ and
// registries.json.
//
// WHY: until now `wfx-server` only worked with the repository's files sitting
// beside it, so the only complete install was the tarball or the container. A
// single binary someone downloads has nothing beside it, and a server that
// serves a 404 at `/` and knows no providers is not a server.
//
// THE PRECEDENCE RULE — DISK WINS, ALWAYS:
//
//  1. A path stated explicitly (env var or config file) is authoritative. It
//     is used if it exists; if it does not, that is REPORTED, loudly, never
//     silently replaced — someone who names a directory meant that directory.
//  2. Otherwise, an existing directory at the default path (beside the binary,
//     i.e. under WFX_ROOT) wins. That is the repository checkout, the tarball
//     and the container image: all three keep behaving exactly as before,
//     because the embedded copy is never consulted while real files exist.
//  3. Only when neither exists does the embedded copy come out — extracted
//     ONCE into the per-user data directory, and never overwriting a file that
//     is already there.
//
// The embedded copy is a FALLBACK, never a shadow: it cannot mask a user's
// files, because it is only reached when there are no user files to mask, and
// extraction skips anything that already exists on disk.
//
// The UI is the one thing served straight from the embedded filesystem rather
// than extracted — it is read-only, nobody edits a hashed Vite bundle in place,
// and writing a few megabytes of JS into someone's home directory to serve it
// back would be pure ceremony.
package assets

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// embedded holds whatever `task assets:stage` copied in before the build.
// `all:` so nothing is dropped for starting with a dot or an underscore — Vite
// emits `.vite/`, and a skill directory may carry dotfiles.
//
// A build that never staged still compiles (the directory holds a committed
// PLACEHOLDER.md) and simply reports HasUI() == false. That case is what the
// build tasks exist to make impossible for a release.
//
//go:embed all:embedded
var embedded embed.FS

// Names of the staged trees, as `task assets:stage` writes them.
const (
	UIName         = "ui"
	TemplatesName  = "templates"
	SkillsName     = "skills"
	RegistriesName = "registries.json"
)

// UI returns the embedded bundle and whether one was actually built in.
func UI() (fs.FS, bool) {
	// fs paths are always slash-separated, even on Windows.
	sub, err := fs.Sub(embedded, path.Join("embedded", UIName))
	if err != nil {
		return nil, false
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}

// DefaultsDir is where embedded defaults are extracted when there is nothing
// on disk. It sits with the rest of the runtime state, outside any repository:
// code in the repo, runtime outside it.
func DefaultsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "wfnexus", "defaults")
	}
	return filepath.Join(home, ".local", "share", "wfnexus", "defaults")
}

// Origin says where a resolved path came from, for the boot log. The point of
// printing it is that "which templates am I running?" should never be a guess.
type Origin string

const (
	FromDisk     Origin = "disk"
	FromEmbedded Origin = "embedded"
	Missing      Origin = "missing"
)

// Resolve applies the precedence rule above to one directory or file.
//
// `explicit` means the operator named this path (env var or config file).
// An explicit path that does not exist yields a non-empty `warn` and still
// falls back, so the binary runs but the mistake is on the first screen of
// output instead of surfacing as an empty catalogue three commands later.
func Resolve(name, configured string, explicit bool) (path string, origin Origin, warn string, err error) {
	if configured != "" {
		if _, statErr := os.Stat(configured); statErr == nil {
			return configured, FromDisk, "", nil
		} else if explicit {
			warn = fmt.Sprintf("%s: configured path %s does not exist — falling back to the copy built into this binary", name, configured)
		}
	}
	extracted, err := Extract(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Nothing on disk and nothing embedded: say so rather than
			// pretending an empty directory is a set of defaults.
			return "", Missing, warn, nil
		}
		return "", Missing, warn, err
	}
	return extracted, FromEmbedded, warn, nil
}

// Extract writes the embedded copy of `name` under DefaultsDir and returns the
// path. It NEVER overwrites a file that already exists: the extracted tree is
// the user's the moment it lands, and a later upgrade must not silently undo an
// edit they made to it. New files from a newer binary are added.
func Extract(name string) (string, error) {
	src := path.Join("embedded", name)
	info, err := fs.Stat(embedded, src)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(DefaultsDir(), name)
	if !info.IsDir() {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		return dst, copyFile(src, dst)
	}
	err = fs.WalkDir(embedded, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.FromSlash(src), filepath.FromSlash(p))
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		return copyFile(p, out)
	})
	if err != nil {
		return "", err
	}
	return dst, nil
}

// copyFile writes one embedded file, leaving an existing one untouched.
func copyFile(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	in, err := embedded.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	// O_EXCL: two servers starting at once must not half-write the same file.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// EnsureWorkflowsDir keeps WorkflowsDir honest. Workflows are USER DATA — they
// are written, edited and imported — so they are never served from the binary.
// But a downloaded binary run in an empty directory used to die at boot on
// `open ./workflows: no such file or directory`, which is a bad first minute.
//
// So: create it. If the configured path cannot be created (read-only mount,
// say), fall back to the per-user runs directory and SAY so — the one thing
// this must not do is quietly load a different directory than the one that was
// configured.
func EnsureWorkflowsDir(configured string, explicit bool) (path string, warn string, err error) {
	if configured == "" {
		configured = filepath.Join(filepath.Dir(DefaultsDir()), "workflows")
	}
	if _, statErr := os.Stat(configured); statErr == nil {
		return configured, "", nil
	}
	if mkErr := os.MkdirAll(configured, 0o755); mkErr == nil {
		return configured, fmt.Sprintf("workflows: created %s (it did not exist)", configured), nil
	} else if explicit {
		warn = fmt.Sprintf("workflows: configured path %s could not be created (%v)", configured, mkErr)
	}
	fallback := filepath.Join(filepath.Dir(DefaultsDir()), "workflows")
	if err := os.MkdirAll(fallback, 0o755); err != nil {
		return "", warn, err
	}
	if warn == "" {
		warn = fmt.Sprintf("workflows: %s unusable — using %s", configured, fallback)
	} else {
		warn += " — using " + fallback
	}
	return fallback, warn, nil
}

package bundle

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// A published bundle is a COMMITTED TREE (design §1): one directory per
// version, holding exactly the files the tar carries, under the same fixed
// names. The tarball is derived from it, because entry digests are defined over
// canonical content rather than over tar ordering, mtimes or permissions.
//
// A reviewer reads a tree in a PR diff. A reviewer reads nothing at all out of
// a committed tarball.

// VersionsDir is where bundles live in a repository.
const VersionsDir = "workflows"

// TreeDir is the directory one published bundle occupies in a repository.
func TreeDir(name, version string) string { return path.Join(VersionsDir, name, version) }

// FromTree reads a published bundle out of a checked-out directory. It does
// NOT verify — Verify is the caller's, so the same recomputation runs whether
// the bytes came from a tar, an upload or a git checkout.
func FromTree(dir string) (*Bundle, error) {
	b := &Bundle{Files: map[string][]byte{}}
	raw, err := os.ReadFile(filepath.Join(dir, ManifestPath))
	if err != nil {
		return nil, fmt.Errorf("bundle: %s: %w", ManifestPath, err)
	}
	if err := jsonUnmarshalStrict(raw, &b.Manifest); err != nil {
		return nil, fmt.Errorf("bundle: %s: %w", ManifestPath, err)
	}
	err = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if name == ManifestPath {
			return nil // the manifest is the description, not a carried file
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if len(content) > maxEntry {
			return fmt.Errorf("bundle: %s is larger than %d bytes", name, maxEntry)
		}
		b.Files[name] = content
		return nil
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// FindInTree lists every published bundle in a checkout, as
// "<name>/<version>" directories under workflows/.
func FindInTree(root string) ([]string, error) {
	base := filepath.Join(root, VersionsDir)
	names, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if !n.IsDir() {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(base, n.Name()))
		if err != nil {
			return nil, err
		}
		for _, v := range versions {
			if !v.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(base, n.Name(), v.Name(), ManifestPath)); err != nil {
				continue
			}
			out = append(out, n.Name()+"/"+v.Name())
		}
	}
	return out, nil
}

// SelectInTree picks the one bundle a reference means. With no selector the
// repository must hold exactly one; otherwise the `#fragment` names it, by
// "<name>" or "<name>/<version>".
func SelectInTree(root, selector string) (string, error) {
	found, err := FindInTree(root)
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", fmt.Errorf("this checkout holds no published bundle: %s/<name>/<version>/%s is absent", VersionsDir, ManifestPath)
	}
	if selector == "" {
		if len(found) == 1 {
			return filepath.Join(root, VersionsDir, filepath.FromSlash(found[0])), nil
		}
		return "", fmt.Errorf("this checkout holds %d bundles (%s); name one with a #fragment, as in acme/workflows@v1.2.0#%s",
			len(found), strings.Join(found, ", "), strings.SplitN(found[0], "/", 2)[0])
	}
	var match []string
	for _, f := range found {
		if f == selector || strings.SplitN(f, "/", 2)[0] == selector {
			match = append(match, f)
		}
	}
	switch len(match) {
	case 1:
		return filepath.Join(root, VersionsDir, filepath.FromSlash(match[0])), nil
	case 0:
		return "", fmt.Errorf("no bundle %q in this checkout; it holds: %s", selector, strings.Join(found, ", "))
	default:
		return "", fmt.Errorf("%q matches %s; name the version too, as in #%s", selector, strings.Join(match, ", "), match[0])
	}
}

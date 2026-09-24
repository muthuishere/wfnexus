package workflow

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)

// ValidName keeps an authored name safe as a filename and as a URL segment.
func ValidName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("workflow name %q must be lowercase letters, digits and dashes (2-64 chars)", name)
	}
	return nil
}

// Check validates a definition without writing anything — the authoritative
// verdict for an authoring UI, which should not have to create a file to learn
// whether what it built is legal.
func Check(d *Definition, cat Catalog) error {
	if err := ValidName(d.Name); err != nil {
		return err
	}
	normalize(d)
	if err := d.validate(cat); err != nil {
		return err
	}
	// A credential written as a literal is refused here, where it is still only
	// in memory — the next line marshals this to YAML and writes it.
	if err := CheckEnv(d.Name, d.Env); err != nil {
		return err
	}
	for i := range d.Steps {
		if err := CheckEnv(d.Name+"/"+d.Steps[i].ID, d.Steps[i].Env); err != nil {
			return err
		}
	}
	raw, err := yaml.Marshal(d)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "bfp-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if _, err := writeForm(tmp, d, raw, nil); err != nil {
		return err
	}
	loaded, err := LoadDir(tmp, cat)
	if err != nil {
		return fmt.Errorf("the definition does not survive a round trip: %w", err)
	}
	if _, ok := loaded[d.Name]; !ok {
		return fmt.Errorf("the definition did not load back under %q", d.Name)
	}
	return nil
}

// Save validates a definition and, only if it is loadable, writes it to dir as
// YAML. Validation runs against a temporary copy, so a rejected definition
// never lands on disk and cannot break the next boot.
func Save(dir string, d *Definition, cat Catalog) (string, error) {
	if err := ValidName(d.Name); err != nil {
		return "", err
	}
	normalize(d)
	if err := d.validate(cat); err != nil {
		return "", err
	}

	raw, err := yaml.Marshal(d)
	if err != nil {
		return "", err
	}
	// Round-trip through the real loader, in whichever FORM this workflow has:
	// a directory-form workflow whose sidecars were dropped on the way through
	// would pass a flat-file round trip and still be broken.
	tmp, err := os.MkdirTemp("", "bfp-workflow-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if _, err := writeForm(tmp, d, raw, nil); err != nil {
		return "", err
	}
	loaded, err := LoadDir(tmp, cat)
	if err != nil {
		return "", fmt.Errorf("the definition does not survive a round trip: %w", err)
	}
	back, ok := loaded[d.Name]
	if !ok {
		return "", fmt.Errorf("the definition did not load back under %q", d.Name)
	}
	if len(back.Files) != len(d.Files) {
		return "", fmt.Errorf("the workflow's files did not survive a round trip: wrote %d, read back %d",
			len(d.Files), len(back.Files))
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	header := []byte("# Managed by the wfnexus workflow builder.\n" +
		"# Hand edits are fine; the builder round-trips through the same loader.\n")
	return writeForm(dir, d, raw, header)
}

// writeForm writes a workflow in the form it has, and returns the path of its
// definition.
//
// The DIRECTORY form is used when the workflow carries files, or when it is
// already a directory on disk — so saving from the Builder never quietly
// demotes a `workflows/report/` into a flat file and deletes the run.js beside
// it. Everything else stays a plain `<name>.yaml`, exactly as before.
func writeForm(dir string, d *Definition, raw, header []byte) (string, error) {
	wfDir := filepath.Join(dir, d.Name)
	if len(d.Files) == 0 && !dirExists(wfDir) {
		path := filepath.Join(dir, d.Name+".yaml")
		return path, os.WriteFile(path, append(append([]byte{}, header...), raw...), 0o644)
	}
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		return "", err
	}
	// The files that are no longer part of the workflow go, so a rename in the
	// Builder does not leave the old script behind for a step to pick up.
	keep := map[string]bool{DefinitionFile: true, "workflow.yml": true}
	for _, f := range d.Files {
		keep[filepath.Clean(filepath.FromSlash(f.Path))] = true
	}
	_ = filepath.WalkDir(wfDir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		if rel, relErr := filepath.Rel(wfDir, p); relErr == nil && !keep[rel] && !keep[filepath.ToSlash(rel)] {
			_ = os.Remove(p)
		}
		return nil
	})
	for _, f := range d.Files {
		// CheckFiles already refused anything that climbs out; this is the
		// belt-and-braces at the point of writing, because this function is
		// what actually creates files from a path that arrived over the API.
		clean := filepath.Clean(filepath.FromSlash(f.Path))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("file %q escapes the workflow directory", f.Path)
		}
		dest := filepath.Join(wfDir, clean)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		mode := os.FileMode(f.Mode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(dest, f.Body, mode); err != nil {
			return "", err
		}
	}
	path := filepath.Join(wfDir, DefinitionFile)
	return path, os.WriteFile(path, append(append([]byte{}, header...), raw...), 0o644)
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// Delete removes a workflow file. It refuses a name that is not a plain
// workflow name, so a path can never escape the workflows directory.
func Delete(dir, name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	// A directory-form workflow is deleted whole: the definition AND the files
	// that only exist to serve it. Leaving a `report/run.js` behind after
	// deleting `report` is litter that the next workflow of that name inherits.
	if d := filepath.Join(dir, name); dirExists(d) && fileExists(filepath.Join(d, DefinitionFile)) {
		if !strings.HasPrefix(filepath.Clean(d), filepath.Clean(dir)+string(filepath.Separator)) {
			return fmt.Errorf("refusing to delete outside the workflows directory")
		}
		return os.RemoveAll(d)
	}
	path := filepath.Join(dir, name+".yaml")
	if _, err := os.Stat(path); err != nil {
		if alt := filepath.Join(dir, name+".yml"); fileExists(alt) {
			path = alt
		} else {
			return fmt.Errorf("workflow %q not found", name)
		}
	}
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(dir)+string(filepath.Separator)) {
		return fmt.Errorf("refusing to delete outside the workflows directory")
	}
	return os.Remove(path)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

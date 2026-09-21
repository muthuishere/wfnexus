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
	// Round-trip through the real loader: what is written must be what loads.
	tmp, err := os.MkdirTemp("", "bfp-workflow-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := os.WriteFile(filepath.Join(tmp, d.Name+".yaml"), raw, 0o644); err != nil {
		return "", err
	}
	loaded, err := LoadDir(tmp, cat)
	if err != nil {
		return "", fmt.Errorf("the definition does not survive a round trip: %w", err)
	}
	if _, ok := loaded[d.Name]; !ok {
		return "", fmt.Errorf("the definition did not load back under %q", d.Name)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, d.Name+".yaml")
	header := "# Managed by the bug-fixer-platform workflow builder.\n" +
		"# Hand edits are fine; the builder round-trips through the same loader.\n"
	if err := os.WriteFile(path, append([]byte(header), raw...), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Delete removes a workflow file. It refuses a name that is not a plain
// workflow name, so a path can never escape the workflows directory.
func Delete(dir, name string) error {
	if err := ValidName(name); err != nil {
		return err
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

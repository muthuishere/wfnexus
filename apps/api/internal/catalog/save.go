package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// Saving a registry entry goes through the SAME validation the loader uses, and
// then through a real reload, so the API cannot accept something the loader
// would skip. That is the rule ADR 0013 states for workflows, applied to the
// registries: one validation path, or the two drift and the UI starts accepting
// entries that silently never load.
//
// Values are never written here. A provider stores the NAME of its key
// variable (ADR 0011), so registries.json stays a file that can be committed
// and pasted into a bug report.

// SaveProvider writes or replaces one provider in registriesPath.
func SaveProvider(registriesPath string, p Provider) error {
	if p.Name == "" {
		return fmt.Errorf("a provider needs a name")
	}
	if err := validateProvider(p); err != nil {
		return err
	}
	if looksLikeSecret(p.APIKeyEnv) {
		return fmt.Errorf("apiKeyEnv must be the NAME of an environment variable, not a value")
	}
	return mutate(registriesPath, func(f *file) {
		if f.Providers == nil {
			f.Providers = map[string]Provider{}
		}
		stored := p
		stored.Name = "" // the key IS the name; storing both lets them disagree
		f.Providers[p.Name] = stored
	})
}

// SaveClassifier writes or replaces one classifier.
func SaveClassifier(registriesPath string, c Classifier) error {
	if c.Name == "" {
		return fmt.Errorf("a classifier needs a name")
	}
	if err := validateClassifier(c); err != nil {
		return err
	}
	if looksLikeSecret(c.APIKeyEnv) {
		return fmt.Errorf("apiKeyEnv must be the NAME of an environment variable, not a value")
	}
	return mutate(registriesPath, func(f *file) {
		if f.Classifiers == nil {
			f.Classifiers = map[string]Classifier{}
		}
		stored := c
		stored.Name = ""
		f.Classifiers[c.Name] = stored
	})
}

// DeleteProvider removes a provider. A workflow still naming it will fail to
// load on the next reload, which is the loud outcome — better than a step
// silently falling back to a different model.
func DeleteProvider(registriesPath, name string) error {
	return mutate(registriesPath, func(f *file) { delete(f.Providers, name) })
}

// DeleteClassifier removes a classifier.
func DeleteClassifier(registriesPath, name string) error {
	return mutate(registriesPath, func(f *file) { delete(f.Classifiers, name) })
}

// looksLikeSecret rejects the mistake that matters: pasting a key into the
// field that is supposed to hold a variable's name. An env var name has no
// spaces and no punctuation beyond underscores; a key has both, or is simply
// far too long.
//
// It is a guard against a slip, not against a determined author — but this is
// the one field where a slip writes a live credential into a file destined for
// version control, so the cheap check is worth having.
func looksLikeSecret(v string) bool {
	if v == "" {
		return false
	}
	if len(v) > 64 {
		return true
	}
	for _, r := range v {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return true
		}
	}
	return false
}

// keyOrder is the order top-level keys are written in: the explanation first,
// then the registries in the order a reader meets them.
//
// Marshalling a map sorts its keys, which put `classifiers` above `providers`
// and produced a 54-line diff for a write that changed nothing. A file a person
// maintains should not be reshuffled by an unrelated edit — the diff is how
// they review it.
var keyOrder = []string{"_comment", "providers", "classifiers", "mcpServers"}

// encodeOrdered writes the object with keyOrder first and anything else after,
// alphabetically, so an unrecognised key is preserved in a stable position.
func encodeOrdered(obj map[string]json.RawMessage) ([]byte, error) {
	rest := make([]string, 0, len(obj))
	for k := range obj {
		if !slices.Contains(keyOrder, k) {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)

	var b bytes.Buffer
	b.WriteString("{\n")
	first := true
	write := func(k string) error {
		v, ok := obj[k]
		if !ok {
			return nil
		}
		if !first {
			b.WriteString(",\n")
		}
		first = false
		key, _ := json.Marshal(k)
		b.WriteString("  ")
		b.Write(key)
		b.WriteString(": ")
		// Re-indent the value to sit two spaces in, so the file reads the same
		// as a hand-written one.
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, v, "  ", "  "); err != nil {
			return err
		}
		b.Write(pretty.Bytes())
		return nil
	}
	for _, k := range append(append([]string{}, keyOrder...), rest...) {
		if err := write(k); err != nil {
			return nil, err
		}
	}
	b.WriteString("\n}")
	return b.Bytes(), nil
}

// mutate reads registries.json, applies a change and writes it back atomically.
// Everything it does not know about is preserved: the file is decoded into the
// same struct the loader uses plus a bag of unknown keys, so a hand-written
// comment block survives a UI edit.
func mutate(path string, apply func(*file)) error {
	if path == "" {
		return fmt.Errorf("no registries path is configured")
	}
	// Unknown keys are carried through verbatim. registries.json opens with a
	// `_comment` array explaining the file; an edit that silently deleted it
	// would be a small betrayal of whoever wrote it.
	var extra map[string]json.RawMessage
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &extra); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	f, err := readFile(path)
	if err != nil {
		return err
	}
	apply(&f)

	for k, v := range map[string]any{"providers": f.Providers, "classifiers": f.Classifiers, "mcpServers": f.McpServers} {
		if v == nil {
			continue
		}
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		if extra == nil {
			extra = map[string]json.RawMessage{}
		}
		extra[k] = b
	}

	out, err := encodeOrdered(extra)
	if err != nil {
		return err
	}
	// Written via a temp file in the same directory and renamed, so a crash
	// mid-write cannot leave a half-parsed registry that stops the server
	// booting.
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, append(out, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

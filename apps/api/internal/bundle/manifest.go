// Package bundle is the published unit: a workflow plus every skill and MCP
// declaration its steps name, resolved on the PUBLISHING machine and carried
// with it (ADR 0018).
//
// The vocabulary is OCI's — manifest, entry digest, `sha256:`, an immutable
// version and a moving tag — because it is the vocabulary people already have
// for content-addressed artifacts. OCI's /v2/ distribution API is deliberately
// NOT adopted: we are not serving container runtimes, and that endpoint set is
// a large surface for one client.
package bundle

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SchemaVersion is the manifest's own version, so a later format change is a
// refusal to read rather than a misread.
const SchemaVersion = 1

// Paths inside the tar. Fixed names, because a manifest that could name its own
// members would make two bundles with identical content disagree on their digest.
const (
	ManifestPath = "manifest.json"
	WorkflowPath = "workflow.yaml"
	McpPath      = "mcp.json"
	SkillsDir    = "skills"
)

// Entry is one carried dependency and its digest. Files is the canonical
// listing a directory's digest was computed over — present for a skill, absent
// for a single file — so a reader can say exactly which file differs.
type Entry struct {
	Name   string   `json:"name"`
	Path   string   `json:"path"`
	Digest string   `json:"digest"`
	Files  []string `json:"files,omitempty"`
}

// Requirement is one thing the RECEIVING host must already have for this
// bundle's steps to run. It is gathered at publish time from the steps
// themselves, because the receiving host cannot infer it: a provider NAME says
// nothing about whether it needs an API key or a binary installed locally, and
// a `runs-on:` label nobody holds means a step that waits forever.
//
// It records a REQUIREMENT AND NEVER A CREDENTIAL. `provider: claude-cli` means
// *that machine's* subscription; the platform can hold neither the seat nor the
// login, so the only honest thing to carry is the name of what is needed, and
// the receiving host's only answer is "I have it" or "I do not".
type Requirement struct {
	// Kind is what sort of thing this is: "provider", "label" or "mcp".
	Kind string `json:"kind"`
	// Name is the entry's name as the step spells it.
	Name string `json:"name"`
	// ProviderKind is "http", "cli" or "acp" for a provider requirement — the
	// part that decides whether a key or a binary satisfies it. Empty otherwise.
	ProviderKind string `json:"providerKind,omitempty"`
	// APIKeyEnv is the NAME of the environment variable the PUBLISHER's `http`
	// provider read its key from — never the key. It is the same NAME
	// `catalog.Provider` already carries and `LooksLikeSecret` already refuses a
	// value in, so it travels for the same reason it is safe to store.
	//
	// It is a HINT and never a rule. The receiving host reads its OWN variable
	// for its own provider entry; a published bundle does not get to dictate the
	// consuming machine's environment. What the name buys is an actionable
	// refusal — "a key is needed, and where this was published it was called
	// ANTHROPIC_API_KEY" — instead of one that is merely true.
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
	// Steps are the step IDs that ask for it, so a refusal can say where.
	Steps []string `json:"steps,omitempty"`
}

// Requirement kinds.
const (
	ReqProvider = "provider"
	ReqLabel    = "label"
	ReqMcp      = "mcp"
)

// Manifest names every entry, so the manifest's own digest covers the whole
// bundle transitively. That is the OCI arrangement and the reason the bundle
// digest is one hash of one small document.
type Manifest struct {
	SchemaVersion int     `json:"schemaVersion"`
	Kind          string  `json:"kind"`
	Project       string  `json:"project"`
	Name          string  `json:"name"`
	Version       string  `json:"version"`
	Workflow      Entry   `json:"workflow"`
	Skills        []Entry `json:"skills,omitempty"`
	Mcp           *Entry  `json:"mcp,omitempty"`
	// Requires is what this bundle needs to RUN on the receiving host (design
	// §7). A bundle that names no provider, label or MCP server records nothing.
	Requires []Requirement `json:"requires,omitempty"`
}

// Canonical is the manifest's bytes for digest purposes: keys sorted, no
// insignificant whitespace. It goes through a generic decode so the ordering
// comes from the JSON encoder's map-key sort rather than from Go's struct
// field order, which a later field reshuffle would change.
func (m *Manifest) Canonical() ([]byte, error) {
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return json.Marshal(generic)
}

// Digest is the bundle's identity: sha256 of the canonical manifest.
func (m *Manifest) Digest() (string, error) {
	b, err := m.Canonical()
	if err != nil {
		return "", err
	}
	return DigestBytes(b), nil
}

// DigestBytes is the one hash function in this package.
func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + fmt.Sprintf("%x", sum)
}

// Hex is the digest without its algorithm prefix — what a blob key is built
// from, because a key with a colon in it is a needless portability problem.
func Hex(digest string) string { return strings.TrimPrefix(digest, "sha256:") }

// DigestFile is the entry digest of a single file: the hash of its bytes.
func DigestFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return DigestBytes(b), nil
}

// DirListing is a directory's canonical content listing: one
// "<digest>  <relative/slash/path>\n" line per file, sorted by path.
//
// Sorting by path and hashing content is what makes a skill's digest depend on
// its CONTENTS alone — not on tar ordering, not on mtimes, not on permissions,
// all three of which differ between two checkouts of the same tree.
func DirListing(dir string) ([]string, error) {
	var lines []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
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
		dg, err := DigestFile(p)
		if err != nil {
			return err
		}
		lines = append(lines, dg+"  "+filepath.ToSlash(rel)+"\n")
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(lines)
	return lines, nil
}

// DigestListing hashes a canonical listing produced by DirListing.
func DigestListing(lines []string) string { return DigestBytes([]byte(strings.Join(lines, ""))) }

// DigestDir is the entry digest of a skill directory.
func DigestDir(dir string) (string, []string, error) {
	lines, err := DirListing(dir)
	if err != nil {
		return "", nil, err
	}
	return DigestListing(lines), lines, nil
}

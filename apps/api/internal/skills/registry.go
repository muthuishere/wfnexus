// Package skills is the skill registry: every SKILL.md the platform can give a
// workflow step, discovered across several roots and addressable by name.
//
// Discovery order matters — the first root that defines a name wins, so a
// repo-local skill shadows a machine-wide one of the same name.
package skills

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	tn "github.com/muthuishere/toolnexus/golang"
)

// Skill is one registry entry.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Root is the directory it was discovered under; Location is its SKILL.md.
	Root     string `json:"root"`
	Location string `json:"location"`
	// Source labels the root for humans: "project", "claude", "agents", or the dir.
	Source string `json:"source"`
	// Shadowed lists same-named skills in later roots that this one hides.
	Shadowed []string `json:"shadowed,omitempty"`
}

// Skipped is a candidate that did not become a skill, with the reason.
type Skipped struct {
	Location string `json:"location"`
	Reason   string `json:"reason"`
}

// Registry is an immutable snapshot of the discovered skills.
type Registry struct {
	mu      sync.RWMutex
	roots   []string
	byName  map[string]Skill
	skipped []Skipped
}

// DefaultRoots is the project's skills dir followed by the machine-wide ones.
// A root that does not exist is skipped silently — most machines have neither.
func DefaultRoots(projectDir string) []string {
	roots := []string{projectDir}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, ".claude", "skills"), filepath.Join(home, ".agents", "skills"))
	}
	return existing(roots)
}

func existing(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		if st, err := os.Stat(r); err == nil && st.IsDir() {
			out = append(out, r)
		}
	}
	return out
}

func sourceLabel(root string) string {
	switch {
	case strings.Contains(root, filepath.Join(".claude", "skills")):
		return "claude"
	case strings.Contains(root, filepath.Join(".agents", "skills")):
		return "agents"
	default:
		return "project"
	}
}

// Load discovers every skill under roots. Discovery is delegated to toolnexus
// so the registry sees exactly what an agent would load — same frontmatter
// parsing, same skip reasons.
func Load(roots ...string) *Registry {
	r := &Registry{roots: existing(roots), byName: map[string]Skill{}}
	for _, root := range r.roots {
		inv := tn.ListSkills(tn.LoadSkillsOptions{Dirs: []string{root}})
		for _, s := range inv.Skills {
			if prev, ok := r.byName[s.Name]; ok {
				// an earlier root already owns this name; record the shadowing
				prev.Shadowed = append(prev.Shadowed, s.Location)
				r.byName[s.Name] = prev
				continue
			}
			r.byName[s.Name] = Skill{
				Name: s.Name, Description: s.Description, Root: root,
				Location: s.Location, Source: sourceLabel(root),
			}
		}
		for _, sk := range inv.Skipped {
			r.skipped = append(r.skipped, Skipped{Location: sk.Location, Reason: string(sk.Reason)})
		}
	}
	return r
}

// Roots are the directories this registry was built from, in precedence order.
func (r *Registry) Roots() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.roots...)
}

// Get returns one skill by name.
func (r *Registry) Get(name string) (Skill, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byName[name]
	return s, ok
}

// Has reports whether the registry can supply this skill.
func (r *Registry) Has(name string) bool {
	_, ok := r.Get(name)
	return ok
}

// List returns every skill, name-sorted.
func (r *Registry) List() []Skill {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Skill, 0, len(r.byName))
	for _, s := range r.byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Skipped returns the candidates that failed to load, so a typo in frontmatter
// is visible instead of a skill silently missing.
func (r *Registry) Skipped() []Skipped {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Skipped(nil), r.skipped...)
}

// Missing returns the names not in the registry, preserving order.
func (r *Registry) Missing(names []string) []string {
	var out []string
	for _, n := range names {
		if !r.Has(n) {
			out = append(out, n)
		}
	}
	return out
}

// MissingBuiltins lets a Registry stand in as a workflow Catalog.
func (r *Registry) MissingBuiltins(names []string) []string { return MissingBuiltins(names) }

// RootsFor returns the roots a step's skills live in — what to hand toolnexus
// as SkillsDir so only the needed trees are walked.
func (r *Registry) RootsFor(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if s, ok := r.Get(n); ok && !seen[s.Root] {
			seen[s.Root] = true
			out = append(out, s.Root)
		}
	}
	return out
}

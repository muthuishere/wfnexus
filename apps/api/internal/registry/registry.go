// Package registry is the ONE registry type the platform uses for everything a
// workflow step can name: skills, providers, classifiers, MCP servers.
//
// A step never carries configuration inline — it names an entry, and the entry
// is defined once, in one place, validated at load time. That is what makes a
// workflow portable between machines: the names travel, the credentials and
// endpoints do not.
package registry

import (
	"fmt"
	"sort"
)

// Entry is anything a registry can hold. A name is all the registry needs.
type Entry interface {
	EntryName() string
}

// Skip records something that could not become an entry, and why — so a typo is
// visible rather than a silently missing capability.
type Skip struct {
	Location string `json:"location"`
	Reason   string `json:"reason"`
}

// Registry is an immutable, name-keyed set of entries.
type Registry[T Entry] struct {
	kind    string
	byName  map[string]T
	skipped []Skip
}

// New builds a registry. The FIRST entry for a name wins and later ones are
// recorded as skipped, so precedence is explicit rather than last-write-wins.
func New[T Entry](kind string, entries ...T) *Registry[T] {
	r := &Registry[T]{kind: kind, byName: make(map[string]T, len(entries))}
	for _, e := range entries {
		r.Add(e, "")
	}
	return r
}

// Add inserts an entry unless its name is taken. location labels the source for
// the skip record.
func (r *Registry[T]) Add(e T, location string) bool {
	name := e.EntryName()
	if name == "" {
		r.skipped = append(r.skipped, Skip{Location: location, Reason: "missing-name"})
		return false
	}
	if _, taken := r.byName[name]; taken {
		r.skipped = append(r.skipped, Skip{Location: location, Reason: "duplicate-name"})
		return false
	}
	r.byName[name] = e
	return true
}

// Skipped records a rejection the caller detected itself.
func (r *Registry[T]) Skipped(location, reason string) {
	r.skipped = append(r.skipped, Skip{Location: location, Reason: reason})
}

// Kind names what this registry holds ("skill", "provider", …), for messages.
func (r *Registry[T]) Kind() string { return r.kind }

func (r *Registry[T]) Get(name string) (T, bool) {
	e, ok := r.byName[name]
	return e, ok
}

func (r *Registry[T]) Has(name string) bool {
	_, ok := r.byName[name]
	return ok
}

func (r *Registry[T]) Len() int { return len(r.byName) }

// List returns every entry, name-sorted, so output is stable.
func (r *Registry[T]) List() []T {
	out := make([]T, 0, len(r.byName))
	for _, name := range r.Names() {
		out = append(out, r.byName[name])
	}
	return out
}

// Names returns every entry name, sorted.
func (r *Registry[T]) Names() []string {
	out := make([]string, 0, len(r.byName))
	for name := range r.byName {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Skips are the candidates that did not become entries.
func (r *Registry[T]) Skips() []Skip {
	return append([]Skip(nil), r.skipped...)
}

// Missing returns the names this registry cannot supply, in the order asked.
func (r *Registry[T]) Missing(names []string) []string {
	var out []string
	for _, n := range names {
		if !r.Has(n) {
			out = append(out, n)
		}
	}
	return out
}

// Require resolves a name or explains what was available, which is the error a
// workflow author actually needs.
func (r *Registry[T]) Require(name string) (T, error) {
	if e, ok := r.byName[name]; ok {
		return e, nil
	}
	var zero T
	known := r.Names()
	if len(known) == 0 {
		return zero, fmt.Errorf("unknown %s %q — no %s is configured", r.kind, name, r.kind)
	}
	if len(known) > 12 {
		known = append(known[:12:12], "…")
	}
	return zero, fmt.Errorf("unknown %s %q — known: %v", r.kind, name, known)
}

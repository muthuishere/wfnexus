package skills

import (
	"sort"

	tn "github.com/muthuishere/toolnexus/golang"
)

// BuiltinTool describes one toolnexus built-in a step may be granted. These are
// the Claude-style coding tools: shell, file read/write/edit/patch, search, fetch.
type BuiltinTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Builtins is every tool a step may name — toolnexus's built-ins plus this
// platform's own (platform.go) — name-sorted.
func Builtins() []BuiltinTool {
	all := tn.CreateBuiltinTools()
	out := make([]BuiltinTool, 0, len(all))
	for _, t := range all {
		out = append(out, BuiltinTool{Name: t.Name, Description: t.Description})
	}
	out = append(out, PlatformTools()...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// BuiltinNames is the set of valid tool names a step may request.
func BuiltinNames() map[string]bool {
	set := map[string]bool{}
	for _, t := range tn.CreateBuiltinTools() {
		set[t.Name] = true
	}
	for _, t := range PlatformTools() {
		set[t.Name] = true
	}
	return set
}

// BuiltinAllowlist turns "the tools this step may use" into the config
// toolnexus actually enforces.
//
// This is NOT cosmetic: SelectBuiltins only drops a tool whose entry is
// explicitly false, so a map holding just the allowed names leaves every other
// built-in switched ON. Every name outside the allowlist must be written false.
func BuiltinAllowlist(allowed []string) tn.BuiltinsConfig {
	// A step that asks only for platform tools still gets NO toolnexus
	// built-ins — it asked for none.
	real := 0
	for _, n := range allowed {
		if !IsPlatformTool(n) {
			real++
		}
	}
	if real == 0 {
		off := false
		return tn.BuiltinsConfig{Enabled: &off}
	}
	want := map[string]bool{}
	for _, n := range allowed {
		want[n] = true
	}
	// Only toolnexus's own built-ins go in its config; a platform tool is
	// registered on the toolkit by the engine and is not something toolnexus
	// knows how to switch on or off.
	tools := map[string]bool{}
	for _, t := range tn.CreateBuiltinTools() {
		tools[t.Name] = want[t.Name]
	}
	return tn.BuiltinsConfig{Tools: tools}
}

// MissingBuiltins returns requested names that are not real built-ins.
func MissingBuiltins(names []string) []string {
	valid := BuiltinNames()
	var out []string
	for _, n := range names {
		if !valid[n] {
			out = append(out, n)
		}
	}
	return out
}

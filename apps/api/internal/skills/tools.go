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

// Builtins is every built-in tool name toolnexus ships, name-sorted.
func Builtins() []BuiltinTool {
	all := tn.CreateBuiltinTools()
	out := make([]BuiltinTool, 0, len(all))
	for _, t := range all {
		out = append(out, BuiltinTool{Name: t.Name, Description: t.Description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// BuiltinNames is the set of valid built-in tool names.
func BuiltinNames() map[string]bool {
	set := map[string]bool{}
	for _, t := range tn.CreateBuiltinTools() {
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
	if len(allowed) == 0 {
		off := false
		return tn.BuiltinsConfig{Enabled: &off}
	}
	want := map[string]bool{}
	for _, n := range allowed {
		want[n] = true
	}
	tools := map[string]bool{}
	for name := range BuiltinNames() {
		tools[name] = want[name]
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

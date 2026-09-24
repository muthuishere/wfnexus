package bundle

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// What publishing REFUSES (design §6).
//
// Publish is the gate. Failing here rather than at run time on somebody else's
// machine is the entire point of the verb: a workflow that arrives and then
// fails to load is exactly the "portable if the destination already agreed"
// outcome ADR 0018 rejects.
//
// These run CLIENT-SIDE before upload — so nothing leaves the machine — and
// SERVER-SIDE on receipt, because a client is not a guard.

// CheckNoLiteralCredential applies catalog.LooksLikeSecret UNCHANGED to every
// field that is meant to hold the NAME of an environment variable.
//
// The message names the field and never the value. Echoing it would write the
// credential into a log on the way to telling somebody not to write it into a
// file.
func CheckNoLiteralCredential(def *workflow.Definition, mcp map[string]catalog.McpServer, providers []catalog.Provider, classifiers []catalog.Classifier) error {
	bad := func(where string) error {
		return fmt.Errorf("%s must be the NAME of an environment variable, not a value", where)
	}
	for _, k := range sortedKeys(def.Env) {
		if catalog.LooksLikeSecret(k) {
			return bad("a workflow-level `env:` key")
		}
	}
	for _, st := range def.Steps {
		for _, k := range sortedKeys(st.Env) {
			if catalog.LooksLikeSecret(k) {
				return bad(fmt.Sprintf("step %s: an `env:` key", st.ID))
			}
		}
	}
	for _, p := range providers {
		if catalog.LooksLikeSecret(p.APIKeyEnv) {
			return bad(fmt.Sprintf("provider %s: apiKeyEnv", p.Name))
		}
	}
	for _, c := range classifiers {
		if catalog.LooksLikeSecret(c.APIKeyEnv) {
			return bad(fmt.Sprintf("classifier %s: apiKeyEnv", c.Name))
		}
	}
	for _, name := range sortedMcpKeys(mcp) {
		for _, hk := range sortedKeys(mcp[name].Headers) {
			if catalog.LooksLikeSecret(mcp[name].Headers[hk]) {
				return bad(fmt.Sprintf("mcp server %s: header %s", name, hk))
			}
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedMcpKeys(m map[string]catalog.McpServer) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Named is every skill and MCP server the workflow's steps name, deduplicated
// and sorted so a bundle built twice from the same workflow is identical.
func Named(def *workflow.Definition) (skillNames, mcpNames []string) {
	sk, mc := map[string]bool{}, map[string]bool{}
	for _, st := range def.Steps {
		for _, n := range st.Skills {
			sk[n] = true
		}
		for _, n := range st.MCP {
			mc[n] = true
		}
		for _, t := range st.Team {
			for _, n := range t.Skills {
				sk[n] = true
			}
		}
	}
	for n := range sk {
		skillNames = append(skillNames, n)
	}
	for n := range mc {
		mcpNames = append(mcpNames, n)
	}
	sort.Strings(skillNames)
	sort.Strings(mcpNames)
	return
}

// Build resolves every named dependency against the PUBLISHER's roots and
// carries it in. An unresolved name is a publish error naming the reference and
// the roots searched — the same loud failure the registry already produces at
// boot, moved to where it is cheap to fix.
func Build(kind, project, name, version string, workflowYAML []byte, def *workflow.Definition, reg *skills.Registry, cat *catalog.Catalog) (*Bundle, error) {
	b := New(kind, project, name, version)
	b.AddWorkflow(workflowYAML)

	skillNames, mcpNames := Named(def)
	for _, sn := range skillNames {
		sk, ok := reg.Get(sn)
		if !ok {
			return nil, fmt.Errorf("cannot publish: step skill %q does not resolve on this machine; roots searched: %v", sn, reg.Roots())
		}
		// A skill is its DIRECTORY — SKILL.md plus whatever sits beside it.
		dir := skillDir(sk)
		if err := b.AddSkillDir(sn, dir); err != nil {
			return nil, fmt.Errorf("cannot publish skill %q from %s: %w", sn, dir, err)
		}
	}

	servers := map[string]catalog.McpServer{}
	for _, mn := range mcpNames {
		if cat == nil {
			return nil, fmt.Errorf("cannot publish: step names mcp server %q and no mcp catalog is loaded", mn)
		}
		e, ok := cat.Mcp.Get(mn)
		if !ok {
			return nil, fmt.Errorf("cannot publish: step mcp server %q does not resolve on this machine", mn)
		}
		servers[mn] = e
	}
	if len(servers) > 0 {
		doc, err := json.Marshal(map[string]any{"mcpServers": servers})
		if err != nil {
			return nil, err
		}
		b.AddMcp(doc)
	}

	var providers []catalog.Provider
	var classifiers []catalog.Classifier
	if cat != nil {
		providers, classifiers = cat.Providers.List(), cat.Classifiers.List()
	}
	if err := CheckNoLiteralCredential(def, servers, providers, classifiers); err != nil {
		return nil, err
	}
	return b, nil
}

// skillDir is the directory a discovered skill occupies. Location is its
// SKILL.md, so the directory is its parent — everything beside SKILL.md
// (references/, scripts/) is part of the skill and travels with it.
func skillDir(sk skills.Skill) string { return filepath.Dir(sk.Location) }

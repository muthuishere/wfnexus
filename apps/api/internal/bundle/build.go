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
	b.Manifest.Requires = Requirements(def, cat)
	return b, nil
}

// Requirements is what the RECEIVING host must already have for this bundle's
// steps to run (design §7): every provider a step names and its KIND, every
// `runs-on:` label asked for, and every MCP server named.
//
// It is gathered HERE because the receiving host cannot infer it. A provider
// name says nothing about whether an API key or a locally installed binary
// satisfies it, and a label nobody holds means a step that waits forever — the
// check `wfx dryrun` does locally, which a pull can only do if the bundle says
// what it needs.
//
// A workflow that names none returns nil, not an empty slice: the manifest key
// is then ABSENT rather than present-and-empty, so nothing is checked at the
// other end and the digest of a requirement-free bundle is what it always was.
func Requirements(def *workflow.Definition, cat *catalog.Catalog) []Requirement {
	// Keyed by kind+name, because one requirement asked for by three steps is
	// one requirement and three step IDs.
	seen := map[string]*Requirement{}
	add := func(kind, name, providerKind, apiKeyEnv, step string) {
		if name == "" {
			return
		}
		key := kind + "\x00" + name
		r, ok := seen[key]
		if !ok {
			r = &Requirement{Kind: kind, Name: name, ProviderKind: providerKind, APIKeyEnv: apiKeyEnv}
			seen[key] = r
		}
		// A workflow-level `env:` or `mount:` belongs to no single step, and an
		// empty string in this list would read as a step whose id is blank.
		if step != "" {
			r.Steps = append(r.Steps, step)
		}
	}
	// provider returns the kind and the apiKeyEnv NAME together, because they
	// come from the same catalog entry and a caller that had to ask twice would
	// be two lookups that could disagree.
	provider := func(name string) (kind, apiKeyEnv string) {
		// Empty when this machine's catalog does not know the provider. The
		// workflow's own validation refuses an unknown provider name, so the
		// honest reading of an empty kind is "no catalog was loaded", not "a
		// provider nobody has heard of".
		if cat == nil {
			return "", ""
		}
		p, ok := cat.Providers.Get(name)
		if !ok {
			return "", ""
		}
		// Only an `http` provider has one. A `cli`/`acp` provider's credential
		// is the CLI's own, held on that machine, so there is no variable to
		// name and naming one would imply a key we neither want nor can use.
		if p.Kind != catalog.KindHTTP {
			return string(p.Kind), ""
		}
		return string(p.Kind), p.APIKeyEnv
	}
	// `runs-on:` cascades workflow -> job -> step (jobs.go), and a bundle
	// published from the jobs form must record the label the step will actually
	// carry rather than the one it literally spells.
	walk := func(steps []workflow.Step, label string) {
		for _, st := range steps {
			kind, keyEnv := provider(st.Provider)
			add(ReqProvider, st.Provider, kind, keyEnv, st.ID)
			runsOn := st.RunsOn
			if runsOn == "" {
				runsOn = label
			}
			add(ReqLabel, runsOn, "", "", st.ID)
			for _, n := range st.MCP {
				add(ReqMcp, n, "", "", st.ID)
			}
		}
	}
	// Configuration and volumes, from the same steps. Both are recorded as the
	// workflow WROTE them — a variable NAME and a mount path — because both are
	// answered by the receiving machine and neither can be answered here.
	//
	// `workflow.EnvKeys` is the one place that knows which parts of an `env:`
	// block are references rather than literals. A literal (`NODE_ENV: test`)
	// requires nothing of the host; `${GITHUB_PAT}` requires everything.
	envReq := func(env map[string]string, step string) {
		for _, name := range workflow.EnvKeys(env) {
			add(ReqEnv, name, "", "", step)
		}
	}
	mountReq := func(mounts []workflow.Mount, step string) {
		for _, m := range mounts {
			// As WRITTEN, `./` included: the check has to be able to tell a folder
			// beside the workflow from a folder under the data dir, because they
			// are different folders and only one of them exists on a worker.
			name := m.Host
			if m.FromSource {
				name = "./" + name
			}
			key := ReqVolume + "\x00" + name
			if r, ok := seen[key]; ok {
				// Two steps mounting the same folder, one rw: the requirement is
				// the stricter of the two, because satisfying the looser one
				// would still fail the run.
				if !m.ReadOnly {
					r.Writable = true
				}
				if step != "" {
					r.Steps = append(r.Steps, step)
				}
				continue
			}
			add(ReqVolume, name, "", "", step)
			seen[key].Writable = !m.ReadOnly
		}
	}
	envReq(def.Env, "")
	mountReq(def.Mount, "")
	for _, st := range def.Steps {
		envReq(st.Env, st.ID)
	}
	for _, id := range sortedJobKeys(def.Jobs) {
		for _, st := range def.Jobs[id].Steps {
			envReq(st.Env, st.ID)
		}
	}

	walk(def.Steps, def.RunsOn)
	for _, id := range sortedJobKeys(def.Jobs) {
		job := def.Jobs[id]
		label := job.RunsOn
		if label == "" {
			label = def.RunsOn
		}
		walk(job.Steps, label)
	}
	if len(seen) == 0 {
		return nil
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Requirement, 0, len(keys))
	for _, k := range keys {
		r := seen[k]
		sort.Strings(r.Steps)
		out = append(out, *r)
	}
	return out
}

func sortedJobKeys(m map[string]*workflow.Job) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// skillDir is the directory a discovered skill occupies. Location is its
// SKILL.md, so the directory is its parent — everything beside SKILL.md
// (references/, scripts/) is part of the skill and travels with it.
func skillDir(sk skills.Skill) string { return filepath.Dir(sk.Location) }

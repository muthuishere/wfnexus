package bundle

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/registry"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

func testCatalog() *catalog.Catalog {
	return &catalog.Catalog{
		Providers: registry.New[catalog.Provider](
			"provider",
			catalog.Provider{Name: "haiku", Kind: catalog.KindHTTP, APIKeyEnv: "OPENROUTER_API_KEY"},
			catalog.Provider{Name: "opencode", Kind: catalog.KindCLI, Preset: "opencode"},
			catalog.Provider{Name: "devin", Kind: catalog.KindACP, Preset: "devin"},
		),
		Classifiers: registry.New[catalog.Classifier]("classifier"),
		Mcp:         registry.New[catalog.McpServer]("mcp server"),
		Notifiers:   registry.New[catalog.Notifier]("notifier"),
	}
}

// task 5.1 — the tree writer is the exact inverse of FromTree, and the tree and
// the tar describe the SAME bundle: one manifest, one set of digests.
func TestWriteTreeRoundTripsToTheSameDigestAsTheTar(t *testing.T) {
	b := New("workflow", "", "code-review", "1.0.0")
	b.AddWorkflow([]byte("name: code-review\n"))
	b.AddMcp([]byte(`{"mcpServers":{}}`))
	if err := b.AddSkillDir("fix-author", skillTree(t, "fix-author")); err != nil {
		t.Fatal(err)
	}
	want, err := b.Verify("")
	if err != nil {
		t.Fatal(err)
	}

	// Pack -> Unpack -> Verify, the path that already worked.
	raw, err := b.Pack()
	if err != nil {
		t.Fatal(err)
	}
	fromTar, err := Unpack(raw)
	if err != nil {
		t.Fatal(err)
	}
	tarDigest, err := fromTar.Verify(want)
	if err != nil {
		t.Fatalf("the tar did not verify: %v", err)
	}

	// WriteTree -> FromTree -> Verify, the new one, under the layout a
	// repository uses.
	root := t.TempDir()
	dir := filepath.Join(root, filepath.FromSlash(TreeDir("code-review", "1.0.0")))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteTree(dir); err != nil {
		t.Fatal(err)
	}
	fromTree, err := FromTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	treeDigest, err := fromTree.Verify(want)
	if err != nil {
		t.Fatalf("the written tree did not verify: %v", err)
	}
	if treeDigest != tarDigest {
		t.Fatalf("the tree and the tar disagree: %s vs %s", treeDigest, tarDigest)
	}
	for p, content := range b.Files {
		if string(fromTree.Files[p]) != string(content) {
			t.Fatalf("%s differs between the bundle and the written tree", p)
		}
	}
	// The committed files are readable, named exactly as the tar's members,
	// and there is no archive among them.
	for _, name := range []string{ManifestPath, WorkflowPath, McpPath, "skills/fix-author/SKILL.md"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Fatalf("the tree is missing %s: %v", name, err)
		}
	}
	// SelectInTree finds it, so what publish writes is what a `use:` reads.
	got, err := SelectInTree(root, "")
	if err != nil || got != dir {
		t.Fatalf("SelectInTree found %q (err %v), want %q", got, err, dir)
	}
}

// A version directory that already holds a bundle is not overwritten: a
// published version is immutable, and an amendment in a clone would never reach
// the remote's refusal.
func TestWriteTreeRefusesAnExistingVersion(t *testing.T) {
	b := New("workflow", "", "w", "1.0.0")
	b.AddWorkflow([]byte("name: w\n"))
	if _, err := b.Verify(""); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := b.WriteTree(dir); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteTree(dir); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("a rewrite of a published version: %v", err)
	}
}

// task 5.2 / 5.4 — every provider with its KIND, every runs-on label, every MCP
// server, gathered from the steps, deterministic, with the step IDs recorded.
func TestRequirementsRecordProvidersLabelsAndMcpServers(t *testing.T) {
	def := &workflow.Definition{
		Name:   "w",
		RunsOn: "linux",
		Steps: []workflow.Step{
			{ID: "two", Provider: "haiku", MCP: []string{"github"}},
			{ID: "one", Provider: "opencode", RunsOn: "windows"},
			{ID: "three", Provider: "haiku", MCP: []string{"github", "slack"}},
		},
	}
	got := Requirements(def, testCatalog())
	want := []Requirement{
		{Kind: ReqLabel, Name: "linux", Steps: []string{"three", "two"}},
		{Kind: ReqLabel, Name: "windows", Steps: []string{"one"}},
		{Kind: ReqMcp, Name: "github", Steps: []string{"three", "two"}},
		{Kind: ReqMcp, Name: "slack", Steps: []string{"three"}},
		{Kind: ReqProvider, Name: "haiku", ProviderKind: "http", Steps: []string{"three", "two"}},
		{Kind: ReqProvider, Name: "opencode", ProviderKind: "cli", Steps: []string{"one"}},
	}
	if len(got) != len(want) {
		t.Fatalf("recorded %d requirements, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Kind != want[i].Kind || got[i].Name != want[i].Name || got[i].ProviderKind != want[i].ProviderKind {
			t.Fatalf("requirement %d is %+v, want %+v", i, got[i], want[i])
		}
		if strings.Join(got[i].Steps, ",") != strings.Join(want[i].Steps, ",") {
			t.Fatalf("requirement %s steps are %v, want %v", got[i].Name, got[i].Steps, want[i].Steps)
		}
	}
	// The same workflow gathered twice is the same slice — a bundle built twice
	// has to have the same digest.
	again := Requirements(def, testCatalog())
	for i := range again {
		if again[i].Name != got[i].Name {
			t.Fatalf("requirement order moved between two gatherings: %v", again)
		}
	}
	// An `acp` provider records that a BINARY is required, not a key.
	acp := Requirements(&workflow.Definition{Steps: []workflow.Step{{ID: "s", Provider: "devin"}}}, testCatalog())
	if len(acp) != 1 || acp[0].ProviderKind != "acp" {
		t.Fatalf("an acp provider recorded as %+v", acp)
	}
	// The label cascades workflow -> job -> step, so a bundle published from
	// the jobs form records the label the step will actually carry.
	jobs := Requirements(&workflow.Definition{
		RunsOn: "linux",
		Jobs:   map[string]*workflow.Job{"build": {RunsOn: "macos", Steps: []workflow.Step{{ID: "s"}}}},
	}, testCatalog())
	if len(jobs) != 1 || jobs[0].Kind != ReqLabel || jobs[0].Name != "macos" {
		t.Fatalf("the job's label was not recorded: %+v", jobs)
	}
}

// task 8.6 — a workflow naming nothing records NOTHING: absent, not empty. The
// manifest key is then missing, so the digest of a requirement-free bundle is
// exactly what it was before requirements existed.
func TestAWorkflowNamingNothingRecordsNoRequirements(t *testing.T) {
	def := &workflow.Definition{Name: "w", Steps: []workflow.Step{{ID: "one", Run: "echo hi"}}}
	if got := Requirements(def, testCatalog()); got != nil {
		t.Fatalf("a requirement-free workflow recorded %+v", got)
	}

	b := New("workflow", "", "w", "1.0.0")
	b.AddWorkflow([]byte("name: w\n"))
	before, err := b.Manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	b.Manifest.Requires = Requirements(def, testCatalog())
	after, err := b.Manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("recording no requirements moved an existing bundle's digest: %s -> %s", before, after)
	}
	canon, err := b.Manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(canon), "requires") {
		t.Fatalf("the manifest carries an empty requires key: %s", canon)
	}
}

// [SEC-TEST] task 5.3 — the attack is a published manifest carrying the
// CREDENTIAL of the provider it records: a key pasted into apiKeyEnv, or the
// value of the env var the publishing machine happens to hold. The manifest
// records a requirement and never a credential, so neither the value nor the
// variable's name-as-a-value may appear in the written bytes.
func TestManifestRecordsARequirementAndNeverACredential(t *testing.T) {
	const pasted = "sk-proj-AAAABBBBCCCCDDDDEEEEFFFF/1234+5678=="
	t.Setenv("OPENROUTER_API_KEY", pasted)

	cat := testCatalog()
	def := &workflow.Definition{Name: "w", Steps: []workflow.Step{{ID: "one", Provider: "haiku"}}}
	b := New("workflow", "", "w", "1.0.0")
	b.AddWorkflow([]byte("name: w\nsteps:\n  - id: one\n    provider: haiku\n"))
	b.Manifest.Requires = Requirements(def, cat)
	if len(b.Manifest.Requires) == 0 {
		t.Fatal("the http provider was not recorded at all")
	}
	if _, err := b.Verify(""); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := b.WriteTree(dir); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(filepath.Join(dir, ManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	// The key's VALUE, never. The variable's NAME, deliberately — that is the
	// line this whole codebase draws, and a written manifest is exactly where it
	// has to hold, because these bytes get committed and pulled by strangers.
	//
	// This assertion was once "no apiKey field at all", which was right while the
	// name could not travel and became wrong the moment it could. A test that
	// forbids the field rather than the value would forbid the actionable
	// refusal and permit nothing extra.
	if strings.Contains(string(written), pasted) {
		t.Fatal("the written manifest carries the key's value")
	}
	if !strings.Contains(string(written), `"apiKeyEnv":"OPENROUTER_API_KEY"`) {
		t.Fatalf("the variable's NAME did not travel: %s", written)
	}
	// And the same rule the publish refusals already apply: a pasted value in
	// the field meant to hold a NAME never gets as far as a manifest.
	if !catalog.LooksLikeSecret(pasted) {
		t.Fatal("catalog.LooksLikeSecret no longer recognises a pasted key")
	}
	bad := testCatalog()
	bad.Providers.Add(catalog.Provider{Name: "leaky", Kind: catalog.KindHTTP, APIKeyEnv: pasted}, "")
	err = CheckNoLiteralCredential(def, nil, bad.Providers.List(), nil)
	if err == nil {
		t.Fatal("a provider whose apiKeyEnv holds a value was accepted")
	}
	if strings.Contains(err.Error(), pasted) {
		t.Fatalf("the refusal echoed the value: %v", err)
	}
}

// [SEC-TEST] The manifest records the NAME of the variable an `http` provider
// reads its key from, and can never record the key. The distinction is the whole
// discipline of this codebase — names travel, values do not — and the attack it
// stops is a bundle published from a machine where the key was pasted into the
// registry instead of the variable's name, which would then ship the credential
// to every host that pulled it.
//
// A `cli` provider gets no variable at all: its credential is the CLI's own,
// held on the machine, so naming one would imply a key we neither want nor use.
func TestTheManifestCarriesTheKeysNameAndNeverItsValue(t *testing.T) {
	const secret = "sk-ant-not-a-real-key-0123456789abcdef"
	t.Setenv("ANTHROPIC_API_KEY", secret)

	cat := &catalog.Catalog{
		Providers: registry.New[catalog.Provider]("provider",
			catalog.Provider{Name: "anthropic", Kind: catalog.KindHTTP, BaseURL: "https://api.anthropic.com", APIKeyEnv: "ANTHROPIC_API_KEY"},
			catalog.Provider{Name: "claude-cli", Kind: catalog.KindCLI, Preset: "claude"},
		),
		Classifiers: registry.New[catalog.Classifier]("classifier"),
		Mcp:         registry.New[catalog.McpServer]("mcp server"),
		Notifiers:   registry.New[catalog.Notifier]("notifier"),
	}
	def := &workflow.Definition{Name: "w", Steps: []workflow.Step{
		{ID: "paid", Provider: "anthropic"},
		{ID: "seat", Provider: "claude-cli"},
	}}

	reqs := Requirements(def, cat)
	byName := map[string]Requirement{}
	for _, r := range reqs {
		byName[r.Name] = r
	}
	if got := byName["anthropic"].APIKeyEnv; got != "ANTHROPIC_API_KEY" {
		t.Errorf("the http provider's apiKeyEnv NAME did not travel: %q", got)
	}
	if got := byName["claude-cli"].APIKeyEnv; got != "" {
		t.Errorf("a cli provider named a variable %q; its credential is the CLI's own", got)
	}

	// The value cannot appear anywhere in the bytes that get committed.
	b := New("workflow", "", "w", "1.0.0")
	b.AddWorkflow([]byte("name: w\n"))
	b.Manifest.Requires = reqs
	canon, err := b.Manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(canon), secret) {
		t.Fatal("the key's VALUE is in the manifest")
	}
}

// Configuration and volumes are gathered at publish time like every other
// requirement, so the receiving machine can be asked BEFORE anything installs.
//
// The two rules that matter: a LITERAL env value requires nothing of the host
// (`NODE_ENV: test` is committed, so it is not a secret and not a question),
// and two steps mounting the same folder take the STRICTER of the two — a
// read-write need is not satisfied by clearing the read-only one.
func TestEnvReferencesAndMountsBecomeRequirements(t *testing.T) {
	def := &workflow.Definition{
		Name: "w",
		Env:  map[string]string{"NODE_ENV": "test", "GH_TOKEN": "${GITHUB_PAT}"},
		Mount: []workflow.Mount{
			{Host: "datasets", At: "data", ReadOnly: true},
		},
		Steps: []workflow.Step{
			{ID: "read", Env: map[string]string{"KEY": "${SHARED_KEY}"}},
			{ID: "write", Env: map[string]string{"KEY": "${SHARED_KEY}"}},
		},
	}
	// The same folder again, read-write this time.
	def.Mount = append(def.Mount, workflow.Mount{Host: "datasets", At: "data", ReadOnly: false})

	byKind := map[string][]Requirement{}
	for _, r := range Requirements(def, testCatalog()) {
		byKind[r.Kind] = append(byKind[r.Kind], r)
	}

	var names []string
	for _, r := range byKind[ReqEnv] {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	want := []string{"GITHUB_PAT", "SHARED_KEY"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Errorf("env requirements are %v, want %v — a literal must require nothing and a reference must require its NAME", names, want)
	}
	// One name, both steps that read it.
	for _, r := range byKind[ReqEnv] {
		if r.Name == "SHARED_KEY" && len(r.Steps) != 2 {
			t.Errorf("SHARED_KEY is read by two steps, recorded %v", r.Steps)
		}
	}

	if n := len(byKind[ReqVolume]); n != 1 {
		t.Fatalf("the same folder twice is one requirement, got %d", n)
	}
	if !byKind[ReqVolume][0].Writable {
		t.Error("a folder mounted both ro and rw must record the STRICTER need; clearing the ro one would let the write fail mid-run")
	}
}

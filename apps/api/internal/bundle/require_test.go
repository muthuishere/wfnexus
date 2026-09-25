package bundle

import (
	"reflect"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
)

// fakeHost is a receiving machine, described. It also RECORDS every question it
// was asked, because several rules below are about what is NOT asked.
type fakeHost struct {
	providers map[string]ProviderReadiness
	mcp       map[string]bool
	labels    map[string]int
	env       map[string]string // name → which store answered; a VALUE is never here
	volumes   map[string]VolumeReadiness
	asked     []string
}

func (h *fakeHost) Provider(name string) ProviderReadiness {
	h.asked = append(h.asked, "provider:"+name)
	return h.providers[name]
}

func (h *fakeHost) HasMcp(name string) bool {
	h.asked = append(h.asked, "mcp:"+name)
	return h.mcp[name]
}

func (h *fakeHost) EnvResolves(name string) (bool, string) {
	h.asked = append(h.asked, "env:"+name)
	from, ok := h.env[name]
	return ok, from
}

func (h *fakeHost) VolumeState(host string, writable bool) VolumeReadiness {
	h.asked = append(h.asked, "volume:"+host)
	st, ok := h.volumes[host]
	if !ok {
		return VolumeReadiness{Resolved: "/resolved" + host}
	}
	if st.Resolved == "" {
		st.Resolved = "/resolved" + host
	}
	return st
}

func (h *fakeHost) LabelHolders(label string) int {
	h.asked = append(h.asked, "label:"+label)
	return h.labels[label]
}

// task 8.1/8.2 — every unmet requirement at once, each with what would satisfy
// it. Naming one and stopping makes the operator pull, install, pull again.
func TestARefusalNamesEveryUnmetRequirement(t *testing.T) {
	host := &fakeHost{
		providers: map[string]ProviderReadiness{
			"openrouter": {Found: true, Kind: "http", Ready: false, Problem: "OPENROUTER_API_KEY is not set", Fix: "set OPENROUTER_API_KEY in this machine's environment"},
		},
	}
	rep := CheckRequirements([]Requirement{
		{Kind: ReqProvider, Name: "openrouter", ProviderKind: "http", Steps: []string{"plan"}},
		{Kind: ReqProvider, Name: "claude-cli", ProviderKind: "cli", Steps: []string{"fix"}},
		{Kind: ReqLabel, Name: "windows", Steps: []string{"fix"}},
		{Kind: ReqMcp, Name: "github", Steps: []string{"open-pr"}},
	}, host)

	if rep.OK() || len(rep.Unmet) != 4 {
		t.Fatalf("want 4 unmet, got %d: %+v", len(rep.Unmet), rep.Unmet)
	}
	msg := rep.Err().Error()
	for _, want := range []string{
		"cannot pull:",
		"openrouter", "OPENROUTER_API_KEY is not set", "set OPENROUTER_API_KEY",
		"claude-cli", "install its command on PATH",
		`label windows`, "no worker online holds",
		"mcp github", "mcp.json",
		"(asked for by plan)", "(asked for by open-pr)",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal does not say %q:\n%s", want, msg)
		}
	}
}

// task 8.6 — absent, not empty. A bundle with no providers, labels or MCP
// servers records nothing, so nothing is checked and the host is never asked.
func TestABundleThatRecordsNothingAsksTheHostNothing(t *testing.T) {
	host := &fakeHost{}
	rep := CheckRequirements(nil, host)
	if !rep.OK() || len(rep.Caveats) != 0 {
		t.Fatalf("a bundle with no requirements produced a report: %+v", rep)
	}
	if len(host.asked) != 0 {
		t.Fatalf("the host was questioned for a bundle that requires nothing: %v", host.asked)
	}
}

// present is not a refusal and not a tick. ready is silence.
func TestPresentIsACaveatAndReadyIsNot(t *testing.T) {
	host := &fakeHost{providers: map[string]ProviderReadiness{
		"claude-cli": {Found: true, Kind: "cli", State: StatePresent, AuthUnknown: true, Login: "claude"},
		"codex":      {Found: true, Kind: "cli", State: StateReady, Ready: true},
	}}
	rep := CheckRequirements([]Requirement{
		{Kind: ReqProvider, Name: "claude-cli", ProviderKind: "cli"},
		{Kind: ReqProvider, Name: "codex", ProviderKind: "cli"},
	}, host)
	if !rep.OK() {
		t.Fatalf("present was refused: %v", rep.Err())
	}
	if len(rep.Caveats) != 1 || rep.Caveats[0].Name != "claude-cli" {
		t.Fatalf("want one caveat, got %+v", rep.Caveats)
	}
	if rep.Caveats[0].Note != "present; authentication not checked" {
		t.Fatalf("note: %q", rep.Caveats[0].Note)
	}
}

// task 8.3 — a `runs-on:` label no worker holds is an unmet requirement, and a
// label somebody does hold is not.
func TestALabelNobodyHoldsIsUnmet(t *testing.T) {
	host := &fakeHost{labels: map[string]int{"local": 1}}
	rep := CheckRequirements([]Requirement{
		{Kind: ReqLabel, Name: "local"},
		{Kind: ReqLabel, Name: "windows"},
	}, host)
	if len(rep.Unmet) != 1 || rep.Unmet[0].Name != "windows" {
		t.Fatalf("want only `windows` unmet, got %+v", rep.Unmet)
	}
	if !strings.Contains(rep.Unmet[0].Fix, "wfx-runner join --labels windows") {
		t.Fatalf("the fix does not name the command that would satisfy it: %q", rep.Unmet[0].Fix)
	}
}

// task 8.5 — PRESENCE IS NOT AUTHENTICATION. A `cli` provider is ready on a
// PATH lookup alone, so a satisfied requirement must not be reported as a bare
// tick: it is a caveat naming the login the operator runs themselves. It is
// also not a refusal — the host has what the bundle asked for.
func TestASatisfiedCliRequirementDoesNotClaimAuthentication(t *testing.T) {
	host := &fakeHost{providers: map[string]ProviderReadiness{
		"claude-cli": {Found: true, Kind: "cli", Ready: true, AuthUnknown: true, Login: "claude"},
		"openrouter": {Found: true, Kind: "http", Ready: true},
	}}
	rep := CheckRequirements([]Requirement{
		{Kind: ReqProvider, Name: "claude-cli", ProviderKind: "cli"},
		{Kind: ReqProvider, Name: "openrouter", ProviderKind: "http"},
	}, host)
	if !rep.OK() {
		t.Fatalf("a present cli binary was refused: %v", rep.Err())
	}
	if len(rep.Caveats) != 1 || rep.Caveats[0].Name != "claude-cli" {
		t.Fatalf("want one caveat for claude-cli, got %+v", rep.Caveats)
	}
	if rep.Caveats[0].Note != "present; authentication not checked" {
		t.Fatalf("the caveat overstates what a PATH lookup proved: %q", rep.Caveats[0].Note)
	}
	if rep.Caveats[0].Fix != "claude" {
		t.Fatalf("the caveat does not carry the login the operator runs: %q", rep.Caveats[0].Fix)
	}
	// An http provider whose key IS set is genuinely ready — the third state
	// belongs to the local-binary kinds only, and applying it everywhere would
	// make every report a shrug.
	for _, c := range rep.Caveats {
		if c.Name == "openrouter" {
			t.Fatalf("a keyed http provider was reported as authentication-unknown")
		}
	}
}

// [SEC-TEST] task 8.4 — the attack is the platform becoming a credential
// broker: a refusal that asks for a cli/acp provider's API key, token or login,
// or a check that accepts one, would put somebody's subscription credential
// into a request body, a log and eventually a database. Neither this check's
// input nor its output has a field a credential could travel in, and the
// refusal only ever offers a command the OPERATOR runs.
func TestTheCheckNeverAsksForOrCarriesACredential(t *testing.T) {
	const secret = "sk-ant-notarealkey"
	host := &fakeHost{providers: map[string]ProviderReadiness{
		"claude-cli": {Found: false},
	}}
	reqs := []Requirement{{Kind: ReqProvider, Name: "claude-cli", ProviderKind: "cli", Steps: []string{"fix"}}}
	rep := CheckRequirements(reqs, host)

	msg := rep.Err().Error()
	if strings.Contains(msg, secret) {
		t.Fatal("a credential reached the refusal text")
	}
	// What is forbidden is ASKING for a value, not mentioning the variable that
	// holds one. `apiKeyEnv` as a bare substring would flag the `http` refusal,
	// whose whole job is to name the variable the operator sets themselves — so
	// the scan is for the ask.
	for _, phrase := range []string{"paste", "enter your", "provide your", "send us", "give us your", "upload your"} {
		if strings.Contains(strings.ToLower(msg), phrase) {
			t.Fatalf("the refusal asks the operator for a credential (%q):\n%s", phrase, msg)
		}
	}
	for _, unmet := range CheckRequirements([]Requirement{{
		Kind: ReqProvider, Name: "anthropic", ProviderKind: "http", APIKeyEnv: "ANTHROPIC_API_KEY", Steps: []string{"paid"},
	}}, &fakeHost{providers: map[string]ProviderReadiness{}}).Unmet {
		for _, phrase := range []string{"paste", "enter your", "provide your", "send us"} {
			if strings.Contains(strings.ToLower(unmet.Fix), phrase) {
				t.Fatalf("the http refusal asks for the key itself: %s", unmet.Fix)
			}
		}
	}
	if !strings.Contains(msg, "the login stays yours") {
		t.Fatalf("the refusal does not say whose credential it is:\n%s", msg)
	}

	// The INPUT side: a future field named like a credential is what this
	// assertion exists to catch. `APIKeyEnv` is the ONE allowed exception and it
	// is allowed on a stated ground, not waved through: it holds the NAME of an
	// environment variable, which is the same thing `catalog.Provider` already
	// stores and `LooksLikeSecret` already refuses a value in. So the exception
	// carries its own proof — a value in that field must still be recognised as
	// one — rather than merely being spelled into the allowlist.
	allowed := map[string]bool{"Requirement.APIKeyEnv": true}
	for _, typ := range []any{Requirement{}, ProviderReadiness{}, Unmet{}, Caveat{}} {
		rt := reflect.TypeOf(typ)
		for i := 0; i < rt.NumField(); i++ {
			field := rt.Name() + "." + rt.Field(i).Name
			if allowed[field] {
				continue
			}
			name := strings.ToLower(rt.Field(i).Name)
			for _, bad := range []string{"key", "token", "secret", "password", "credential"} {
				if strings.Contains(name, bad) {
					t.Fatalf("%s is a place a credential could travel", field)
				}
			}
		}
	}
	// The exception's proof: a pasted value in the NAME field is still a value,
	// and the publish-time refusal is what stops it ever reaching a manifest.
	if !catalog.LooksLikeSecret(secret) {
		t.Fatalf("a pasted key is no longer recognised, so APIKeyEnv's exception is unguarded")
	}
	// And the hint quoted into an `http` refusal is the variable's NAME, with no
	// value anywhere near it.
	hint := providerFix("http", "anthropic", "ANTHROPIC_API_KEY")
	if !strings.Contains(hint, "ANTHROPIC_API_KEY") {
		t.Fatalf("the refusal is true but not actionable — it names no variable: %s", hint)
	}
	if strings.Contains(hint, secret) {
		t.Fatalf("a value reached the refusal: %s", hint)
	}
}

// A requirement kind this version does not understand comes from a newer
// publisher. Refusing beats ignoring: a requirement we cannot interpret is one
// we cannot claim is met.
func TestAnUnknownRequirementKindIsRefusedRatherThanIgnored(t *testing.T) {
	rep := CheckRequirements([]Requirement{{Kind: "gpu", Name: "h100"}}, &fakeHost{})
	if rep.OK() || !strings.Contains(rep.Err().Error(), "newer version") {
		t.Fatalf("an unknown kind was not refused: %+v", rep)
	}
}

// Configuration is recorded as the NAME the workflow reads, and either store may
// answer it. `${GITHUB_PAT}` needs GITHUB_PAT to resolve somewhere on the machine
// that runs the step; whether that is the platform's encrypted store or the
// machine's own environment is the host's business and not the bundle's.
func TestAConfigVariableIsSatisfiedByEitherStore(t *testing.T) {
	reqs := []Requirement{{Kind: ReqEnv, Name: "GITHUB_PAT", Steps: []string{"push"}}}

	for _, from := range []string{"the platform's encrypted env store", "this machine's environment"} {
		host := &fakeHost{env: map[string]string{"GITHUB_PAT": from}}
		if rep := CheckRequirements(reqs, host); !rep.OK() {
			t.Errorf("satisfied from %s was still refused: %v", from, rep.Err())
		}
	}

	bare := &fakeHost{}
	rep := CheckRequirements(reqs, bare)
	if rep.OK() {
		t.Fatal("a variable nothing provides was accepted; the run would fail on the missing value")
	}
	msg := rep.Err().Error()
	if !strings.Contains(msg, "GITHUB_PAT") {
		t.Errorf("the refusal does not name the variable:\n%s", msg)
	}
	if !strings.Contains(msg, "wfx env set GITHUB_PAT") {
		t.Errorf("the refusal is not actionable — it does not say how to provide it:\n%s", msg)
	}
}

// [SEC-TEST] The check asks whether a variable RESOLVES and can never receive
// what it holds. A requirement carries the name; the host answers with a boolean
// and the name of a store. If a value could reach this path it would reach a
// refusal message, and refusals are printed and logged.
func TestAConfigCheckCannotReceiveAValue(t *testing.T) {
	const secret = "ghp_notarealtokenatall"
	host := &fakeHost{env: map[string]string{"GITHUB_PAT": "this machine's environment"}}
	rep := CheckRequirements([]Requirement{
		{Kind: ReqEnv, Name: "GITHUB_PAT", Steps: []string{"push"}},
		{Kind: ReqEnv, Name: "ABSENT_TOKEN", Steps: []string{"push"}},
	}, host)
	out := rep.Err().Error()
	if strings.Contains(out, secret) {
		t.Fatal("a value reached the refusal")
	}
	// The signature is the guarantee: (bool, string) where the string is the
	// STORE. A test that only checked the message would pass on a signature that
	// returned the value and happened not to print it today.
	ok, whence := host.EnvResolves("GITHUB_PAT")
	if !ok || whence == "" {
		t.Fatal("the host could not say whether it resolves")
	}
	if strings.Contains(whence, "ghp_") {
		t.Fatalf("the host returned something that looks like a value: %q", whence)
	}
}

// A read-write mount is NOT satisfied by a folder that exists and cannot be
// written, and that is the whole reason `Writable` is recorded at publish time.
// Without the distinction the run starts, the step works, and the write fails
// partway through — the most expensive moment to find out.
func TestAWritableMountIsNotSatisfiedByAReadOnlyFolder(t *testing.T) {
	host := &fakeHost{volumes: map[string]VolumeReadiness{
		"reports": {Exists: true, Writable: false, Resolved: "/data/mounts/reports"},
	}}

	// Read-only asks the looser question and is satisfied.
	if rep := CheckRequirements([]Requirement{
		{Kind: ReqVolume, Name: "reports", Steps: []string{"write"}},
	}, host); !rep.OK() {
		t.Errorf("a read-only mount was refused by a folder that exists: %v", rep.Err())
	}

	// Read-write is not.
	rep := CheckRequirements([]Requirement{
		{Kind: ReqVolume, Name: "reports", Writable: true, Steps: []string{"write"}},
	}, host)
	if rep.OK() {
		t.Fatal("a read-write mount was cleared by a folder that cannot be written")
	}
	msg := rep.Err().Error()
	for _, want := range []string{"reports", "/data/mounts/reports", "read-only"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal does not mention %q:\n%s", want, msg)
		}
	}
}

// A missing mount names the path it LOOKED FOR, not the path as written. A
// relative mount host resolves under the platform's data dir, so "reports is
// missing" is unactionable — the operator cannot tell which reports.
func TestAMissingMountNamesTheResolvedPath(t *testing.T) {
	host := &fakeHost{volumes: map[string]VolumeReadiness{
		"reports": {Exists: false, Resolved: "/var/lib/wfnexus/mounts/reports"},
	}}
	rep := CheckRequirements([]Requirement{
		{Kind: ReqVolume, Name: "reports", Steps: []string{"write"}},
	}, host)
	if rep.OK() {
		t.Fatal("a mount whose folder does not exist was accepted")
	}
	if !strings.Contains(rep.Err().Error(), "/var/lib/wfnexus/mounts/reports") {
		t.Errorf("the refusal does not say which folder was looked for:\n%s", rep.Err())
	}
}

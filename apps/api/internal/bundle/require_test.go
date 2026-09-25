package bundle

import (
	"reflect"
	"strings"
	"testing"
)

// fakeHost is a receiving machine, described. It also RECORDS every question it
// was asked, because several rules below are about what is NOT asked.
type fakeHost struct {
	providers map[string]ProviderReadiness
	mcp       map[string]bool
	labels    map[string]int
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
	for _, word := range []string{"api key", "apikey", "token", "password", "paste", "enter your"} {
		if strings.Contains(strings.ToLower(msg), word) {
			t.Fatalf("the refusal asks the operator for a credential (%q):\n%s", word, msg)
		}
	}
	if !strings.Contains(msg, "the login stays yours") {
		t.Fatalf("the refusal does not say whose credential it is:\n%s", msg)
	}

	// The INPUT side: a Requirement and a ProviderReadiness have no field that
	// could hold a secret value. A future field named like one is the thing this
	// assertion is here to catch.
	for _, typ := range []any{Requirement{}, ProviderReadiness{}, Unmet{}, Caveat{}} {
		rt := reflect.TypeOf(typ)
		for i := 0; i < rt.NumField(); i++ {
			name := strings.ToLower(rt.Field(i).Name)
			for _, bad := range []string{"key", "token", "secret", "password", "credential"} {
				if strings.Contains(name, bad) {
					t.Fatalf("%s.%s is a place a credential could travel", rt.Name(), rt.Field(i).Name)
				}
			}
		}
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

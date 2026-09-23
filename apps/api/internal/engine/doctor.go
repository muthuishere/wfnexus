package engine

import (
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/shell"
)

// Doctor is what is actually wired up, as opposed to what is declared.
//
// It exists because every registry entry is a NAME (ADR 0011) and a name
// resolves against this machine: a provider names an env var that may not be
// set, a `cli` provider names a program that may not be installed, a step names
// a default model nobody has looked at since it was typed. All of that was
// discoverable only by starting a run and reading the failure.
//
// Nothing here prints a secret. A provider reports the NAME of its key variable
// and whether it is set — never a value, not even a prefix.
type Doctor struct {
	Default     DoctorModel      `json:"default"`
	Providers   []DoctorProvider `json:"providers"`
	Classifiers []DoctorProvider `json:"classifiers"`
	Skills      DoctorCount      `json:"skills"`
	Mcp         DoctorCount      `json:"mcp"`
	Workflows   DoctorCount      `json:"workflows"`
	Models      []string         `json:"models"`
	Shell       DoctorShell      `json:"shell"`
	// Problems is never nil. A nil slice marshals to `null`, and the one
	// consumer that reads it does `problems.length` — so a machine with
	// NOTHING wrong crashed the System page, while a broken one rendered fine.
	// The healthy case is the one nobody tests.
	Problems []string `json:"problems"`
}

// DoctorShell is what a `run:` step will actually execute through on this
// machine, and what else was available. It is here because the shell is the one
// name a workflow relies on that resolves against the PLATFORM rather than
// against a registry — and on Windows it is the name most likely to be missing.
type DoctorShell struct {
	OS        string   `json:"os"`
	Arch      string   `json:"arch"`
	Using     string   `json:"using"`
	Path      string   `json:"path"`
	Problem   string   `json:"problem,omitempty"`
	Available []string `json:"available,omitempty"`
}

// DoctorModel is the process-wide default every step gets when it names no
// provider of its own.
type DoctorModel struct {
	Model     string `json:"model"`
	BaseURL   string `json:"baseUrl"`
	Style     string `json:"style"`
	APIKeyEnv string `json:"apiKeyEnv"`
	KeySet    bool   `json:"keySet"`
}

// DoctorProvider is one registry entry and whether it could actually run.
type DoctorProvider struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Model   string `json:"model,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Ready   bool   `json:"ready"`
	Problem string `json:"problem,omitempty"`
}

type DoctorCount struct {
	Count   int      `json:"count"`
	Skipped []string `json:"skipped,omitempty"`
}

// Doctor inspects the machine. It never starts a run and never dials a model:
// everything it reports is a file, an environment variable name or a PATH
// lookup, so it is safe to run at boot.
func (e *Engine) Doctor() Doctor {
	d := Doctor{
		Problems: []string{},
		Default: DoctorModel{
			Model: e.cfg.Model, BaseURL: e.cfg.LLMBaseURL, Style: e.cfg.LLMStyle,
			APIKeyEnv: e.cfg.LLMAPIKeyEnv, KeySet: os.Getenv(e.cfg.LLMAPIKeyEnv) != "",
		},
		Models: e.Models(),
	}
	if !d.Default.KeySet {
		d.Problems = append(d.Problems,
			"the default model's key variable "+e.cfg.LLMAPIKeyEnv+" is not set, so any step that names no provider will fail")
	}

	d.Shell = DoctorShell{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if sh, err := shell.Default(); err != nil {
		d.Shell.Problem = err.Error()
		d.Problems = append(d.Problems, "no shell: "+err.Error()+" — every `run:` step fails without one")
	} else {
		d.Shell.Using, d.Shell.Path = sh.Name+" "+strings.Join(sh.Flags, " "), sh.Path
	}
	for _, sh := range shell.Report() {
		if sh.Available() {
			d.Shell.Available = append(d.Shell.Available, sh.Name)
		}
	}

	for _, p := range e.catalog.Providers.List() {
		d.Providers = append(d.Providers, checkProvider(p))
	}
	sort.Slice(d.Providers, func(i, j int) bool { return d.Providers[i].Name < d.Providers[j].Name })

	for _, c := range e.catalog.Classifiers.List() {
		entry := DoctorProvider{Name: c.Name, Kind: c.Backend, Model: c.Model, Ready: true}
		if c.APIKeyEnv != "" {
			entry.Detail = c.APIKeyEnv
			if os.Getenv(c.APIKeyEnv) == "" {
				entry.Ready, entry.Problem = false, c.APIKeyEnv+" is not set"
			}
		}
		d.Classifiers = append(d.Classifiers, entry)
	}
	sort.Slice(d.Classifiers, func(i, j int) bool { return d.Classifiers[i].Name < d.Classifiers[j].Name })

	d.Skills = DoctorCount{Count: len(e.skills.List())}
	for _, s := range e.skills.Skipped() {
		d.Skills.Skipped = append(d.Skills.Skipped, s.Location+" ("+s.Reason+")")
	}
	d.Mcp = DoctorCount{Count: e.catalog.Mcp.Len()}
	d.Workflows = DoctorCount{Count: len(e.Definitions())}

	for _, p := range d.Providers {
		if !p.Ready {
			d.Problems = append(d.Problems, "provider "+p.Name+": "+p.Problem)
		}
	}
	for _, c := range d.Classifiers {
		if !c.Ready {
			d.Problems = append(d.Problems, "classifier "+c.Name+": "+c.Problem)
		}
	}
	return d
}

// checkProvider answers the only question that matters about an entry: if a
// step named it right now, would it run?
func checkProvider(p catalog.Provider) DoctorProvider {
	out := DoctorProvider{Name: p.Name, Kind: string(p.Kind), Model: p.Model, Ready: true}
	switch p.Kind {
	case catalog.KindHTTP:
		out.Detail = p.BaseURL
		if os.Getenv(p.APIKeyEnv) == "" {
			out.Ready, out.Problem = false, p.APIKeyEnv+" is not set"
		}
		return out
	case catalog.KindCLI, catalog.KindACP:
		// The model is a program on this machine, so readiness is a PATH lookup
		// and nothing else: no key, because the CLI holds its own credential.
		bin := providerBinary(p)
		out.Detail = bin
		if path, err := exec.LookPath(bin); err != nil {
			out.Ready = false
			out.Problem = bin + " is not on PATH"
		} else {
			out.Detail = path
		}
		if len(p.Command) == 0 && p.Kind == catalog.KindCLI && !knownPreset(p.Preset) {
			out.Ready = false
			out.Problem = "no preset named " + p.Preset + " and no explicit command"
		}
		return out
	}
	out.Ready, out.Problem = false, "unknown kind "+string(p.Kind)
	return out
}

// providerBinary is the program a local provider would actually execute.
func providerBinary(p catalog.Provider) string {
	if len(p.Command) > 0 {
		return p.Command[0]
	}
	if p.Preset != "" {
		return p.Preset
	}
	return "devin"
}

func knownPreset(preset string) bool {
	switch preset {
	case "", "devin", "claude", "copilot":
		return true
	}
	return false
}

package engine

import (
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/bundle"
	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/shell"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
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
// DoctorStorage names the drivers in use. It carries NO dsn: a postgres dsn
// holds a password, and this endpoint is unauthenticated.
type DoctorStorage struct {
	Driver    string `json:"driver"`
	Artifacts string `json:"artifacts"`
}

type Doctor struct {
	Default     DoctorModel      `json:"default"`
	Providers   []DoctorProvider `json:"providers"`
	Classifiers []DoctorProvider `json:"classifiers"`
	Skills      DoctorCount      `json:"skills"`
	Mcp         DoctorCount      `json:"mcp"`
	Workflows   DoctorCount      `json:"workflows"`
	Models      []string         `json:"models"`
	Shell       DoctorShell      `json:"shell"`
	// Storage is what this server is ACTUALLY running on. The UI used to print
	// a hardcoded "postgres · s3" in its header, which became a lie the moment
	// the sqlite/folder path existed — a status line nobody can trust is worse
	// than none.
	Storage DoctorStorage `json:"storage"`
	// Problems is never nil. A nil slice marshals to `null`, and the one
	// consumer that reads it does `problems.length` — so a machine with
	// NOTHING wrong crashed the System page, while a broken one rendered fine.
	// The healthy case is the one nobody tests.
	Problems []string `json:"problems"`
	// Notes is what is satisfied and still cannot promise a run. A `cli`/`acp`
	// provider is ready on a PATH lookup alone, so its tick means the binary is
	// installed and NOT that anyone is logged into it — a distinction a
	// Problems entry would overstate and a bare tick understates.
	Notes []string `json:"notes"`
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
	// State is missing, present or ready. present means the binary was found
	// and authentication was not established — either nobody checked, or a
	// vendor's own status command said it was not logged in. Ready is true
	// only for ready: a PATH hit is not a login.
	State string `json:"state,omitempty"`
	// AuthUnknown marks present-and-unchecked: the program is on PATH, and
	// whether anybody is authenticated to it was never checked. A logged-out
	// answer is present with AuthUnknown false — the check ran, and it said no.
	AuthUnknown bool `json:"authUnknown,omitempty"`
	// Login is the command the OPERATOR runs to authenticate, where the vendor
	// is one we know. We never run it and never hold what it produces.
	Login string `json:"login,omitempty"`
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
		Notes:    []string{},
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

	d.Storage = DoctorStorage{Driver: e.cfg.StorageDriver, Artifacts: e.cfg.ArtifactDriver}

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
		// present does not block a run. It is a note, and it stays a note
		// when the vendor said "not logged in" — the failure that prevents
		// is one loud auth error, and blocking on it is the false refusal
		// an operator works around within a week.
		if p.State == bundle.StatePresent {
			note := "provider " + p.Name + ": present"
			if p.AuthUnknown {
				note += "; authentication not checked"
			} else {
				note += "; not authenticated"
			}
			if p.Login != "" {
				note += " — to authenticate, run `" + p.Login + "` yourself"
			}
			d.Notes = append(d.Notes, note)
			continue
		}
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
// InspectProvider is checkProvider for a caller outside this package — the
// runner, which must not grow a second readiness path.
func InspectProvider(p catalog.Provider) DoctorProvider { return checkProvider(p) }

func checkProvider(p catalog.Provider) DoctorProvider {
	out := DoctorProvider{Name: p.Name, Kind: string(p.Kind), Model: p.Model, Ready: true, State: bundle.StateReady}
	switch p.Kind {
	case catalog.KindHTTP:
		out.Detail = p.BaseURL
		// An entry with no apiKeyEnv needs no key — the self-hosted case, the
		// same rule the runtime path applies. Without this, a provider that is
		// correctly configured reports `NOT READY:  is not set`, naming no
		// variable because there is none to name. The value is never read.
		if p.APIKeyEnv != "" && os.Getenv(p.APIKeyEnv) == "" {
			out.Ready, out.State, out.Problem = false, bundle.StateMissing, p.APIKeyEnv+" is not set"
		}
		return out
	case catalog.KindCLI, catalog.KindACP:
		// The model is a program on this machine. A PATH hit proves the
		// program exists and nothing else: the credential lives inside it.
		bin := providerBinary(p)
		out.Detail = bin
		path, lookErr := exec.LookPath(bin)
		if lookErr != nil {
			out.Ready, out.State = false, bundle.StateMissing
			out.Problem = bin + " is not on PATH"
		} else {
			out.Detail = path
		}
		if len(p.Command) == 0 && p.Kind == catalog.KindCLI && !knownPreset(p.Preset) {
			out.Ready, out.State = false, bundle.StateMissing
			out.Problem = "no preset named " + p.Preset + " and no explicit command"
		}
		if lookErr == nil && out.State != bundle.StateMissing {
			out.Login = presetLogin(p)
			switch probeAuth(bin, path) {
			case authReady:
				out.Ready, out.State = true, bundle.StateReady
			case authLoggedOut:
				// Checked, and the vendor said no. present, not missing:
				// the binary is there. Not ready: nobody is logged in.
				out.Ready, out.State = false, bundle.StatePresent
			default:
				out.Ready, out.State, out.AuthUnknown = false, bundle.StatePresent, true
			}
		}
		return out
	}
	out.Ready, out.State, out.Problem = false, bundle.StateMissing, "unknown kind "+string(p.Kind)
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

// presetLogin is the command the OPERATOR runs to authenticate a local agent
// CLI, for the vendors the catalog names by preset. It is a POINTER, not an
// action: we never run it, never prompt for what it asks, and never see what it
// stores — the seat is theirs (design §7).
//
// A vendor we have no confident command for gets no guess. A wrong login
// command is worse than none, because it reads as a checked fact.
func presetLogin(p catalog.Provider) string {
	switch providerBinary(p) {
	case "claude":
		return "claude"
	case "copilot":
		return "gh auth login"
	case "codex":
		return "codex login"
	case "opencode":
		return "opencode auth login"
	}
	return ""
}

// DoctorHost answers a pulled bundle's requirements (bundle.Host) from this
// engine: the SAME checkProvider the System page reports, the same mcp catalog,
// and the same worker-label count `wfx dryrun` uses. A second readiness path
// would drift from the one an operator actually reads.
type DoctorHost struct{ e *Engine }

// CheckBundleRequirements is the pull-time gate: a bundle's recorded
// requirements against this machine, refusing with every unmet one at once. A
// bundle that records none asks this machine nothing.
func (e *Engine) CheckBundleRequirements(reqs []bundle.Requirement) bundle.Report {
	return bundle.CheckRequirements(reqs, e.RequirementHost())
}

// RequirementHost is how a caller gets one. It takes no bundle and no
// credential: it answers questions about THIS machine and nothing else.
func (e *Engine) RequirementHost() *DoctorHost { return &DoctorHost{e: e} }

func (h *DoctorHost) Provider(name string) bundle.ProviderReadiness {
	if h.e.catalog == nil {
		return bundle.ProviderReadiness{}
	}
	p, ok := h.e.catalog.Providers.Get(name)
	if !ok {
		return bundle.ProviderReadiness{}
	}
	got := checkProvider(p)
	out := bundle.ProviderReadiness{
		Found: true, Kind: got.Kind, Ready: got.Ready, Problem: got.Problem,
		State: got.State, AuthUnknown: got.AuthUnknown, Login: got.Login,
	}
	if got.State == bundle.StatePresent {
		// The binary is there. The fix is a login, not an install, and only
		// where we know the vendor's command. An invented one would hang.
		out.Fix = got.Login
		return out
	}
	if !got.Ready {
		switch p.Kind {
		case catalog.KindHTTP:
			// The variable's NAME, which is all a provider ever holds.
			out.Fix = "set " + p.APIKeyEnv + " in this machine's environment"
		case catalog.KindCLI, catalog.KindACP:
			bin := providerBinary(p)
			if ins, ok := LookupInstaller(p.Preset); ok && ins.Instruction != "" {
				out.Fix = ins.Instruction
			} else if ins, ok := LookupInstaller(bin); ok && ins.Instruction != "" {
				out.Fix = ins.Instruction
			} else {
				out.Fix = "install " + bin + " on this machine's PATH"
			}
		}
	}
	return out
}

func (h *DoctorHost) HasMcp(name string) bool {
	return h.e.catalog != nil && h.e.catalog.Mcp.Has(name)
}

func (h *DoctorHost) LabelHolders(label string) int {
	// A label this process serves itself needs no worker at all, and the pool
	// count alone would report it as unheld.
	if h.e.servesLocally(label) {
		return 1
	}
	return h.e.labelHolders(&workflow.Step{RunsOn: label})
}

func knownPreset(preset string) bool {
	switch preset {
	case "", "devin", "claude", "copilot":
		return true
	}
	return false
}

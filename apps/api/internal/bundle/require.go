package bundle

import (
	"fmt"
	"sort"
	"strings"
)

// The receiving end of design §7: a bundle records what it needs to RUN, and
// the host it lands on answers "I have it" or "I do not" BEFORE anything is
// materialised.
//
// Refusing here rather than at run time is the same rule publishing already
// applies pointed the other way: a bundle that can never run on this machine
// should not become a run row, a worktree and a first turn's tokens before
// anybody finds out. So the wording follows the publish-time refusals — the
// thing that is missing, where it was asked for, and what would satisfy it.
//
// Nothing in this file accepts, stores or forwards a credential. A fix is a
// command the OPERATOR runs or the NAME of a variable they set themselves;
// there is no field a key could travel in, which is what makes "the platform
// can never hold a CLI's credential" a property of the type rather than a
// promise in a comment.

// ProviderReadiness is the host's answer about one provider. It is what
// engine/doctor.go's checkProvider already computes — this type exists so the
// check can be pure, not so readiness can be decided twice.
//
// AuthUnknown is the third state design §7 insists on. A `cli`/`acp` provider
// is Ready on a PATH lookup alone, so Ready means THE BINARY EXISTS and says
// nothing about whether anyone is logged into it: a pull that passes can still
// fail on turn one with an auth error. Reporting that as a bare tick is the lie
// this field exists to prevent. Probing a vendor's login state is deliberately
// not attempted (design "Not decided here").
type ProviderReadiness struct {
	Found       bool
	Kind        string
	Ready       bool
	Problem     string
	Fix         string
	AuthUnknown bool
	// Login is the command the operator runs themselves, where the catalog
	// knows one for that vendor. Never run for them, and never captured.
	Login string
}

// Host is what the receiving machine can answer. An interface because the
// answers come from the engine — its catalog, its worker pool — and this
// package must not depend on the engine to ask.
type Host interface {
	// Provider reports readiness for a provider NAME as this machine's catalog
	// has it.
	Provider(name string) ProviderReadiness
	// HasMcp reports whether this machine's mcp catalog holds that server.
	HasMcp(name string) bool
	// LabelHolders is how many workers currently serve a `runs-on:` label,
	// counting the labels the platform serves itself — the same count
	// `wfx dryrun` reports locally.
	LabelHolders(label string) int
}

// Unmet is one requirement the host cannot satisfy: what is absent, where it
// was asked for, and what would satisfy it.
type Unmet struct {
	Requirement
	Because string
	Fix     string
}

// Caveat is a requirement that IS satisfied and still cannot promise a run —
// today only "the binary is there; nobody checked the login".
type Caveat struct {
	Requirement
	Note string
	Fix  string
}

// Report is the whole answer, every unmet requirement at once. Naming one and
// stopping makes an operator pull, install, pull again — the loop a refusal is
// supposed to replace.
type Report struct {
	Unmet   []Unmet
	Caveats []Caveat
}

// OK reports whether the pull may proceed. A caveat is not a refusal: the host
// has what was asked for, it simply cannot certify a login.
func (r Report) OK() bool { return len(r.Unmet) == 0 }

// Err is the refusal, in the publish-time vocabulary, naming every unmet
// requirement and its fix.
func (r Report) Err() error {
	if r.OK() {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "cannot pull: this machine does not satisfy %d of the bundle's requirements", len(r.Unmet))
	for _, u := range r.Unmet {
		fmt.Fprintf(&b, "\n  %s %s: %s", u.Kind, u.Name, u.Because)
		if len(u.Steps) > 0 {
			fmt.Fprintf(&b, " (asked for by %s)", strings.Join(u.Steps, ", "))
		}
		if u.Fix != "" {
			fmt.Fprintf(&b, "\n      to satisfy it: %s", u.Fix)
		}
	}
	return fmt.Errorf("%s", b.String())
}

// CheckRequirements answers a manifest's Requires against one host.
//
// A bundle that records nothing is checked against nothing: no requirement, no
// question asked of the host, no report to print. "Absent, not empty" is the
// same rule the manifest itself follows.
func CheckRequirements(reqs []Requirement, h Host) Report {
	var rep Report
	if len(reqs) == 0 || h == nil {
		return rep
	}
	for _, req := range reqs {
		switch req.Kind {
		case ReqProvider:
			checkProviderReq(&rep, req, h)
		case ReqLabel:
			// A label nobody holds is a step that waits forever — the check
			// `wfx dryrun` does locally, done at the receiving end where the
			// pool actually is.
			if h.LabelHolders(req.Name) == 0 {
				rep.Unmet = append(rep.Unmet, Unmet{
					Requirement: req,
					Because:     fmt.Sprintf("no worker online holds the label %q, so the step would wait", req.Name),
					Fix:         fmt.Sprintf("bring a worker online holding it (`wfx-runner join --labels %s`), or add it to this process's WFX_RUNNER_LABELS", req.Name),
				})
			}
		case ReqMcp:
			if !h.HasMcp(req.Name) {
				rep.Unmet = append(rep.Unmet, Unmet{
					Requirement: req,
					Because:     fmt.Sprintf("no mcp server named %q is configured on this machine", req.Name),
					Fix:         fmt.Sprintf("add %q to this machine's mcp.json", req.Name),
				})
			}
		default:
			// An unknown kind comes from a NEWER publisher. Refusing beats
			// ignoring it: a requirement we cannot interpret is one we cannot
			// claim is met.
			rep.Unmet = append(rep.Unmet, Unmet{
				Requirement: req,
				Because:     fmt.Sprintf("unknown requirement kind %q — this bundle was published by a newer version", req.Kind),
				Fix:         "upgrade this host, or publish the bundle from a matching version",
			})
		}
	}
	sort.SliceStable(rep.Unmet, func(i, j int) bool {
		if rep.Unmet[i].Kind != rep.Unmet[j].Kind {
			return rep.Unmet[i].Kind < rep.Unmet[j].Kind
		}
		return rep.Unmet[i].Name < rep.Unmet[j].Name
	})
	return rep
}

func checkProviderReq(rep *Report, req Requirement, h Host) {
	st := h.Provider(req.Name)
	if !st.Found {
		rep.Unmet = append(rep.Unmet, Unmet{
			Requirement: req,
			Because:     fmt.Sprintf("no provider named %q is configured on this machine", req.Name),
			Fix:         providerFix(req.ProviderKind, req.Name, req.APIKeyEnv),
		})
		return
	}
	if !st.Ready {
		fix := st.Fix
		if fix == "" {
			fix = providerFix(req.ProviderKind, req.Name, req.APIKeyEnv)
		}
		rep.Unmet = append(rep.Unmet, Unmet{Requirement: req, Because: st.Problem, Fix: fix})
		return
	}
	if st.AuthUnknown {
		rep.Caveats = append(rep.Caveats, Caveat{
			Requirement: req,
			Note:        "present; authentication not checked",
			Fix:         st.Login,
		})
	}
}

// providerFix is what would satisfy a provider requirement, by KIND — which is
// the whole reason the kind is recorded at publish time. A key satisfies an
// `http` provider; only a binary on THIS machine, logged in by whoever owns the
// seat, satisfies a `cli` or `acp` one.
//
// apiKeyEnv is the name the PUBLISHER used, quoted as a hint and never as an
// instruction — this machine's own entry decides which variable is actually
// read. Saying "where this was published the variable was called X" is what
// turns a true refusal into an actionable one.
func providerFix(kind, name, apiKeyEnv string) string {
	switch kind {
	case "http":
		if apiKeyEnv != "" {
			return fmt.Sprintf("add an `http` provider %q to this machine's registry and set the environment variable its apiKeyEnv NAMES (where this bundle was published that was %s)", name, apiKeyEnv)
		}
		return fmt.Sprintf("add an `http` provider %q to this machine's registry and set the environment variable its apiKeyEnv NAMES", name)
	case "cli", "acp":
		return fmt.Sprintf("add a %s provider %q to this machine's registry and install its command on PATH — the platform cannot hold its credential, so the login stays yours", kind, name)
	}
	return fmt.Sprintf("add a provider %q to this machine's registry", name)
}

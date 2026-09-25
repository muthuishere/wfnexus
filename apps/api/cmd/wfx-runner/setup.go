package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
)

// Hooks so a test can prove what ran without curling a vendor or opening a
// terminal. Production leaves them at the defaults below.
var (
	lookPath                  = exec.LookPath
	runCommand                = defaultRunCommand
	inspectProvider           = engine.InspectProvider
	fetchProviders            = defaultFetchProviders
	joinAfterSetup            = cmdJoin
	setupOut        io.Writer = os.Stdout
)

func defaultRunCommand(ctx context.Context, argv []string, attached bool) error {
	if len(argv) == 0 {
		return errors.New("no command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if attached {
		// The vendor's own process, on this terminal. Nothing is captured:
		// an installer script and a login both talk to the operator, and a
		// buffer here would be a place a credential could land.
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	}
	return cmd.Run()
}

func defaultFetchProviders(ctx context.Context, base, token string) ([]engine.SetupNeed, error) {
	var res struct {
		Providers []engine.SetupNeed `json:"providers"`
	}
	err := call(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/api/workers/providers", token, nil, &res)
	return res.Providers, err
}

// outcome is one provider's line in the report. The three install results
// are installed, already present, and could not. LoginNeeded is separate:
// a binary can be present and still need a login.
type outcome struct {
	Name        string
	Kind        string
	Result      string
	Login       string
	LoginNeeded bool
	Checked     bool
	Detail      string
}

type setupReport struct {
	Outcomes []outcome
	Unmet    int
	Changed  bool
}

func cmdSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(setupOut)
	url := fs.String("url", "", "platform URL, if this machine has not joined")
	token := fs.String("token", "", "registration token from the Workers page, if this machine has not joined")
	labels := fs.String("labels", "self-hosted", "comma-separated labels, used with --join")
	name := fs.String("name", "", "worker name, used with --join")
	workDir := fs.String("work", "", "where checkouts go, used with --join")
	dryRun := fs.Bool("dry-run", false, "print what would be installed and install nothing")
	login := fs.Bool("login", false, "run each vendor's own login command in this terminal")
	requireAuth := fs.Bool("require-authenticated", false, "treat a present-but-not-authenticated provider as unmet")
	doJoin := fs.Bool("join", false, "join the pool after setup, with the same flags join takes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *doJoin && (*url == "" || *token == "") {
		return errors.New("setup --join needs --url and --token, the same ones join takes")
	}
	base, tok, err := resolveSetupTarget(*url, *token)
	if err != nil {
		return err
	}
	if strings.HasPrefix(strings.ToLower(base), "http://") {
		fmt.Fprintln(os.Stderr, "note: talking to the platform over plain http — the token crosses the network in clear. Fine on a trusted network; use https over anything else.")
	}
	needs, err := fetchProviders(context.Background(), base, tok)
	if err != nil {
		return err
	}
	rep := prepare(context.Background(), needs, setupOpts{
		DryRun: *dryRun, Login: *login, RequireAuthenticated: *requireAuth,
	})
	printReport(rep)
	if rep.Unmet > 0 {
		// Joining a machine that is not prepared would put it in the pool
		// and then send it work it cannot do. --join does not override that.
		return fmt.Errorf("setup incomplete: %d unmet", rep.Unmet)
	}
	if *doJoin {
		joinArgs := []string{"--url", *url, "--token", *token, "--labels", *labels}
		if *name != "" {
			joinArgs = append(joinArgs, "--name", *name)
		}
		if *workDir != "" {
			joinArgs = append(joinArgs, "--work", *workDir)
		}
		return joinAfterSetup(joinArgs)
	}
	return nil
}

func resolveSetupTarget(url, token string) (string, string, error) {
	if url != "" || token != "" {
		if url == "" || token == "" {
			return "", "", errors.New("--url and --token are both required before this machine has joined")
		}
		return strings.TrimRight(url, "/"), token, nil
	}
	cfg, err := loadConfig()
	if err != nil {
		return "", "", err
	}
	return cfg.URL, cfg.Token, nil
}

type setupOpts struct {
	DryRun               bool
	Login                bool
	RequireAuthenticated bool
}

func prepare(ctx context.Context, needs []engine.SetupNeed, opt setupOpts) setupReport {
	var rep setupReport
	for _, n := range needs {
		rep.Outcomes = append(rep.Outcomes, prepareOne(ctx, n, opt))
	}
	for _, o := range rep.Outcomes {
		if o.Result == "installed" {
			rep.Changed = true
		}
		if o.Result == "could not" || (opt.RequireAuthenticated && o.LoginNeeded) {
			rep.Unmet++
		}
	}
	return rep
}

func prepareOne(ctx context.Context, n engine.SetupNeed, opt setupOpts) outcome {
	o := outcome{Name: n.Name, Kind: n.Kind}
	switch n.Kind {
	case string(catalog.KindHTTP):
		return prepareHTTP(n, o)
	case string(catalog.KindCLI), string(catalog.KindACP):
		return prepareLocal(ctx, n, o, opt)
	default:
		o.Result = "could not"
		o.Detail = "unknown kind " + n.Kind
		return o
	}
}

// prepareHTTP reports whether the named variable is set. It does not read
// the value, and it does not install anything — an http provider's readiness
// is an environment variable, which is the operator's to set.
func prepareHTTP(n engine.SetupNeed, o outcome) outcome {
	if n.APIKeyEnv == "" || os.Getenv(n.APIKeyEnv) != "" {
		o.Result = "already present"
		return o
	}
	o.Result = "could not"
	o.Detail = "set " + n.APIKeyEnv + " in this machine's environment"
	return o
}

func prepareLocal(ctx context.Context, n engine.SetupNeed, o outcome, opt setupOpts) outcome {
	bin := n.Binary
	if bin == "" {
		bin = n.Preset
	}
	if bin == "" {
		o.Result = "could not"
		o.Detail = "no binary named for " + n.Name
		return o
	}
	_, err := lookPath(bin)
	installed := false
	if err != nil {
		ins, ok := installerFor(n)
		switch {
		case !ok || len(ins.Command) == 0:
			o.Result = "could not"
			if ok && ins.Instruction != "" {
				o.Detail = ins.Instruction
			} else {
				o.Detail = "no installer for " + bin + " — install it yourself; nothing was run"
			}
			return o
		case opt.DryRun:
			o.Result = "could not"
			o.Detail = "would install: " + ins.Instruction
			return o
		default:
			if runErr := runCommand(ctx, ins.Command, true); runErr != nil {
				o.Result = "could not"
				o.Detail = ins.Instruction
				return o
			}
			if _, err := lookPath(bin); err != nil {
				o.Result = "could not"
				o.Detail = "installer ran and " + bin + " is still not on PATH"
				return o
			}
			installed = true
		}
	}
	got := inspectProvider(catalog.Provider{
		Name: n.Name, Kind: catalog.ProviderKind(n.Kind), Preset: n.Preset,
		Command: []string{bin}, APIKeyEnv: n.APIKeyEnv,
	})
	if installed {
		o.Result = "installed"
	} else {
		o.Result = "already present"
	}
	o.Login = got.Login
	o.Checked = !got.AuthUnknown
	if got.State != "ready" {
		o.LoginNeeded = true
		if got.Login == "" {
			o.Detail = "no login command known for " + bin + "; not guessing"
		}
	}
	if opt.Login && o.LoginNeeded {
		if got.Login == "" {
			return o
		}
		// The vendor's argv, split on spaces because every known login is a
		// fixed command with no quoting. Not a shell, and not a string that
		// arrived from the platform.
		if err := runCommand(ctx, strings.Fields(got.Login), true); err != nil {
			o.Detail = "login command failed: " + got.Login
			return o
		}
		again := inspectProvider(catalog.Provider{
			Name: n.Name, Kind: catalog.ProviderKind(n.Kind), Preset: n.Preset,
			Command: []string{bin},
		})
		if again.State == "ready" {
			o.LoginNeeded = false
			o.Checked = true
		}
	}
	return o
}

// installerFor keys the allowlist on the preset, then the binary name. A
// string that is not one of those names — including anything a bundle might
// have carried — is a miss, and a miss runs nothing.
func installerFor(n engine.SetupNeed) (engine.Installer, bool) {
	if n.Preset != "" {
		if ins, ok := engine.LookupInstaller(n.Preset); ok {
			return ins, true
		}
	}
	if n.Binary != "" {
		if ins, ok := engine.LookupInstaller(n.Binary); ok {
			return ins, true
		}
	}
	return engine.Installer{}, false
}

func printReport(rep setupReport) {
	if len(rep.Outcomes) == 0 {
		fmt.Fprintln(setupOut, "nothing to do")
		return
	}
	for _, o := range rep.Outcomes {
		fmt.Fprintf(setupOut, "%s (%s): %s\n", o.Name, o.Kind, o.Result)
		if o.Detail != "" {
			fmt.Fprintf(setupOut, "  %s\n", o.Detail)
		}
		if o.LoginNeeded {
			if o.Login != "" {
				if o.Checked {
					fmt.Fprintf(setupOut, "  login still needed — run `%s` yourself\n", o.Login)
				} else {
					fmt.Fprintf(setupOut, "  present; authentication not checked — to authenticate, run `%s` yourself\n", o.Login)
				}
			}
		}
	}
	switch {
	case rep.Unmet > 0:
		fmt.Fprintf(setupOut, "not done: %d unmet\n", rep.Unmet)
	case !rep.Changed:
		fmt.Fprintln(setupOut, "nothing to do")
	}
}

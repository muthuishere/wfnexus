package engine

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/store"
)

// Installing and configuring what a module wraps, from the Modules page.

// CLIStatus is one required CLI on this server.
type CLIStatus struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Install   string `json:"install,omitempty"`
}

// ModuleCLIs runs each required CLI's check (5s cap) on this server.
func (e *Engine) ModuleCLIs(ctx context.Context, v ModuleView) []CLIStatus {
	out := []CLIStatus{}
	for _, c := range v.Requires.CLI {
		st := CLIStatus{Name: c.Name, Install: c.Install}
		if c.Check != "" {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			b, err := exec.CommandContext(cctx, "sh", "-c", c.Check).CombinedOutput()
			cancel()
			st.Installed = err == nil
			if st.Installed {
				st.Version, _, _ = strings.Cut(strings.TrimSpace(string(b)), "\n")
			}
		}
		out = append(out, st)
	}
	return out
}

// InstallModuleCLI runs the module's declared install command for one CLI on
// this server and returns the combined output. Only a declared command runs.
func (e *Engine) InstallModuleCLI(ctx context.Context, module, cli string) (string, error) {
	mods, _ := e.Modules()
	for _, m := range mods {
		if m.Name != module {
			continue
		}
		for _, c := range m.Requires.CLI {
			if c.Name != cli {
				continue
			}
			if c.Install == "" {
				return "", fmt.Errorf("module %s declares no install command for %s", module, cli)
			}
			cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
			defer cancel()
			b, err := exec.CommandContext(cctx, "sh", "-c", c.Install).CombinedOutput()
			return string(b), err
		}
		return "", fmt.Errorf("module %s does not require %s", module, cli)
	}
	return "", fmt.Errorf("no module %s", module)
}

// ConfigureModule sets one of a module's declared env vars / secrets in the
// platform's system env store. Undeclared names are refused.
func (e *Engine) ConfigureModule(ctx context.Context, module, name, value string) error {
	mods, _ := e.Modules()
	for _, m := range mods {
		if m.Name != module {
			continue
		}
		for _, n := range m.Requires.Env {
			if n == name {
				return e.SetEnvVar(ctx, store.ScopeSystem, "", name, value, false)
			}
		}
		for _, n := range m.Requires.Secrets {
			if n == name {
				return e.SetEnvVar(ctx, store.ScopeSystem, "", name, value, true)
			}
		}
		return fmt.Errorf("module %s does not declare %s", module, name)
	}
	return fmt.Errorf("no module %s", module)
}

// DefaultProvider is the provider a step with none runs on ("" = built-in).
func (e *Engine) DefaultProvider() string { return e.cfg.DefaultProvider }

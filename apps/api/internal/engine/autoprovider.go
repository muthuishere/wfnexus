package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/bundle"
	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
)

// preferredAgents is the order a default is picked in when the operator named
// none: claude first, codex second, then every other agent CLI or ACP entry
// the registry knows, by name. Only local agent programs qualify — a default
// never resolves to an API endpoint that needs a key.
var preferredAgents = []string{"claude-cli", "codex-cli"}

// pickDefaultProvider sets the default provider to the first agent CLI that is
// on this machine and logged in, when config said nothing about a default model.
func (e *Engine) pickDefaultProvider() {
	if !e.cfg.AutoProvider || e.cfg.DefaultProvider != "" {
		return
	}
	if name := e.firstAvailableAgent(); name != "" {
		e.cfg.DefaultProvider, e.autoProvider = name, name
	}
}

func (e *Engine) firstAvailableAgent() string {
	var rest []string
	for _, p := range e.Catalog().Providers.List() {
		if p.Kind != catalog.KindCLI && p.Kind != catalog.KindACP {
			continue
		}
		if p.Name != preferredAgents[0] && p.Name != preferredAgents[1] {
			rest = append(rest, p.Name)
		}
	}
	sort.Strings(rest)
	e.autoSkipped = nil
	for _, n := range append(append([]string{}, preferredAgents...), rest...) {
		p, ok := e.Catalog().Providers.Get(n)
		if !ok {
			continue
		}
		if c := checkProviderWith(p, nil); !c.Ready && c.State != bundle.StatePresent {
			continue // not installed: not worth naming
		}
		if ok, why := loginCheck(providerBinary(p)); !ok {
			e.autoSkipped = append(e.autoSkipped, n+": "+why)
			continue
		}
		return n
	}
	return ""
}

// AutoProvider is the agent CLI the engine picked as its default, "" if none.
func (e *Engine) AutoProvider() string { return e.autoProvider }

// errNoAgentCLI is what a step with no provider gets when no default was named
// and no usable agent CLI is installed. It is refused, never sent to an API.
func (e *Engine) errNoAgentCLI() error {
	msg := "no agent CLI is available on this machine (claude, codex, opencode, …): " +
		"install and log in to one, or name a provider — wfnexus never falls back to an API key"
	if len(e.autoSkipped) > 0 {
		msg += " (skipped: " + strings.Join(e.autoSkipped, "; ") + ")"
	}
	return fmt.Errorf("%s", msg)
}

// loginCheck runs `<bin> --version` and, for the CLIs with a cheap login
// status command (claude, codex), that command. It dials no model. Results are
// cached for loginTTL so doctor and boot do not re-exec the CLIs.
func loginCheck(bin string) (bool, string) {
	if path, err := exec.LookPath(bin); err == nil {
		bin = path // the cache is per installed binary, not per name
	}
	loginMu.Lock()
	if r, ok := loginCache[bin]; ok && time.Since(r.at) < loginTTL {
		loginMu.Unlock()
		return r.ok, r.why
	}
	loginMu.Unlock()
	ok, why := runLoginCheck(bin)
	loginMu.Lock()
	loginCache[bin] = loginResult{ok: ok, why: why, at: time.Now()}
	loginMu.Unlock()
	return ok, why
}

type loginResult struct {
	ok  bool
	why string
	at  time.Time
}

var (
	loginMu    sync.Mutex
	loginCache = map[string]loginResult{}
	loginTTL   = 10 * time.Minute
)

func runLoginCheck(bin string) (bool, string) {
	out := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		b, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
		return string(b), err
	}
	if _, err := out("--version"); err != nil {
		return false, "`" + bin + " --version` failed: " + err.Error()
	}
	switch base := bin[strings.LastIndex(bin, "/")+1:]; base {
	case "claude":
		s, _ := out("auth", "status")
		var st struct {
			LoggedIn *bool `json:"loggedIn"`
		}
		if json.Unmarshal([]byte(s), &st) == nil && st.LoggedIn != nil && !*st.LoggedIn {
			return false, "not logged in (run `claude` and /login)"
		}
	case "codex":
		s, _ := out("login", "status")
		if strings.Contains(strings.ToLower(s), "not logged in") {
			return false, "not logged in (run `codex login`)"
		}
	}
	return true, ""
}

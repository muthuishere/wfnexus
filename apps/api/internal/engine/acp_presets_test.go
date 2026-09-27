package engine

import (
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
)

// Each adapter launches its agent its own way. Before presets, every acp entry
// was spoken to as devin: `opencode --model m acp acp` — a devin-only flag and
// a doubled subcommand.
func TestEachACPAdapterLaunchesItsAgentItsOwnWay(t *testing.T) {
	cases := []struct {
		p                    catalog.Provider
		bin, argv, modelFlag string
	}{
		{catalog.Provider{Kind: catalog.KindACP, Preset: "devin"}, "devin", "acp", "--model"},
		{catalog.Provider{Kind: catalog.KindACP}, "devin", "acp", "--model"},
		{catalog.Provider{Kind: catalog.KindACP, Preset: "opencode"}, "opencode", "acp", ""},
		{catalog.Provider{Kind: catalog.KindACP, Preset: "codex"}, "npx", "-y @zed-industries/codex-acp", ""},
		// An explicit command runs exactly as written; the model goes over the protocol.
		{catalog.Provider{Kind: catalog.KindACP, Command: []string{"gemini", "--experimental-acp"}}, "gemini", "--experimental-acp", ""},
	}
	for _, c := range cases {
		cfg := acpConfig(c.p, "m", "/w", []string{"K=V"})
		got := cfg.Bin + " | " + strings.Join(cfg.Argv, " ") + " | " + cfg.ModelFlag
		want := c.bin + " | " + c.argv + " | " + c.modelFlag
		if got != want {
			t.Errorf("%+v: got %q, want %q", c.p, got, want)
		}
		if cfg.Env[len(cfg.Env)-1] != "K=V" || cfg.Model != "m" {
			t.Errorf("%+v: env/model not carried: %+v", c.p, cfg)
		}
	}
}

func TestTheDoctorLooksForTheProgramAnAdapterReallyRuns(t *testing.T) {
	if b := providerBinary(catalog.Provider{Kind: catalog.KindACP, Preset: "codex"}); b != "npx" {
		t.Errorf("codex over ACP runs through npx, doctor looked for %q", b)
	}
}

// opencode is driven as a model: its own tools must be off, or its build agent
// does the task itself with its own bash and the step never finishes a turn.
func TestOpencodeIsLaunchedWithItsOwnToolsDenied(t *testing.T) {
	cfg := acpConfig(catalog.Provider{Kind: catalog.KindACP, Preset: "opencode"}, "m", "/w", []string{"K=V"})
	env := strings.Join(cfg.Env, "\n")
	for _, tool := range []string{`"bash":"deny"`, `"edit":"deny"`, `"read":"deny"`, `"webfetch":"deny"`} {
		if !strings.Contains(env, tool) {
			t.Errorf("opencode env lacks %s:\n%s", tool, env)
		}
	}
	if cfg.Env[len(cfg.Env)-1] != "K=V" {
		t.Error("the step's env must come after the adapter's, so it can override")
	}
}

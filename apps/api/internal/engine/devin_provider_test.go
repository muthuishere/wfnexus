package engine

import (
	"strings"
	"testing"

	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/devinadapter"
)

// A devin registry entry carries the step's model and its permission mode to
// the CLI in both kinds.
func TestDevinEntryCarriesModelAndPermissionMode(t *testing.T) {
	cliEntry := catalog.Provider{Name: "devin", Kind: catalog.KindCLI, Preset: "devin", Model: "swe", PermissionMode: "accept-edits"}
	ag, _, err := localAgent(cliEntry, "opus", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Join(ag.(*devinadapter.CommandAgent).Args, " ")
	if !strings.Contains(args, "--model opus") || !strings.Contains(args, "--permission-mode accept-edits") {
		t.Fatalf("cli argv = %s", args)
	}

	acpEntry := catalog.Provider{Name: "devin-acp", Kind: catalog.KindACP, Preset: "devin", PermissionMode: "accept-edits"}
	cfg, err := acpConfig(acpEntry, "opus", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "opus" || cfg.ModelFlag != "--model" || cfg.Mode != "accept-edits" {
		t.Fatalf("acp cfg = model %q flag %q mode %q", cfg.Model, cfg.ModelFlag, cfg.Mode)
	}
}

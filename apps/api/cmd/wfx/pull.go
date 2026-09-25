package main

import (
	"fmt"
	"path/filepath"

	"github.com/muthuishere/wfnexus/apps/api/internal/bundle"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
)

// pull is the receiving half. The server materialises the bundle into a
// bundle-scoped skill root and saves the workflow; this verb asks it to and
// REPORTS what the bundle carries against what this machine already holds —
// naming the skill and printing BOTH digests rather than silently preferring
// either.
//
// The server also checks the bundle's recorded requirements against ITSELF
// before installing anything (design §7), because it is the machine that will
// run the steps. A refusal arrives here as the error, naming every unmet
// requirement at once and what would satisfy each. What IS satisfied and still
// cannot promise a run arrives as a note — a `cli` provider found on PATH is
// never printed as a run that will work.
func pull(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: wfx pull <digest> [--url host]")
	}
	digest := args[0]
	var out struct {
		Workflow  string         `json:"workflow"`
		SkillRoot string         `json:"skillRoot"`
		Skills    []bundle.Entry `json:"skills"`
		Notes     []string       `json:"notes"`
	}
	if err := call("POST", "/api/bundles/"+digest+"/install", nil, &out); err != nil {
		return err
	}
	cfg := config.Load()
	local := skills.Load(skills.DefaultRoots(cfg.SkillsDir)...)
	fmt.Printf("pulled %s\n  skills into %s\n", out.Workflow, out.SkillRoot)
	for _, n := range out.Notes {
		fmt.Printf("  NOTE %s\n", n)
	}
	for _, n := range out.Notes {
		fmt.Printf("  NOTE %s\n", n)
	}
	for _, sk := range out.Skills {
		fmt.Printf("  %-20s %s\n", sk.Name, sk.Digest)
		got, ok := local.Get(sk.Name)
		if !ok {
			continue
		}
		mine, _, err := bundle.DigestDir(filepath.Dir(got.Location))
		if err != nil {
			continue
		}
		if mine != sk.Digest {
			// Named, with both digests. Never a silent preference.
			fmt.Printf("    NOTE this machine also holds %q with a DIFFERENT digest\n      bundle %s\n      local  %s (%s)\n      the run uses the BUNDLE's copy\n",
				sk.Name, sk.Digest, mine, got.Location)
		}
	}
	return nil
}

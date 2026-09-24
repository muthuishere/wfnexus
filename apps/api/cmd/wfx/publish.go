package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/bundle"
	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// `wfx publish` — the publish direction, from the machine that HAS the
// dependencies (ADR 0018).
//
// Resolution happens HERE, against this machine's skill roots, and the content
// travels. Resolving on the receiving machine would let the same workflow mean
// two different things on two machines because their roots differ, which is
// exactly what publishing exists to stop.

func publish(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: wfx publish <file.yaml> --version <semver> [--as name] [--project p] [--tag t]")
	}
	path := args[0]
	version := flagOf(args, "--version", "")
	if version == "" {
		return fmt.Errorf("--version is required: a published version is immutable, so it has to be named")
	}

	// A publish has to go SOMEWHERE. There is no local-only or degraded mode:
	// with no host configured this is an error and nothing is written.
	target, err := resolveContext(flagOf(args, "--url", ""))
	if err != nil {
		return err
	}
	if target.token == "" {
		return fmt.Errorf("no host: run `wfx login --url <host>` first — publishing records who published, so it needs a subject")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	def, err := workflow.ParseYAML(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if as := flagOf(args, "--as", ""); as != "" {
		def.Name = as
		if raw, err = workflow.MarshalYAML(def); err != nil {
			return err
		}
	}
	if def.Name == "" {
		return fmt.Errorf("%s: the workflow has no name", path)
	}

	cfg := config.Load()
	reg := skills.Load(skills.DefaultRoots(cfg.SkillsDir)...)
	cat, err := catalog.Load(cfg.RegistriesPath, cfg.McpConfig)
	if err != nil {
		return err
	}
	project := flagOf(args, "--project", "")
	// Build applies both publish-time refusals that can be answered locally —
	// an unresolvable skill or MCP reference, and a literal credential — so a
	// refused publish sends nothing at all.
	b, err := bundle.Build("workflow", project, def.Name, version, raw, def, reg, cat)
	if err != nil {
		return err
	}
	digest, err := b.Verify("")
	if err != nil {
		return err
	}
	tarGz, err := b.Pack()
	if err != nil {
		return err
	}

	var out map[string]any
	body := map[string]any{
		"kind": "workflow", "project": project, "name": def.Name,
		"version": version, "digest": digest, "tarGz": tarGz,
		"tag": flagOf(args, "--tag", ""),
	}
	if err := callWith(target, "POST", "/api/bundles", body, &out); err != nil {
		return err
	}
	if has(args, "--json") {
		raw, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(raw))
		return nil
	}
	fmt.Printf("published %s@%s\n  digest   %s\n  skills   %d\n  by       %v at %v\n",
		def.Name, version, digest, len(b.Manifest.Skills), out["publishedBy_name"], out["publishedAt"])
	for _, sk := range b.Manifest.Skills {
		fmt.Printf("  skill    %-20s %s\n", sk.Name, sk.Digest)
	}
	return nil
}

// pull is the receiving half. The server materialises the bundle into a
// bundle-scoped skill root and saves the workflow; this verb asks it to and
// REPORTS what the bundle carries against what this machine already holds —
// naming the skill and printing BOTH digests rather than silently preferring
// either.
func pull(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: wfx pull <digest> [--url host]")
	}
	digest := args[0]
	var out struct {
		Workflow  string         `json:"workflow"`
		SkillRoot string         `json:"skillRoot"`
		Skills    []bundle.Entry `json:"skills"`
	}
	if err := call("POST", "/api/bundles/"+digest+"/install", nil, &out); err != nil {
		return err
	}
	cfg := config.Load()
	local := skills.Load(skills.DefaultRoots(cfg.SkillsDir)...)
	fmt.Printf("pulled %s\n  skills into %s\n", out.Workflow, out.SkillRoot)
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

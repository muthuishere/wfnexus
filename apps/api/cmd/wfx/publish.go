package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
		return fmt.Errorf("usage: wfx publish <file.yaml> --version <semver> [--to <git remote>] [--as name] [--project p] [--tag t]")
	}
	path := args[0]
	version := flagOf(args, "--version", "")
	if version == "" {
		return fmt.Errorf("--version is required: a published version is immutable, so it has to be named")
	}

	// A publish has to go SOMEWHERE. There is no local-only or degraded mode:
	// the sink is a host or a git remote, and with neither this is an error and
	// nothing is written.
	remote := flagOf(args, "--to", "")
	var target resolved
	if remote == "" {
		t, err := resolveContext(flagOf(args, "--url", ""))
		if err != nil {
			return err
		}
		// Only the HOST sink needs an account with us. A publish to a git
		// remote is a push, and git's own credentials do it — we never read,
		// store or prompt for one (design §5).
		if t.token == "" {
			return fmt.Errorf("no host: run `wfx login --url <host>` first — publishing records who published, so it needs a subject (or publish to a git remote with --to <remote>)")
		}
		target = t
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
	// A `uses:` workflow cannot be published, and publish is where that has to be
	// said. ParseYAML does NOT expand uses — expansion happens in the loader — so
	// at this point a uses-only workflow has NO steps, which means Named() finds
	// no skills to carry, Requirements() records nothing for the receiving host to
	// check, and the carried workflow.yaml names a task the bundle does not
	// include. The result is a bundle that publishes cleanly and cannot possibly
	// run.
	//
	// It surfaces today as "carries no steps to expand" when somebody else
	// consumes it — a confusing message, on another machine, long after the
	// mistake. ADR 0018's whole argument for this verb is that failing here beats
	// failing there.
	if len(def.Uses) > 0 {
		names := make([]string, 0, len(def.Uses))
		for _, u := range def.Uses {
			names = append(names, u.Task)
		}
		return fmt.Errorf("cannot publish %s: it is built from `uses:` (%s), and a bundle carries the workflow's own steps — "+
			"the task those names refer to would not travel with it. Publish a workflow with its own `steps:`, "+
			"or publish the task itself and reference it remotely with `use: owner/repo@tag`",
			def.Name, strings.Join(names, ", "))
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
	if remote != "" {
		// Every refusal above has already run, so a refused publish reaches no
		// clone, makes no commit and pushes nothing.
		return publishToGit(remote, def.Name, version, b, digest)
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

// publishToGit is the second sink (design §4): the bundle written as a
// readable tree, committed, tagged and pushed. Nothing above this line
// changes — one bundle builder, one manifest, one set of refusals, two writers.
//
// Git is shelled out to exactly as internal/remoteuse does on the reading
// side: `insteadOf`, the credential helper and the SSH agent are then the
// operator's own, and wfnexus never reads, stores or prompts for a git
// credential.
func publishToGit(remote, name, version string, b *bundle.Bundle, digest string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	dir, err := os.MkdirTemp("", "wfx-publish-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	tag := name + "/v" + version
	fail := func(step string, out []byte, e error) error {
		return fmt.Errorf("git %s on %s: %v: %s", step, remote, e, strings.TrimSpace(string(out)))
	}
	git := func(args ...string) ([]byte, error) {
		return exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	}
	// --no-tags deliberately: the clone does not learn the remote's tags, so a
	// version that already exists is refused by the REMOTE rejecting the push
	// rather than by a local check we could get wrong. Immutability is git's.
	if out, e := exec.CommandContext(ctx, "git", "clone", "--quiet", "--no-tags", remote, dir).CombinedOutput(); e != nil {
		return fail("clone", out, e)
	}
	// A clone can come back EMPTY even though the remote has branches: that
	// happens when the remote's HEAD names a branch that does not exist there,
	// which a bare repository created with one default branch name and pushed to
	// with another does routinely.
	//
	// Publishing into that empty checkout is the dangerous outcome, not the
	// obvious one. The commit below would succeed, the push would succeed, and
	// the result would be a DIVERGENT branch holding this bundle and nothing
	// else — every bundle already published to that repository orphaned on the
	// branch nobody is now looking at. So this refuses instead, and names the
	// branches it can see, because the operator is one `git symbolic-ref` from
	// fixing it and has no way to guess that from a successful publish.
	if _, e := git("rev-parse", "--verify", "--quiet", "HEAD"); e != nil {
		heads, _ := exec.CommandContext(ctx, "git", "ls-remote", "--heads", remote).CombinedOutput()
		if names := branchNames(heads); len(names) > 0 {
			return fmt.Errorf("cannot publish to %s: it has branches (%s) but its HEAD points at none of them, so the clone is empty — publishing now would commit this bundle to a new branch and orphan everything already there.\n  fix it on the remote with: git symbolic-ref HEAD refs/heads/%s",
				remote, strings.Join(names, ", "), names[0])
		}
		// No branches at all: a genuinely empty repository, which is the
		// ordinary first publish. An initial commit is exactly right.
	}
	treeDir := filepath.Join(dir, filepath.FromSlash(bundle.TreeDir(name, version)))
	if err := os.MkdirAll(treeDir, 0o755); err != nil {
		return err
	}
	if err := b.WriteTree(treeDir); err != nil {
		return fmt.Errorf("cannot publish %s@%s: it is already in %s — a published version is immutable: %w", name, version, remote, err)
	}
	if out, e := git("add", "--all", bundle.TreeDir(name, version)); e != nil {
		return fail("add", out, e)
	}
	if out, e := git("commit", "--quiet", "-m",
		fmt.Sprintf("publish %s@%s\n\ndigest %s", name, version, digest)); e != nil {
		return fail("commit", out, e)
	}
	if out, e := git("tag", tag); e != nil {
		return duplicateVersion(name, version, remote, tag, out, e)
	}
	// --atomic, so a rejected tag leaves the branch unmoved too: a refused
	// publish must leave the remote exactly as it was, not half-published.
	if out, e := git("push", "--atomic", "origin", "HEAD", "refs/tags/"+tag); e != nil {
		return duplicateVersion(name, version, remote, tag, out, e)
	}

	fmt.Printf("published %s@%s\n  remote   %s\n  tree     %s\n  tag      %s\n  digest   %s\n  skills   %d\n",
		name, version, remote, bundle.TreeDir(name, version), tag, digest, len(b.Manifest.Skills))
	for _, sk := range b.Manifest.Skills {
		fmt.Printf("  skill    %-20s %s\n", sk.Name, sk.Digest)
	}
	for _, rq := range b.Manifest.Requires {
		fmt.Printf("  needs    %-8s %s %s\n", rq.Kind, rq.Name, rq.ProviderKind)
	}
	return nil
}

// branchNames reads the branch names out of `git ls-remote --heads` output.
// Used only to make a refusal specific — a refusal that cannot name what it saw
// is one the operator has to reproduce by hand before they can act on it.
func branchNames(out []byte) []string {
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		_, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok {
			continue
		}
		if b, ok := strings.CutPrefix(ref, "refs/heads/"); ok && b != "" {
			names = append(names, b)
		}
	}
	sort.Strings(names)
	return names
}

// duplicateVersion turns git's rejection into the refusal the server path gives
// for a version that already exists — one vocabulary, two mechanisms. Raw git
// output is kept only for a failure that is NOT a duplicate, because "! [remote
// rejected] ... (already exists)" tells an operator nothing about what they
// should do instead.
func duplicateVersion(name, version, remote, tag string, out []byte, e error) error {
	text := string(out)
	if strings.Contains(text, "already exists") || strings.Contains(text, "rejected") {
		return fmt.Errorf("cannot publish %s@%s: %s already carries the tag %s — a published version is immutable, so publish a higher version instead",
			name, version, remote, tag)
	}
	return fmt.Errorf("git push to %s: %v: %s", remote, e, strings.TrimSpace(text))
}

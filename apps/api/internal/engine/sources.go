package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Importing a repository is how workflows arrive from outside: clone it once,
// read its `.wfx/workflows/`, and its workflows become runnable here.
//
// This is the same arrangement as `.github/workflows/` and it is chosen for the
// same reasons — a workflow lives next to the code it knows about, it is
// reviewed in the pull request that changes it, and a clone brings it along.
//
// The list of imported repositories is machine state, not code, so it lives in
// the runtime directory rather than the repository. That is the rule this
// project already follows everywhere: code in the repo, runtime outside it.

// sourcesFile is where the imported list is remembered, under the runtime dir.
const sourcesFile = "sources.json"

var importMu sync.Mutex

// safeName limits a source name to what can be half of a `source/workflow`
// reference and a directory name, so importing cannot write outside the
// runtime directory by being called `../../etc`.
var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// Sources returns the platform's own directory plus every imported repository.
// The platform's own is always first, so its names win a collision — importing
// a repository never changes what an existing name does.
// templatesSource is the source name the shipped templates load under.
const templatesSource = "templates"

func (e *Engine) Sources() []workflow.Source {
	out := []workflow.Source{{Name: "local", Dir: e.cfg.WorkflowsDir}}
	// The templates the platform ships. They are loaded by the same loader as
	// everything else — a template that would not load is a broken template,
	// found at boot rather than by the first person who copies it.
	if e.cfg.TemplatesDir != "" {
		out = append(out, workflow.Source{Name: templatesSource, Dir: e.cfg.TemplatesDir})
	}
	imported, err := e.readSources()
	if err != nil {
		return out
	}
	return append(out, imported...)
}

func (e *Engine) sourcesPath() string { return filepath.Join(e.cfg.WorkDir, sourcesFile) }

func (e *Engine) readSources() ([]workflow.Source, error) {
	raw, err := os.ReadFile(e.sourcesPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []workflow.Source
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", e.sourcesPath(), err)
	}
	return out, nil
}

func (e *Engine) writeSources(list []workflow.Source) error {
	if err := os.MkdirAll(e.cfg.WorkDir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(workflow.SortedSources(list), "", "  ")
	if err != nil {
		return err
	}
	tmp := e.sourcesPath() + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, e.sourcesPath())
}

// ImportRepo registers a repository as a workflow source and reloads.
//
// `ref` is either a local path — used in place, because a developer wants to
// edit a workflow and re-run it without pushing — or a URL, cloned once under
// the runtime directory.
func (e *Engine) ImportRepo(ctx context.Context, name, ref, branch string) (workflow.Source, error) {
	importMu.Lock()
	defer importMu.Unlock()

	if name == "" {
		name = inferName(ref)
	}
	if !safeName.MatchString(name) {
		return workflow.Source{}, fmt.Errorf("source name %q must start alphanumeric and contain only "+
			"letters, digits, dot, dash and underscore — it becomes half of a `source/workflow` name "+
			"and a directory", name)
	}
	if name == "local" {
		return workflow.Source{}, fmt.Errorf(`"local" is the platform's own workflows; choose another name`)
	}

	var src workflow.Source
	if isLocalPath(ref) {
		abs, err := filepath.Abs(ref)
		if err != nil {
			return workflow.Source{}, err
		}
		if _, err := os.Stat(abs); err != nil {
			return workflow.Source{}, err
		}
		// Used IN PLACE, not copied: editing a workflow and re-running it
		// without a push is the normal development loop, and a copy would make
		// the file you edited not the file that runs.
		src = workflow.RepoSource(name, abs, "")
	} else {
		dir := filepath.Join(e.cfg.WorkDir, "sources", name)
		if err := cloneOrUpdate(ctx, dir, ref, branch); err != nil {
			return workflow.Source{}, err
		}
		src = workflow.RepoSource(name, dir, ref)
	}

	// Refusing an import that contributes nothing, rather than accepting it
	// silently: "imported, and nothing happened" is a confusing state to debug.
	if _, err := os.Stat(src.Dir); os.IsNotExist(err) {
		return workflow.Source{}, fmt.Errorf("%s has no %s directory, so it has no workflows to import",
			ref, workflow.RepoWorkflowDir)
	}

	list, err := e.readSources()
	if err != nil {
		return workflow.Source{}, err
	}
	replaced := false
	for i := range list {
		if list[i].Name == name {
			list[i], replaced = src, true
		}
	}
	if !replaced {
		list = append(list, src)
	}
	if err := e.writeSources(list); err != nil {
		return workflow.Source{}, err
	}
	return src, e.ReloadDefinitions()
}

// ForgetRepo removes a source. The clone is left on disk: deleting a working
// copy somebody may have edited is not something to do as a side effect.
func (e *Engine) ForgetRepo(name string) error {
	importMu.Lock()
	defer importMu.Unlock()

	list, err := e.readSources()
	if err != nil {
		return err
	}
	var keep []workflow.Source
	for _, s := range list {
		if s.Name != name {
			keep = append(keep, s)
		}
	}
	if len(keep) == len(list) {
		return fmt.Errorf("no imported source named %q", name)
	}
	if err := e.writeSources(keep); err != nil {
		return err
	}
	return e.ReloadDefinitions()
}

// cloneOrUpdate clones, or fetches into an existing clone.
func cloneOrUpdate(ctx context.Context, dir, url, branch string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		cmd := exec.CommandContext(ctx, "git", "-C", dir, "pull", "--ff-only")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git pull in %s: %v: %s", dir, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	args := []string{"clone", "--depth", "50"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, url, dir)
	if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("git clone: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// isLocalPath distinguishes a path from a URL. An scp-style git remote
// (git@host:org/repo) has a colon before any slash, which is what separates it
// from a Windows drive letter.
func isLocalPath(ref string) bool {
	if strings.HasPrefix(ref, ".") || strings.HasPrefix(ref, "~") {
		return true
	}
	if strings.Contains(ref, "://") {
		return false
	}
	if i := strings.Index(ref, ":"); i > 1 {
		return false // git@host:org/repo
	}
	return filepath.IsAbs(ref) || !strings.Contains(ref, "@")
}

// inferName takes the repository's own name, which is what a person would have
// typed anyway.
func inferName(ref string) string {
	s := strings.TrimSuffix(strings.TrimRight(ref, "/"), ".git")
	if i := strings.LastIndexAny(s, "/\\:"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

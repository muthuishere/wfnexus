package workflow

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
)

// Workflows live in the REPOSITORY they act on, next to the code they know
// about — the same arrangement as `.github/workflows/`, and for the same
// reasons: they are reviewed in the pull request that changes them, they travel
// with a clone, and a fork gets them for free.
//
// So a source is a directory holding workflow files, and the platform loads
// from several: its own, plus one per repository it has been pointed at.
//
// RepoWorkflowDir is where they live inside a repository: `.wfx/workflows`,
// named after the CLI that reads them.
//
// Not `.github/workflows`, because a repository may reasonably have both and a
// file written for one runner is not valid for the other — sharing the
// directory would mean each runner trying to parse the other's files.
const RepoWorkflowDir = ".wfx/workflows"

// RepoTaskDir holds reusable tasks belonging to a repository.
const RepoTaskDir = ".wfx/tasks"

// Source is one place workflows are loaded from.
type Source struct {
	// Name identifies the source. The platform's own is "local"; a repository's
	// is whatever it was imported as.
	Name string `json:"name"`
	// Dir is the directory holding the workflow files.
	Dir string `json:"dir"`
	// Repo is the working copy the workflows came with, and the default
	// `repo_path` for a run of one of them. Empty for the platform's own.
	Repo string `json:"repo,omitempty"`
	// URL is where the repository came from, when it was cloned.
	URL string `json:"url,omitempty"`
}

// RepoSource describes a checked-out repository as a workflow source. It does
// NOT verify the directory exists: a repository with no `.wfx/workflows`
// is a normal thing, and contributes nothing rather than failing.
func RepoSource(name, repoDir, url string) Source {
	return Source{
		Name: name,
		Dir:  filepath.Join(repoDir, filepath.FromSlash(RepoWorkflowDir)),
		Repo: repoDir,
		URL:  url,
	}
}

// LoadSources loads every source in order and merges them.
//
// Later sources do NOT silently overwrite earlier ones. Two repositories can
// each have a `checks` workflow, and picking one at random is how a run does
// something nobody asked for — so a collision is reported and both names stay
// reachable as `<source>/<name>`, which is also how the fully-qualified name is
// always spelled.
func LoadSources(sources []Source, cat Catalog, opts ...LoadOption) (map[string]*Definition, []Skip, error) {
	out := map[string]*Definition{}
	var skips []Skip
	seen := map[string]string{} // workflow name → the source that claimed it

	// Parse every source before expanding any, so a workflow can `use:` one
	// from another source (load_calls.go).
	groups := make([]*loadGroup, len(sources))
	for i, src := range sources {
		g := &loadGroup{name: src.Name}
		groups[i] = g
		tasksDir := ""
		if src.Repo != "" {
			tasksDir = filepath.Join(src.Repo, filepath.FromSlash(RepoTaskDir))
		}
		if g.tasks, g.err = LoadTasks(tasksDir); g.err != nil {
			continue
		}
		g.defs, g.broken, g.err = parseDirIfPresent(src.Dir)
	}
	newExpander(groups, newLoadOptions(opts)).run(cat)

	for i, src := range sources {
		g := groups[i]
		if g.err != nil {
			// One repository's broken file must not stop the platform booting,
			// or importing a repository becomes a way to take the server down.
			// It is recorded as a skip, which `wfx doctor` shows.
			skips = append(skips, Skip{Source: src.Name, Location: src.Dir, Reason: g.err.Error()})
			continue
		}
		// One workflow that did not load costs only itself (load_calls.go).
		for _, b := range g.broken {
			log.Printf("workflow: source %s: skipped %s: %s", src.Name, b.Path, b.Reason)
			skips = append(skips, Skip{Source: src.Name, Workflow: b.Name, Location: b.Path, Reason: b.Reason})
		}
		defs := map[string]*Definition{}
		var order []string
		for _, d := range g.defs {
			if first, dup := defs[d.Name]; dup {
				// Two files in one project claiming one name: the first (in
				// directory order) keeps it, and the other is reported, never
				// silently overwritten.
				reason := fmt.Sprintf("duplicate workflow name %q in source %q: %s is used and this file is ignored", d.Name, src.Name, first.Path)
				log.Printf("workflow: %s", reason)
				skips = append(skips, Skip{Source: src.Name, Workflow: d.Name, Location: d.Path, Reason: reason})
				continue
			}
			defs[d.Name] = d
			order = append(order, d.Name)
		}
		for _, name := range order {
			d := defs[name]
			d.Source = src.Name
			d.RepoDir = src.Repo
			qualified := src.Name + "/" + name
			out[qualified] = d
			if owner, clash := seen[name]; clash {
				skips = append(skips, Skip{
					Source:   src.Name,
					Location: d.Path,
					Reason: fmt.Sprintf("the name %q is already used by source %q; "+
						"both are reachable as %q and %q", name, owner, owner+"/"+name, qualified),
				})
				// The short name stays with whoever had it first, so importing
				// a repository never changes what an existing name does.
				continue
			}
			seen[name] = src.Name
			out[name] = d
		}
	}
	return out, skips, nil
}

// LoadLocal loads the platform's own workflows directory the way every source
// is loaded: a workflow that does not parse or validate is skipped (and
// returned in the skips) instead of failing the whole directory. Boot uses it,
// so one bad file never stops the server. LoadDir stays strict for `save`.
func LoadLocal(dir string, cat Catalog, opts ...LoadOption) (map[string]*Definition, []Skip, error) {
	return LoadSources([]Source{{Name: "local", Dir: dir}}, cat, opts...)
}

// Skip is a source, or a workflow within one, that did not load.
type Skip struct {
	Source string `json:"source"`
	// Workflow is the name of the workflow that did not load, when the file
	// got far enough to have one. Empty for a whole source that failed.
	Workflow string `json:"workflow,omitempty"`
	Location string `json:"location"`
	Reason   string `json:"reason"`
}

// parseDirIfPresent treats an absent directory as empty. A repository without
// workflows is the normal case, not an error.
func parseDirIfPresent(dir string) ([]*Definition, []Broken, error) {
	if dir == "" {
		return nil, nil, nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil, nil
	}
	return parseDir(dir)
}

// SortedSources returns sources in a stable order for display.
func SortedSources(s []Source) []Source {
	out := append([]Source(nil), s...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

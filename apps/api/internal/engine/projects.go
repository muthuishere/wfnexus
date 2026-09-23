package engine

import (
	"context"
	"fmt"
	"sort"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A PROJECT is a repository the platform knows about, and it owns the
// workflows in its `.wfx/workflows/` and every run of them:
//
//	project → workflow → runs
//
// The same hierarchy GitHub Actions has, for the same reason: a flat global run
// list stops meaning anything the moment there is more than one repository, and
// "which repo was that against" is the first question anyone asks about a run.
//
// `local` is the platform's own workflows directory. It always exists, cannot
// be removed, and is where everything ran before projects existed.

// Project is a repository plus what is currently true about it.
type Project struct {
	Name string `json:"name"`
	// Dir is the directory holding its workflow files.
	Dir string `json:"dir"`
	// Repo is the working copy; empty for `local`.
	Repo string `json:"repo,omitempty"`
	// URL is where it was cloned from, when it was cloned.
	URL string `json:"url,omitempty"`
	// Local marks the platform's own directory, which is not removable.
	Local bool `json:"local"`

	Workflows []string `json:"workflows"`
	Runs      int      `json:"runs"`
	// LastRun is the newest run's status, so a dashboard can say whether the
	// last thing that happened here worked.
	LastRun   string `json:"lastRun,omitempty"`
	LastRunAt string `json:"lastRunAt,omitempty"`
	// Problems are the workflows in this project that did not load.
	Problems []workflow.Skip `json:"problems,omitempty"`
}

// Projects lists every project with its workflows and run activity.
func (e *Engine) Projects(ctx context.Context) ([]Project, error) {
	byName := map[string]*Project{}
	var order []string
	for _, src := range e.Sources() {
		// The templates directory is a SOURCE of definitions but not a project:
		// nothing there is runnable, so listing it beside real repositories
		// offers a row whose every action is a dead end.
		if src.Name == templatesSource {
			continue
		}
		p := &Project{
			Name: src.Name, Dir: src.Dir, Repo: src.Repo, URL: src.URL,
			Local: src.Name == "local",
			// Never nil. A nil slice marshals to `null`, and the dashboard
			// does `workflows.length` — so a project with NO workflows took
			// the landing page down while every populated one rendered.
			Workflows: []string{},
		}
		byName[src.Name] = p
		order = append(order, src.Name)
	}

	// A workflow belongs to the project it was loaded from. Sorted() yields each
	// definition once, so a workflow registered under both its short and its
	// qualified name is not counted twice.
	for _, d := range workflow.Sorted(e.Definitions()) {
		src := d.Source
		if src == "" {
			src = "local"
		}
		if p, ok := byName[src]; ok {
			p.Workflows = append(p.Workflows, d.Name)
		}
	}
	for _, sk := range e.SourceSkips() {
		if p, ok := byName[sk.Source]; ok {
			p.Problems = append(p.Problems, sk)
		}
	}

	// Counted in SQL. Listing runs and counting them reported the listing's own
	// cap as the number of runs, so every busy project looked the same.
	activity, err := e.store.ProjectRunActivity(ctx)
	if err != nil {
		return nil, err
	}
	for name, a := range activity {
		p, ok := byName[name]
		if !ok {
			// A project that has been forgotten still has real runs, so it is
			// shown rather than letting them vanish from every view.
			p = &Project{Name: name, Workflows: []string{}}
			byName[name] = p
			order = append(order, name)
		}
		p.Runs = a.Runs
		p.LastRun = a.LastStatus
		p.LastRunAt = a.LastCreated.Format("2006-01-02T15:04:05Z07:00")
	}

	out := make([]Project, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	sort.Slice(out, func(i, j int) bool {
		// `local` first — it is where a new install starts — then by name.
		if out[i].Local != out[j].Local {
			return out[i].Local
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Project returns one project, or an error naming what does exist.
func (e *Engine) Project(ctx context.Context, name string) (*Project, error) {
	all, err := e.Projects(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("no project named %q", name)
}

// ProjectFor decides which project a run of this workflow belongs to.
//
// It is the workflow's own source, because a workflow travels with the
// repository it acts on. Anything loaded from the platform's own directory is
// `local`.
func (e *Engine) ProjectFor(name string) string {
	if def := e.Definitions()[name]; def != nil && def.Source != "" {
		return def.Source
	}
	return "local"
}

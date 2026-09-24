package engine

import (
	"context"
	"fmt"
	"sort"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// A TEMPLATE is a workflow that exists to be copied.
//
// It is not a second kind of file, and there is no template language. It is an
// ordinary workflow carrying `template:`, loaded, validated and dry-run by the
// same code as everything else — so a template that would not run is caught by
// the loader rather than by the first person who tries to use it. The only
// thing it refuses to do is run, because the point is the copy.
//
// That also means a project ships its own: a repository's `.wfx/workflows/`
// can hold templates for the shape of work that repository does, and they
// appear in the gallery beside the platform's.

// Template is one entry of the gallery.
type Template struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
	// Fill is what the author must supply to make the copy theirs.
	Fill []string `json:"fill,omitempty"`
	// Source is the project it came from — "local" for the platform's own.
	Source string `json:"source,omitempty"`
	// Phases names the steps in order, which is the shape someone is choosing
	// between when they pick one.
	Phases []TemplatePhase `json:"phases"`
	// NeedsSkills is true when at least one step has no skills yet. It is the
	// normal state of a template and the reason the gallery says so: the shape
	// is given, the expertise is yours.
	NeedsSkills bool `json:"needsSkills"`
	// Files are everything sitting beside the definition — a run.js, a
	// fixture, a README. Shown in the gallery because they are part of what
	// you are about to copy, and because a template whose step says
	// `run: node report.js` is unreadable until you can see report.js is
	// there.
	Files []workflow.FileInfo `json:"files,omitempty"`
	// Path is where the definition lives, so a listing can be traced to disk.
	Path string `json:"path,omitempty"`
}

// TemplatePhase is one step, as the gallery shows it.
type TemplatePhase struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	Kind        string   `json:"kind"`
	Description string   `json:"description,omitempty"`
	Skills      []string `json:"skills,omitempty"`
	Tools       []string `json:"tools,omitempty"`
	// Approval marks a step that stops for a human.
	Approval bool `json:"approval,omitempty"`
}

// Templates lists every template the platform and its projects offer.
func (e *Engine) Templates() []Template {
	var out []Template
	for _, d := range workflow.Sorted(e.Definitions()) {
		if !d.Template.Is {
			continue
		}
		out = append(out, describeTemplate(d))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Template returns one, or an error naming what does exist.
func (e *Engine) Template(name string) (*workflow.Definition, error) {
	def := e.Definitions()[name]
	if def == nil {
		return nil, fmt.Errorf("no template named %q", name)
	}
	if !def.Template.Is {
		return nil, fmt.Errorf("%q is a workflow, not a template", name)
	}
	return def, nil
}

func describeTemplate(d *workflow.Definition) Template {
	t := Template{
		Name: d.Name, Title: d.Template.Title, Summary: d.Template.Summary,
		Description: d.Description, Fill: d.Template.Fill, Source: d.Source,
		Files: workflow.Infos(d.Files), Path: d.Path,
	}
	if t.Title == "" {
		t.Title = d.Name
	}
	if t.Summary == "" {
		t.Summary = firstLine(d.Description)
	}
	for i := range d.Steps {
		s := &d.Steps[i]
		p := TemplatePhase{
			ID: s.ID, Name: s.Name, Kind: stepKind(s), Description: s.Description,
			Skills: s.Skills, Tools: s.Tools, Approval: s.RequiresApproval,
		}
		if isAgent(s) && len(s.Skills) == 0 {
			t.NeedsSkills = true
		}
		t.Phases = append(t.Phases, p)
	}
	return t
}

// CopyTemplate turns a template into a workflow of the author's own, saved
// under `as`. The copy is validated before it is written, so a name collision
// or a skill the machine does not have is refused here rather than at the next
// reload.
func (e *Engine) CopyTemplate(ctx context.Context, name, as string) (*workflow.Definition, error) {
	if _, err := e.Template(name); err != nil {
		return nil, err
	}
	return e.CopyWorkflow(ctx, name, as)
}

// CopyWorkflow copies ANY loaded workflow into the platform's own directory.
//
// REUSE IS NOT A TEMPLATE-ONLY PRIVILEGE. The gallery came first, so copying
// was template-only plumbing — but the commonest thing anyone actually wants
// to reuse is an ordinary workflow already working in another repository, and
// making them mark it `template:` first is a step that exists only because of
// how this was built.
//
// EVERYTHING comes along. The definition and every file beside it: the run.js
// the step invokes, the fixture it reads, the README that explains it. Nothing
// here judges which of those matter, because the platform cannot know and the
// person copying can — and a copy missing one file is a workflow that loads
// and then fails on its first run, which is the worst place to find out.
func (e *Engine) CopyWorkflow(ctx context.Context, name, as string) (*workflow.Definition, error) {
	src := e.Definitions()[name]
	if src == nil {
		return nil, fmt.Errorf("no workflow named %q", name)
	}
	if as == "" {
		return nil, fmt.Errorf("the copy needs a name of its own")
	}
	if existing := e.Definitions()[as]; existing != nil {
		return nil, fmt.Errorf("%q already exists — pick another name", as)
	}
	copy := *src
	copy.Name = as
	// The copy is a workflow, not a template: it is meant to be run, and
	// leaving the flag on would make it refuse for no reason anyone could see.
	copy.Template = workflow.TemplateInfo{}
	// It belongs to whoever copied it now, not to where it came from. Leaving
	// the source on would file the copy under someone else's project, and
	// leaving RepoDir on would point its runs at a checkout it does not own.
	copy.Source = ""
	copy.RepoDir = ""
	copy.Path = ""
	// Deep, so the copy and the original never share bytes.
	copy.Files = workflow.Clone(src.Files)
	return &copy, nil
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

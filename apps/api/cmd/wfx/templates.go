package main

import (
	"fmt"
	"net/url"
	"strings"
)

// `wfx templates` and `wfx new` — the same gallery the UI shows.

type tmplPhase struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Skills   []string `json:"skills"`
	Approval bool     `json:"approval"`
}

type tmpl struct {
	Name        string      `json:"name"`
	Title       string      `json:"title"`
	Summary     string      `json:"summary"`
	Fill        []string    `json:"fill"`
	Phases      []tmplPhase `json:"phases"`
	NeedsSkills bool        `json:"needsSkills"`
}

func templates(args []string) error {
	if len(args) > 0 {
		return showTemplate(args[0])
	}
	var list []tmpl
	if err := call("GET", "/api/templates", nil, &list); err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no templates — a workflow becomes one by carrying `template:` in the templates directory")
		return nil
	}
	for _, t := range list {
		fmt.Printf("%-14s %s\n", t.Name, t.Title)
		fmt.Printf("               %s\n", wrap(t.Summary, 70, "               "))
		var ids []string
		for _, p := range t.Phases {
			id := p.ID
			if p.Approval {
				id += " (human gate)"
			}
			ids = append(ids, id)
		}
		fmt.Printf("               %s\n", strings.Join(ids, " → "))
		if t.NeedsSkills {
			fmt.Printf("               ships with no skills — that part is yours\n")
		}
		fmt.Println()
	}
	fmt.Println("copy one:  wfx new <template> --as <name>")
	return nil
}

func showTemplate(name string) error {
	var t tmpl
	var list []tmpl
	if err := call("GET", "/api/templates", nil, &list); err != nil {
		return err
	}
	for _, c := range list {
		if c.Name == name {
			t = c
		}
	}
	if t.Name == "" {
		return fmt.Errorf("no template named %q", name)
	}
	fmt.Printf("%s — %s\n\n%s\n\n", t.Name, t.Title, t.Summary)
	for i, p := range t.Phases {
		gate := ""
		if p.Approval {
			gate = "   [stops for a human]"
		}
		skills := "no skills yet"
		if len(p.Skills) > 0 {
			skills = strings.Join(p.Skills, ", ")
		}
		fmt.Printf("  %d. %-12s %-28s %s%s\n", i+1, p.ID, p.Name, skills, gate)
	}
	if len(t.Fill) > 0 {
		fmt.Println("\nyours to supply:")
		for _, f := range t.Fill {
			fmt.Printf("  · %s\n", wrap(f, 70, "    "))
		}
	}
	fmt.Printf("\ncopy it:  wfx new %s --as my-%s\n", t.Name, t.Name)
	return nil
}

// newFromTemplate writes the author's own copy.
func newFromTemplate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("which template? try `wfx templates`")
	}
	name := args[0]
	as := flagOf(args, "--as", name)
	var out struct{ Name, Path string }
	if err := call("POST", "/api/templates/"+url.PathEscape(name)+"/copy", map[string]any{"as": as}, &out); err != nil {
		return err
	}
	fmt.Printf("%s → %s\n", out.Name, out.Path)
	fmt.Printf("it is yours now: add the skills each phase needs, then `wfx dryrun %s`\n", out.Name)
	return nil
}

// wrap re-flows a summary under a hanging indent.
func wrap(s string, width int, indent string) string {
	var out, line []string
	n := 0
	for _, w := range strings.Fields(s) {
		if n+len(w) > width && len(line) > 0 {
			out = append(out, strings.Join(line, " "))
			line, n = nil, 0
		}
		line = append(line, w)
		n += len(w) + 1
	}
	if len(line) > 0 {
		out = append(out, strings.Join(line, " "))
	}
	return strings.Join(out, "\n"+indent)
}

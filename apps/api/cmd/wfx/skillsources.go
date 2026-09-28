package main

import (
	"fmt"
	"net/url"
	"strings"
)

// skillsCmd manages skill sources: git repositories of skills imported into
// the server's registry at a branch, tag or commit.
//
//	wfx skills import <git-url> [--ref r | --branch b] [--path skills/] [--name n]
//	wfx skills sources
//	wfx skills ref <source> <ref>        (alias: branch)
//	wfx skills refresh <source>          (alias: sync)
//	wfx skills remove <source>
//	wfx skills show <source>
func skillsCmd(args []string) error {
	const use = "usage: wfx skills import <git-url> [--ref <branch|tag|sha>] [--path skills/] [--name n]\n" +
		"       wfx skills sources | ref <source> <ref> | refresh <source> | remove <source> | show <source>"
	if len(args) == 0 {
		return skillSourcesList()
	}
	switch args[0] {
	case "import", "add":
		if len(args) < 2 || strings.HasPrefix(args[1], "--") {
			return fmt.Errorf("%s", use)
		}
		ref := flagOf(args, "--ref", "")
		if ref == "" {
			ref = flagOf(args, "--branch", "")
		}
		body := map[string]any{"url": args[1], "ref": ref, "path": flagOf(args, "--path", "skills"),
			"name": flagOf(args, "--name", "")}
		var rep skillReport
		if err := call("POST", "/api/skill-sources", body, &rep); err != nil {
			return err
		}
		printSkillReport(rep)
		return nil
	case "sources", "list", "ls":
		return skillSourcesList()
	case "ref", "branch", "checkout":
		if len(args) < 3 {
			return fmt.Errorf("usage: wfx skills ref <source> <branch|tag|sha>")
		}
		var rep skillReport
		if err := call("PUT", "/api/skill-sources/"+url.PathEscape(args[1])+"/ref", map[string]any{"ref": args[2]}, &rep); err != nil {
			return err
		}
		printSkillReport(rep)
		return nil
	case "refresh", "sync", "pull":
		if len(args) < 2 {
			return fmt.Errorf("usage: wfx skills refresh <source>")
		}
		var rep skillReport
		if err := call("POST", "/api/skill-sources/"+url.PathEscape(args[1])+"/refresh", nil, &rep); err != nil {
			return err
		}
		printSkillReport(rep)
		return nil
	case "remove", "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: wfx skills remove <source>")
		}
		var rep skillReport
		if err := call("DELETE", "/api/skill-sources/"+url.PathEscape(args[1]), nil, &rep); err != nil {
			return err
		}
		printSkillReport(rep)
		return nil
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: wfx skills show <source>")
		}
		var files []struct {
			Name, Description, Path string
			Loaded                  bool
		}
		if err := call("GET", "/api/skill-sources/"+url.PathEscape(args[1])+"/skills", nil, &files); err != nil {
			return err
		}
		for _, f := range files {
			mark := " "
			if !f.Loaded {
				mark = "✗" // shadowed by an earlier root
			}
			fmt.Printf("%s %-24s %s\n", mark, f.Name, firstLine(f.Description))
		}
		return nil
	}
	return fmt.Errorf("%s", use)
}

type skillSourceView struct {
	Name, URL, Ref, RefKind, Path, Commit, SyncedAt string
	Skills                                          []string
	Count                                           int
	Clashes                                         []struct{ Skill, OwnedBy string }
}

type skillReport struct {
	Source                  skillSourceView
	Added, Removed, Updated []string
	Pinned                  bool
	Message                 string
}

func skillSourcesList() error {
	var list []skillSourceView
	if err := call("GET", "/api/skill-sources", nil, &list); err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Println("no skill sources — `wfx skills import <git-url> --ref main` adds one")
		return nil
	}
	fmt.Printf("%-18s %-22s %-13s %-6s %s\n", "SOURCE", "REF", "COMMIT", "SKILLS", "URL (path)")
	for _, s := range list {
		fmt.Printf("%-18s %-22s %-13s %-6d %s (%s)\n", s.Name, s.RefKind+":"+s.Ref, shortSHA(s.Commit), s.Count, s.URL, s.Path)
		if len(s.Skills) > 0 {
			fmt.Printf("  %s\n", strings.Join(s.Skills, ", "))
		}
		for _, c := range s.Clashes {
			fmt.Printf("  ✗ %s: already provided by %s (theirs is kept)\n", c.Skill, c.OwnedBy)
		}
	}
	return nil
}

func printSkillReport(r skillReport) {
	fmt.Println(r.Message)
	for _, n := range r.Added {
		fmt.Printf("  + %s\n", n)
	}
	for _, n := range r.Removed {
		fmt.Printf("  - %s\n", n)
	}
	for _, n := range r.Updated {
		fmt.Printf("  ~ %s\n", n)
	}
	for _, c := range r.Source.Clashes {
		fmt.Printf("  ✗ %s: already provided by %s (theirs is kept)\n", c.Skill, c.OwnedBy)
	}
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

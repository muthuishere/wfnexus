package main

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// `wfx memory` — what previous runs of a workflow left behind (engine/memory.go).
//
//	wfx memory ls --workflow triage            every entry, newest first
//	wfx memory show --workflow triage          the section the next run's steps will read, then every entry
//	wfx memory forget --workflow triage        delete all of it (or: wfx memory forget <id>)
//
// `--project p` narrows to one project; without it every project's memory of
// that workflow is shown (and forgotten).

type memoryEntry struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Workflow  string    `json:"workflow"`
	RunID     string    `json:"runId"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status"`
	StepID    string    `json:"stepId"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

type memoryList struct {
	Workflow string        `json:"workflow"`
	Entries  []memoryEntry `json:"entries"`
	Preview  string        `json:"preview"`
	Enabled  *bool         `json:"enabled"`
}

const memoryUsage = `usage: wfx memory <ls|show|forget> --workflow NAME [--project P] | wfx memory forget ID`

func memoryCmd(args []string) error {
	wf := flagOf(args, "--workflow", "")
	project := flagOf(args, "--project", "")
	rest := withoutFlag(withoutFlag(args, "--workflow"), "--project")
	verb := "ls"
	if len(rest) > 0 {
		verb, rest = rest[0], rest[1:]
	}
	switch verb {
	case "ls", "list", "show":
		if wf == "" {
			return fmt.Errorf("which workflow? --workflow NAME\n%s", memoryUsage)
		}
		return memoryShow(wf, project, verb == "show")
	case "forget", "rm":
		if len(rest) == 1 && wf == "" {
			if err := call("DELETE", "/api/memory/"+url.PathEscape(rest[0]), nil, nil); err != nil {
				return err
			}
			fmt.Printf("forgot %s\n", rest[0])
			return nil
		}
		if wf == "" {
			return fmt.Errorf("forget what? --workflow NAME, or an entry id\n%s", memoryUsage)
		}
		var out struct {
			Deleted int64 `json:"deleted"`
		}
		if err := call("DELETE", memoryPath(wf, project), nil, &out); err != nil {
			return err
		}
		fmt.Printf("forgot %d memory entries of %s\n", out.Deleted, wf)
		return nil
	}
	return fmt.Errorf("%s", memoryUsage)
}

func memoryPath(wf, project string) string {
	p := "/api/workflows/" + url.PathEscape(wf) + "/memory"
	if project != "" {
		p += "?project=" + url.QueryEscape(project)
	}
	return p
}

func memoryShow(wf, project string, full bool) error {
	var out memoryList
	if err := call("GET", memoryPath(wf, project), nil, &out); err != nil {
		return err
	}
	if out.Enabled != nil && !*out.Enabled {
		fmt.Printf("memory is off for %s (memory: false)\n", wf)
	}
	if full {
		if out.Preview == "" {
			fmt.Println("(the next run would load nothing)")
		} else {
			fmt.Println(out.Preview)
		}
		fmt.Println()
	}
	if len(out.Entries) == 0 {
		fmt.Printf("nothing remembered for %s yet\n", wf)
		return nil
	}
	fmt.Printf("%-36s %-7s %-10s %-10s %-17s %s\n", "ID", "KIND", "STATUS", "PROJECT", "WHEN", "WHAT")
	for _, e := range out.Entries {
		first, _, _ := strings.Cut(e.Body, "\n")
		if e.StepID != "" {
			first = "[" + e.StepID + "] " + first
		}
		if len(first) > 80 {
			first = first[:80] + "…"
		}
		fmt.Printf("%-36s %-7s %-10s %-10s %-17s %s\n", e.ID, e.Kind, or(e.Status, "-"), or(e.Project, "-"),
			e.CreatedAt.Local().Format("2006-01-02 15:04"), first)
		if full && strings.Contains(e.Body, "\n") {
			for _, l := range strings.Split(e.Body, "\n")[1:] {
				fmt.Printf("    %s\n", l)
			}
		}
	}
	return nil
}

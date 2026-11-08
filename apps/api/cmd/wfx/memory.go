package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

// `wfx memory` — a workflow's toolnexus memory home (engine/memory.go):
// MEMORY.md (the index every agent step loads), memory/<slug>.md notes (read
// on demand), memory/log.md (what each dream changed).
//
//	wfx memory show W                    index, notes, dream log
//	wfx memory read W note:<slug>        one file (index | dream | note:<slug>)
//	wfx memory write W TARGET TEXT|-     replace one file (- reads stdin)
//	wfx memory rm W TARGET               delete one file
//	wfx memory forget W                  delete the whole home
//	wfx memory dream W                   run one dream pass now
//
// `--project P` picks the project (default local).

const memoryUsage = `usage: wfx memory <show|read|write|rm|forget|dream> WORKFLOW [TARGET] [TEXT|-] [--project P]
  TARGET: index | dream | note:<slug>`

type memoryView struct {
	Dir     string `json:"dir"`
	Enabled bool   `json:"enabled"`
	Index   string `json:"index"`
	Log     string `json:"log"`
	Notes   []struct {
		Slug  string `json:"slug"`
		Bytes int64  `json:"bytes"`
	} `json:"notes"`
}

func memoryCmd(args []string) error {
	project := flagOf(args, "--project", "")
	rest := withoutFlag(withoutFlag(args, "--workflow"), "--project")
	if wf := flagOf(args, "--workflow", ""); wf != "" && len(rest) > 0 {
		rest = append([]string{rest[0], wf}, rest[1:]...)
	}
	if len(rest) < 2 {
		return fmt.Errorf("%s", memoryUsage)
	}
	verb, wf, rest := rest[0], rest[1], rest[2:]
	q := url.Values{}
	if project != "" {
		q.Set("project", project)
	}
	base := "/api/workflows/" + url.PathEscape(wf) + "/memory"
	file := func() string {
		q.Set("target", rest[0])
		return base + "/file?" + q.Encode()
	}
	switch verb {
	case "show", "ls":
		var v memoryView
		if err := call("GET", base+"?"+q.Encode(), nil, &v); err != nil {
			return err
		}
		if !v.Enabled {
			fmt.Println("memory is off for this workflow (memory: false)")
		}
		fmt.Printf("home  %s\n\n## MEMORY.md\n%s\n\n## notes\n", v.Dir, or(strings.TrimSpace(v.Index), "(empty)"))
		if len(v.Notes) == 0 {
			fmt.Println("(none)")
		}
		for _, n := range v.Notes {
			fmt.Printf("  [[%s]]  %d bytes\n", n.Slug, n.Bytes)
		}
		fmt.Printf("\n## dream log\n%s\n", or(strings.TrimSpace(v.Log), "(no dreams yet)"))
		return nil
	case "read", "cat":
		if len(rest) < 1 {
			return fmt.Errorf("%s", memoryUsage)
		}
		var out struct {
			Body string `json:"body"`
		}
		if err := call("GET", file(), nil, &out); err != nil {
			return err
		}
		fmt.Print(out.Body)
		return nil
	case "write":
		if len(rest) < 2 {
			return fmt.Errorf("%s", memoryUsage)
		}
		text := strings.Join(rest[1:], " ")
		if text == "-" {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			text = string(b)
		}
		if err := call("PUT", file(), map[string]string{"body": text}, nil); err != nil {
			return err
		}
		fmt.Printf("wrote %s\n", rest[0])
		return nil
	case "rm":
		if len(rest) < 1 {
			return fmt.Errorf("%s", memoryUsage)
		}
		if err := call("DELETE", file(), nil, nil); err != nil {
			return err
		}
		fmt.Printf("removed %s\n", rest[0])
		return nil
	case "forget":
		if err := call("DELETE", base+"?"+q.Encode(), nil, nil); err != nil {
			return err
		}
		fmt.Printf("forgot all memory of %s\n", wf)
		return nil
	case "dream":
		var out map[string]any
		if err := call("POST", base+"/dream?"+q.Encode(), nil, &out); err != nil {
			return err
		}
		fmt.Printf("dreamt over %v runs; changed=%v\n%v\n", out["runs"], out["changed"], out["reply"])
		return nil
	}
	return fmt.Errorf("%s", memoryUsage)
}

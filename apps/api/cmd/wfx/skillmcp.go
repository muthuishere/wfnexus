package main

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// `wfx skill` and `wfx mcp` — the same noun-verb form as `wfx tool`, against
// the CURRENT profile's server (or --context <name>). The server applies the
// change, reloads its catalog live, and versions it in its carry state, so
// `wfx config log` lists it and `wfx config rollback <n>` undoes it.
//
// `wfx skills …` (plural) is the older skill-source surface and keeps working.

// skillCmd is `wfx skill add <name|git-url|path> | rm <name> | ls`.
func skillCmd(args []string) error {
	const use = "usage: wfx skill add <name|git-url|path> [--ref r] [--path skills/] [--context c]\n" +
		"       wfx skill rm <name>   wfx skill ls"
	pos := carryPositional(args)
	sub := ""
	if len(pos) > 0 {
		sub = pos[0]
	}
	t, err := carryTarget(args, "--to")
	if err != nil {
		return err
	}
	switch sub {
	case "add", "install":
		if len(pos) < 2 {
			return fmt.Errorf("%s", use)
		}
		for _, spec := range pos[1:] {
			if err := skillAdd(t, spec, args); err != nil {
				return err
			}
		}
		return nil
	case "rm", "remove", "uninstall":
		if len(pos) < 2 {
			return fmt.Errorf("%s", use)
		}
		for _, name := range pos[1:] {
			var out map[string]any
			err := callWith(t, "DELETE", "/api/skills/installed/"+url.PathEscape(name), nil, &out)
			if err != nil {
				// Not an installed skill: perhaps a git source of that name.
				var rep skillReport
				if serr := callWith(t, "DELETE", "/api/skill-sources/"+url.PathEscape(name), nil, &rep); serr != nil {
					return fmt.Errorf("%s is neither an installed skill nor a skill source on %s", name, t.name)
				}
				printSkillReport(rep)
				continue
			}
			fmt.Printf("removed skill %s from %s%s\n", name, t.name, versionNote(out))
		}
		return nil
	case "", "ls", "list":
		var out struct {
			Installed []string          `json:"installed"`
			Sources   []skillSourceView `json:"sources"`
		}
		if err := callWith(t, "GET", "/api/skills/installed", nil, &out); err != nil {
			return err
		}
		return printJSONOr(args, out, func() {
			fmt.Printf("%s — skills\n", t.name)
			if len(out.Installed) == 0 && len(out.Sources) == 0 {
				fmt.Println("  none added — `wfx skill add <name|git-url|path>`")
			}
			for _, n := range out.Installed {
				fmt.Printf("  %-24s installed\n", n)
			}
			for _, s := range out.Sources {
				fmt.Printf("  %-24s git %s @ %s (%d skills)\n", s.Name, s.URL, s.Ref, s.Count)
			}
		})
	}
	return fmt.Errorf("%s", use)
}

func isGitURL(s string) bool {
	return strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") ||
		strings.HasPrefix(s, "git@") || strings.HasPrefix(s, "ssh://") || strings.HasSuffix(s, ".git")
}

func skillAdd(t resolved, spec string, args []string) error {
	if isGitURL(spec) {
		ref := flagOf(args, "--ref", flagOf(args, "--branch", ""))
		body := map[string]any{"url": spec, "ref": ref, "path": flagOf(args, "--path", "skills"), "name": flagOf(args, "--name", "")}
		var rep skillReport
		if err := callWith(t, "POST", "/api/skill-sources", body, &rep); err != nil {
			return err
		}
		printSkillReport(rep)
		return nil
	}
	body := map[string]any{"name": spec}
	if st, err := os.Stat(spec); err == nil && st.IsDir() {
		files, err := readSkillDir(spec)
		if err != nil {
			return err
		}
		// The registry keys a skill by its frontmatter name; the folder name is
		// only the fallback for a SKILL.md that names nothing.
		name := frontmatterName(files["SKILL.md"])
		if name == "" {
			name = strings.ToLower(filepath.Base(filepath.Clean(spec)))
		}
		body = map[string]any{"name": name, "files": files}
		spec = name
	}
	var out map[string]any
	if err := callWith(t, "POST", "/api/skills/installed", body, &out); err != nil {
		return err
	}
	fmt.Printf("added skill %s on %s — live now%s\n", spec, t.name, versionNote(out))
	return nil
}

func frontmatterName(md []byte) string {
	s := string(md)
	if !strings.HasPrefix(s, "---") {
		return ""
	}
	for _, line := range strings.Split(s, "\n")[1:] {
		line = strings.TrimSpace(line)
		if line == "---" {
			break
		}
		if v, ok := strings.CutPrefix(line, "name:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// readSkillDir reads a local skill directory to upload. Hidden files and
// directories (.git, .DS_Store) are left out; SKILL.md must be there.
func readSkillDir(dir string) (map[string][]byte, error) {
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("%s has no SKILL.md — a skill is a directory with SKILL.md at its top", dir)
	}
	files := map[string][]byte{}
	var total int
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && p != dir {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		total += len(b)
		if total > 16<<20 {
			return fmt.Errorf("%s is over 16 MB — a skill that large belongs in a git source", dir)
		}
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	return files, err
}

// versionNote says whether carry recorded the change, and as which version.
func versionNote(out map[string]any) string {
	if out["versioned"] == true {
		return fmt.Sprintf(" (carry version %v — `wfx config rollback` undoes it)", out["version"])
	}
	if r, ok := out["reason"].(string); ok && r != "" {
		return " (not versioned: " + r + ")"
	}
	return ""
}

// mcpCmd is `wfx mcp add <name> [--set k=v]… [--env NAME]… | rm <name> | ls`.
//
//	--set command="npx -y @modelcontextprotocol/server-github"   local server
//	--set url=https://mcp.example.com/mcp  [--set type=remote]   remote server
//	--set env.LOG_LEVEL=debug   --set header.X-Team=core   --set description=…
//	--env GITHUB_TOKEN          an env-store key, injected when a step starts
//
// A value that looks like a credential is refused on both sides; credentials
// go in the env store (`wfx env set NAME`) and are named here.
func mcpCmd(args []string) error {
	const use = "usage: wfx mcp add <name> --set command=\"npx -y pkg\" | --set url=https://… [--set k=v]… [--env NAME]…\n" +
		"       wfx mcp rm <name>   wfx mcp ls"
	pos := mcpPositional(args)
	sub := ""
	if len(pos) > 0 {
		sub = pos[0]
	}
	t, err := carryTarget(args, "--to")
	if err != nil {
		return err
	}
	switch sub {
	case "add", "set":
		if len(pos) < 2 {
			return fmt.Errorf("%s", use)
		}
		m, err := mcpFromFlags(args)
		if err != nil {
			return err
		}
		var out map[string]any
		if err := callWith(t, "PUT", "/api/mcp/"+url.PathEscape(pos[1]), m, &out); err != nil {
			return err
		}
		fmt.Printf("added mcp server %s on %s — live now%s\n", pos[1], t.name, versionNote(out))
		if envs, _ := m["envFrom"].([]string); len(envs) > 0 {
			fmt.Printf("  needs from the env store: %s (`wfx env ls` shows which are set)\n", strings.Join(envs, ", "))
		}
		return nil
	case "rm", "remove":
		if len(pos) < 2 {
			return fmt.Errorf("%s", use)
		}
		var out map[string]any
		if err := callWith(t, "DELETE", "/api/mcp/"+url.PathEscape(pos[1]), nil, &out); err != nil {
			return err
		}
		fmt.Printf("removed mcp server %s from %s%s\n", pos[1], t.name, versionNote(out))
		return nil
	case "", "ls", "list":
		var out struct {
			Entries []struct {
				Name    string   `json:"name"`
				Type    string   `json:"type"`
				Command []string `json:"command"`
				URL     string   `json:"url"`
				EnvFrom []string `json:"envFrom"`
			} `json:"entries"`
		}
		if err := callWith(t, "GET", "/api/mcp", nil, &out); err != nil {
			return err
		}
		return printJSONOr(args, out, func() {
			if len(out.Entries) == 0 {
				fmt.Println("no mcp servers — `wfx mcp add <name> --set command=…`")
				return
			}
			for _, e := range out.Entries {
				what := e.URL
				if what == "" {
					what = strings.Join(e.Command, " ")
				}
				env := ""
				if len(e.EnvFrom) > 0 {
					env = "  env: " + strings.Join(e.EnvFrom, ",")
				}
				fmt.Printf("  %-20s %s%s\n", e.Name, what, env)
			}
		})
	}
	return fmt.Errorf("%s", use)
}

// mcpPositional is positional() with the mcp verbs' valued flags skipped.
func mcpPositional(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--set", "--env", "--context", "--to":
			i++
			continue
		}
		if strings.HasPrefix(args[i], "-") {
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// mcpFromFlags builds the entry. It never prints a value it was given.
func mcpFromFlags(args []string) (map[string]any, error) {
	m := map[string]any{}
	env := map[string]string{}
	headers := map[string]string{}
	for _, kv := range multiFlag(args, "--set") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--set %q: want key=value", k)
		}
		switch {
		case k == "command":
			m["command"] = strings.Fields(v)
		case k == "url":
			m["url"] = v
			if _, set := m["type"]; !set {
				m["type"] = "remote"
			}
		case k == "type" || k == "description":
			m[k] = v
		case strings.HasPrefix(k, "env."):
			env[strings.TrimPrefix(k, "env.")] = v
		case strings.HasPrefix(k, "header."):
			headers[strings.TrimPrefix(k, "header.")] = v
		default:
			return nil, fmt.Errorf("--set %s: one of command, url, type, description, env.NAME, header.NAME", k)
		}
	}
	if len(env) > 0 {
		m["environment"] = env
	}
	if len(headers) > 0 {
		m["headers"] = headers
	}
	envFrom := multiFlag(args, "--env")
	for _, n := range envFrom {
		if strings.Contains(n, "=") {
			return nil, fmt.Errorf("--env takes a NAME (an env-store key), not NAME=value — store the value with `wfx env set NAME`")
		}
	}
	if len(envFrom) > 0 {
		sort.Strings(envFrom)
		m["envFrom"] = envFrom
	}
	if _, ok := m["command"]; !ok {
		if _, ok := m["url"]; !ok {
			return nil, fmt.Errorf("an mcp server needs --set command=\"…\" (local) or --set url=… (remote)")
		}
	}
	return m, nil
}

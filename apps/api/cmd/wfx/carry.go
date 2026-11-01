package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// `wfx tool`, `wfx config` and `wfx sync`: tools, config files and env
// defaults on the server or a worker, through the SERVER's carry. The client
// holds a wfx token and nothing else — no S3 keys, no state key.
//
// Where:  the current context, or --context <name> (also spelled --to on the
//         verbs that write, --from on the ones that read).
// Which:  --on <worker> names a worker's state; --state <s> names any state;
//         neither means the server's own.

// carryTarget resolves the context a carry verb talks to.
func carryTarget(args []string, flags ...string) (resolved, error) {
	for _, f := range append([]string{"--context"}, flags...) {
		if v := flagOf(args, f, ""); v != "" {
			return resolveContext(v)
		}
	}
	return resolveContext("")
}

// carryState is --state, else --on (a worker's state is its name), else "".
func carryState(args []string) string {
	if v := flagOf(args, "--state", ""); v != "" {
		return v
	}
	return flagOf(args, "--on", "")
}

// carryPositional is positional() with carry's valued flags skipped.
func carryPositional(args []string) []string {
	valued := map[string]bool{"--context": true, "--to": true, "--from": true, "--on": true, "--state": true,
		"--channel": true, "--mode": true, "-o": true, "--limit": true, "--to-state": true, "--only": true, "--dir": true,
		"--ref": true, "--branch": true, "--path": true, "--name": true}
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if valued[a] {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") {
			continue
		}
		out = append(out, a)
	}
	return out
}

func stateQuery(state string) string {
	if state == "" {
		return ""
	}
	return "state=" + url.QueryEscape(state)
}

func withQuery(path string, kv ...string) string {
	var q []string
	for _, s := range kv {
		if s != "" {
			q = append(q, s)
		}
	}
	if len(q) == 0 {
		return path
	}
	return path + "?" + strings.Join(q, "&")
}

func printJSONOr(args []string, v any, human func()) error {
	if hasFlag(args, "--json") {
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	human()
	return nil
}

func where(t resolved, state string) string {
	if state == "" {
		state = "server"
	}
	return fmt.Sprintf("%s (%s)", t.name, state)
}

// ---- wfx tool --------------------------------------------------------------

func toolCmd(args []string) error {
	pos := carryPositional(args)
	sub := ""
	if len(pos) > 0 {
		sub = pos[0]
	}
	t, err := carryTarget(args, "--to")
	if err != nil {
		return err
	}
	state := carryState(args)
	switch sub {
	case "add":
		if len(pos) < 2 {
			return fmt.Errorf("usage: wfx tool add <name>[@version] [--on worker] [--channel c] [--context ctx]")
		}
		for _, spec := range pos[1:] {
			var out map[string]any
			if err := callWith(t, "POST", "/api/carry/tools", map[string]any{"state": state, "tool": spec, "channel": flagOf(args, "--channel", "")}, &out); err != nil {
				return err
			}
			fmt.Printf("added %s on %s — version %v; running machines pick it up on their next poll\n", spec, where(t, state), out["version"])
		}
		return nil
	case "rm", "remove":
		if len(pos) < 2 {
			return fmt.Errorf("usage: wfx tool rm <name> [--on worker]")
		}
		var out map[string]any
		if err := callWith(t, "DELETE", withQuery("/api/carry/tools/"+url.PathEscape(pos[1]), stateQuery(state)), nil, &out); err != nil {
			return err
		}
		fmt.Printf("removed %s from %s — version %v\n", pos[1], where(t, state), out["version"])
		return nil
	case "", "ls", "list":
		var out struct {
			Tools []struct {
				Name     string            `json:"name"`
				Versions []string          `json:"versions"`
				Channels map[string]string `json:"channels"`
			} `json:"tools"`
		}
		name := ""
		if len(pos) > 1 {
			name = "name=" + url.QueryEscape(pos[1])
		}
		if err := callWith(t, "GET", withQuery("/api/carry/tools", name), nil, &out); err != nil {
			return err
		}
		return printJSONOr(args, out, func() {
			if len(out.Tools) == 0 {
				fmt.Println("no tools published to this bucket — `carry tool publish` once, then `wfx tool add`")
				return
			}
			fmt.Printf("%-18s %-28s %s\n", "TOOL", "VERSIONS", "CHANNELS")
			for _, tl := range out.Tools {
				var ch []string
				for c, v := range tl.Channels {
					ch = append(ch, c+"="+v)
				}
				sort.Strings(ch)
				fmt.Printf("%-18s %-28s %s\n", tl.Name, strings.Join(tl.Versions, ","), strings.Join(ch, " "))
			}
		})
	default:
		return fmt.Errorf("unknown `wfx tool %s` — add | rm | ls", sub)
	}
}

// ---- wfx config ------------------------------------------------------------

func configCmd(args []string) error {
	pos := carryPositional(args)
	if len(pos) == 0 {
		pos = []string{"ls"}
	}
	sub := pos[0]
	state := carryState(args)
	switch sub {
	case "put", "push":
		// put <remote-path> <local-file>   (push is the same, read as "move it there")
		if len(pos) < 3 {
			return fmt.Errorf("usage: wfx config put <remote-abs-path> <local-file|-> [--on worker] [--to ctx] [--mode 0644]")
		}
		t, err := carryTarget(args, "--to")
		if err != nil {
			return err
		}
		var data []byte
		if pos[2] == "-" {
			data, err = readAllStdin()
		} else {
			data, err = os.ReadFile(pos[2])
		}
		if err != nil {
			return err
		}
		mode := 0
		if m := flagOf(args, "--mode", ""); m != "" {
			v, err := strconv.ParseInt(m, 8, 32)
			if err != nil {
				return fmt.Errorf("--mode %q: octal, e.g. 0644", m)
			}
			mode = int(v)
		}
		if err := carryPutFile(t, state, pos[1], data, mode); err != nil {
			return err
		}
		fmt.Printf("put %s on %s (%d bytes)\n", pos[1], where(t, state), len(data))
		return nil
	case "get", "pull":
		if len(pos) < 2 {
			return fmt.Errorf("usage: wfx config get <remote-abs-path> [-o local-file] [--on worker] [--from ctx]")
		}
		t, err := carryTarget(args, "--from")
		if err != nil {
			return err
		}
		data, err := carryGetFile(t, state, pos[1])
		if err != nil {
			return err
		}
		if dest := flagOf(args, "-o", ""); dest != "" {
			if err := os.WriteFile(dest, data, 0o600); err != nil {
				return err
			}
			fmt.Printf("wrote %s (%d bytes) from %s\n", dest, len(data), where(t, state))
			return nil
		}
		_, err = os.Stdout.Write(data)
		return err
	case "ls", "list":
		t, err := carryTarget(args, "--from")
		if err != nil {
			return err
		}
		files, version, err := carryListFiles(t, state, flagOf(args, "--dir", ""))
		if err != nil {
			return err
		}
		return printJSONOr(args, map[string]any{"version": version, "entries": files}, func() {
			fmt.Printf("%s — version %d\n", where(t, state), version)
			if len(files) == 0 {
				fmt.Println("  no config files")
			}
			for _, f := range files {
				fmt.Printf("  %-50s %8d  %o\n", f.Path, f.Size, f.Mode)
			}
		})
	case "rm", "remove":
		if len(pos) < 2 {
			return fmt.Errorf("usage: wfx config rm <remote-abs-path> [--on worker]")
		}
		t, err := carryTarget(args, "--to")
		if err != nil {
			return err
		}
		if err := callWith(t, "DELETE", withQuery("/api/carry/file", stateQuery(state), "path="+url.QueryEscape(pos[1])), nil, nil); err != nil {
			return err
		}
		fmt.Printf("removed %s from %s\n", pos[1], where(t, state))
		return nil
	case "env":
		return configEnv(args, pos[1:], state)
	case "log", "history":
		t, err := carryTarget(args, "--from")
		if err != nil {
			return err
		}
		var out struct {
			State    string `json:"state"`
			Versions []struct {
				Version int      `json:"version"`
				Op      string   `json:"op"`
				Args    []string `json:"args"`
				At      string   `json:"at"`
				By      string   `json:"by"`
			} `json:"versions"`
		}
		lim := ""
		if l := flagOf(args, "--limit", ""); l != "" {
			lim = "limit=" + url.QueryEscape(l)
		}
		if err := callWith(t, "GET", withQuery("/api/carry/log", stateQuery(state), lim), nil, &out); err != nil {
			return err
		}
		return printJSONOr(args, out, func() {
			fmt.Printf("%-8s %-22s %-14s %-12s %s\n", "VERSION", "AT", "BY", "OP", "ARGS")
			for _, v := range out.Versions {
				fmt.Printf("%-8d %-22s %-14s %-12s %s\n", v.Version, v.At, v.By, v.Op, strings.Join(v.Args, " "))
			}
		})
	case "rollback":
		if len(pos) < 2 {
			return fmt.Errorf("usage: wfx config rollback <version> [--on worker]")
		}
		v, err := strconv.Atoi(pos[1])
		if err != nil {
			return fmt.Errorf("version %q is not a number — `wfx config log` lists them", pos[1])
		}
		t, err := carryTarget(args, "--to")
		if err != nil {
			return err
		}
		var out map[string]any
		if err := callWith(t, "POST", "/api/carry/rollback", map[string]any{"state": state, "version": v}, &out); err != nil {
			return err
		}
		fmt.Printf("rolled %s back to version %d — now version %v\n", where(t, state), v, out["version"])
		return nil
	case "status":
		t, err := carryTarget(args, "--from")
		if err != nil {
			return err
		}
		var out map[string]any
		if err := callWith(t, "GET", "/api/carry", nil, &out); err != nil {
			return err
		}
		return printJSONOr(args, out, func() {
			fmt.Printf("context          %s\n", t.name)
			fmt.Printf("carry            %s\n", tick(out["ready"] == true, "ready", fmt.Sprint(out["reason"])))
			fmt.Printf("server state     %v\n", out["state"])
		})
	default:
		return fmt.Errorf("unknown `wfx config %s` — put | get | ls | rm | env | log | rollback | status", sub)
	}
}

// configEnv is `wfx config env set K=V… | unset K… | ls`. A listing shows
// names only; a value is never printed.
func configEnv(args, pos []string, state string) error {
	sub := "ls"
	if len(pos) > 0 {
		sub = pos[0]
	}
	switch sub {
	case "set":
		vars := map[string]string{}
		for _, kv := range pos[1:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" {
				return fmt.Errorf("%q: want KEY=value", k)
			}
			vars[k] = v
		}
		if len(vars) == 0 {
			return fmt.Errorf("usage: wfx config env set KEY=value… [--on worker] — non-secret defaults only; keys go in `wfx env set`")
		}
		t, err := carryTarget(args, "--to")
		if err != nil {
			return err
		}
		if err := callWith(t, "PUT", "/api/carry/env", map[string]any{"state": state, "vars": vars}, nil); err != nil {
			return err
		}
		fmt.Printf("set %s on %s\n", strings.Join(sortedKeys(vars), ", "), where(t, state))
		return nil
	case "unset", "rm":
		if len(pos) < 2 {
			return fmt.Errorf("usage: wfx config env unset KEY… [--on worker]")
		}
		t, err := carryTarget(args, "--to")
		if err != nil {
			return err
		}
		for _, k := range pos[1:] {
			if err := callWith(t, "DELETE", withQuery("/api/carry/env/"+url.PathEscape(k), stateQuery(state)), nil, nil); err != nil {
				return err
			}
		}
		fmt.Printf("unset %s on %s\n", strings.Join(pos[1:], ", "), where(t, state))
		return nil
	case "ls", "list":
		t, err := carryTarget(args, "--from")
		if err != nil {
			return err
		}
		var out struct {
			State string   `json:"state"`
			Keys  []string `json:"keys"`
		}
		if err := callWith(t, "GET", withQuery("/api/carry/env", stateQuery(state)), nil, &out); err != nil {
			return err
		}
		return printJSONOr(args, out, func() {
			fmt.Printf("%s — env defaults (names only)\n", where(t, state))
			for _, k := range out.Keys {
				fmt.Println("  " + k)
			}
		})
	default:
		return fmt.Errorf("unknown `wfx config env %s` — set | unset | ls", sub)
	}
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func readAllStdin() ([]byte, error) {
	var b []byte
	buf := make([]byte, 32*1024)
	for {
		n, err := os.Stdin.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return b, nil
			}
			return b, err
		}
	}
}

// ---- the carry calls the move path shares ---------------------------------

type carryEntry struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Size int    `json:"size"`
	Mode int    `json:"mode"`
}

func carryListFiles(t resolved, state, dir string) ([]carryEntry, int, error) {
	var out struct {
		Version int          `json:"version"`
		Entries []carryEntry `json:"entries"`
	}
	d := ""
	if dir != "" {
		d = "dir=" + url.QueryEscape(dir)
	}
	err := callWith(t, "GET", withQuery("/api/carry/files", stateQuery(state), d), nil, &out)
	return out.Entries, out.Version, err
}

func carryGetFile(t resolved, state, p string) ([]byte, error) {
	var out struct {
		ContentBase64 string `json:"content_base64"`
	}
	if err := callWith(t, "GET", withQuery("/api/carry/file", stateQuery(state), "path="+url.QueryEscape(p)), nil, &out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.ContentBase64)
}

func carryPutFile(t resolved, state, p string, data []byte, mode int) error {
	return callWith(t, "PUT", "/api/carry/file", map[string]any{
		"state": state, "path": p, "content_base64": base64.StdEncoding.EncodeToString(data), "mode": mode,
	}, nil)
}

func carryEnvValues(t resolved, state string) (map[string]string, error) {
	var out struct {
		Env map[string]string `json:"env"`
	}
	err := callWith(t, "GET", withQuery("/api/carry/env", stateQuery(state), "values=1"), nil, &out)
	return out.Env, err
}

// ---- wfx sync --------------------------------------------------------------

// A sync endpoint is a context (by name) or a local folder, "local:DIR".
// A folder holds files/<absolute path> and env.defaults (KEY=value, 0600).
type syncEnd struct {
	dir   string // set for a local folder
	t     resolved
	state string
}

func (e syncEnd) String() string {
	if e.dir != "" {
		return "local:" + e.dir
	}
	return where(e.t, e.state)
}

func parseSyncEnd(spec, state string) (syncEnd, error) {
	if spec == "" {
		return syncEnd{}, fmt.Errorf("usage: wfx sync --from <context|local:DIR> --to <context|local:DIR>")
	}
	if spec == "local" {
		return syncEnd{dir: ".wfx-carry"}, nil
	}
	if d, ok := strings.CutPrefix(spec, "local:"); ok {
		return syncEnd{dir: d}, nil
	}
	t, err := resolveContext(spec)
	if err != nil {
		return syncEnd{}, err
	}
	return syncEnd{t: t, state: state}, nil
}

type syncSet struct {
	files map[string][]byte
	modes map[string]int
	env   map[string]string
}

func (e syncEnd) read(only map[string]bool) (syncSet, error) {
	s := syncSet{files: map[string][]byte{}, modes: map[string]int{}, env: map[string]string{}}
	if e.dir != "" {
		if only["files"] {
			root := filepath.Join(e.dir, "files")
			err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					if os.IsNotExist(err) {
						return nil
					}
					return err
				}
				rel, _ := filepath.Rel(root, p)
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				info, _ := d.Info()
				key := "/" + filepath.ToSlash(rel)
				s.files[key] = b
				s.modes[key] = int(info.Mode().Perm())
				return nil
			})
			if err != nil && !os.IsNotExist(err) {
				return s, err
			}
		}
		if only["env"] {
			raw, err := os.ReadFile(filepath.Join(e.dir, "env.defaults"))
			if err != nil && !os.IsNotExist(err) {
				return s, err
			}
			for _, line := range strings.Split(string(raw), "\n") {
				if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k != "" && !strings.HasPrefix(k, "#") {
					s.env[k] = v
				}
			}
		}
		return s, nil
	}
	if only["files"] {
		entries, _, err := carryListFiles(e.t, e.state, "")
		if err != nil {
			return s, err
		}
		for _, en := range entries {
			if en.Kind != "" && en.Kind != "file" {
				continue
			}
			b, err := carryGetFile(e.t, e.state, en.Path)
			if err != nil {
				return s, err
			}
			s.files[en.Path] = b
			s.modes[en.Path] = en.Mode
		}
	}
	if only["env"] {
		env, err := carryEnvValues(e.t, e.state)
		if err != nil {
			return s, err
		}
		s.env = env
	}
	return s, nil
}

func (e syncEnd) write(s syncSet) error {
	if e.dir != "" {
		for p, b := range s.files {
			dest := filepath.Join(e.dir, "files", filepath.FromSlash(strings.TrimPrefix(p, "/")))
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				return err
			}
			mode := os.FileMode(s.modes[p])
			if mode == 0 {
				mode = 0o600
			}
			if err := os.WriteFile(dest, b, mode); err != nil {
				return err
			}
		}
		if len(s.env) > 0 {
			var b strings.Builder
			for _, k := range sortedKeys(s.env) {
				b.WriteString(k + "=" + s.env[k] + "\n")
			}
			if err := os.MkdirAll(e.dir, 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(e.dir, "env.defaults"), []byte(b.String()), 0o600); err != nil {
				return err
			}
		}
		return nil
	}
	paths := make([]string, 0, len(s.files))
	for p := range s.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := carryPutFile(e.t, e.state, p, s.files[p], s.modes[p]); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	if len(s.env) > 0 {
		return callWith(e.t, "PUT", "/api/carry/env", map[string]any{"state": e.state, "vars": s.env}, nil)
	}
	return nil
}

// syncCmd moves config files and env defaults between places: a context's
// carry state and another's, or a local folder. Additive: nothing at the
// destination is deleted. Secrets are not moved — they live in the env store.
func syncCmd(args []string) error {
	state := carryState(args)
	toState := flagOf(args, "--to-state", state)
	from, err := parseSyncEnd(flagOf(args, "--from", ""), state)
	if err != nil {
		return err
	}
	to, err := parseSyncEnd(flagOf(args, "--to", ""), toState)
	if err != nil {
		return err
	}
	only := map[string]bool{"files": true, "env": true}
	if o := flagOf(args, "--only", ""); o != "" {
		only = map[string]bool{}
		for _, w := range strings.Split(o, ",") {
			w = strings.TrimSpace(w)
			if w != "files" && w != "env" {
				return fmt.Errorf("--only %q: files | env (workflows and skills move with `wfx publish` / `wfx pull`)", w)
			}
			only[w] = true
		}
	}
	set, err := from.read(only)
	if err != nil {
		return fmt.Errorf("reading %s: %w", from, err)
	}
	paths := make([]string, 0, len(set.files))
	for p := range set.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	verb := "copied"
	if hasFlag(args, "--dry-run") {
		verb = "would copy"
	}
	fmt.Printf("%s → %s\n", from, to)
	for _, p := range paths {
		fmt.Printf("  %s file %s (%d bytes)\n", verb, p, len(set.files[p]))
	}
	if len(set.env) > 0 {
		fmt.Printf("  %s env defaults: %s\n", verb, strings.Join(sortedKeys(set.env), ", "))
	}
	if len(paths) == 0 && len(set.env) == 0 {
		fmt.Println("  nothing to move")
		return nil
	}
	if hasFlag(args, "--dry-run") {
		return nil
	}
	return to.write(set)
}

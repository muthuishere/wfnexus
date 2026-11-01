package engine

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/muthuishere/wfnexus/apps/api/internal/assets"
	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
)

// What `wfx skill add` and `wfx mcp add` put on a server.
//
// Skills added by name or by path land in ONE directory the server owns,
// <workdir>/installed-skills/<name>/, which is a registry root after the
// machine's own roots and before imported git sources. Git URLs stay skill
// sources (skillsources.go) — one mechanism per kind of origin.
//
// MCP servers are entries in the server's mcp.json. Their credentials are env
// store NAMES (McpServer.EnvFrom), resolved when a step is built.
//
// Every write here is followed by a reload, so the change is live without a
// restart; the catalog watcher (watch.go) does the same for edits made by hand.

var skillNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// InstalledSkillsDir is the server-owned root for `wfx skill add` installs.
func (e *Engine) InstalledSkillsDir() string {
	return filepath.Join(e.cfg.WorkDir, "installed-skills")
}

// ManagedPaths are the files and directories `wfx skill`/`wfx mcp` change.
// The API versions them in the server's carry state and restores them on a
// rollback, so `wfx config log` / `wfx config rollback` cover these changes.
func (e *Engine) ManagedPaths() (files []string, dirs []string) {
	return []string{e.cfg.McpConfig, e.skillSourcesPath()}, []string{e.InstalledSkillsDir()}
}

// InstallSkill writes one skill into the installed-skills root. files maps a
// slash path inside the skill to its bytes and must hold SKILL.md; empty files
// means "the skill of that name this platform ships".
func (e *Engine) InstallSkill(name string, files map[string][]byte) error {
	if !skillNameRe.MatchString(name) {
		return fmt.Errorf("skill name %q: lowercase letters, digits, '.', '_' and '-' only", name)
	}
	if len(files) == 0 {
		var err error
		if files, err = shippedSkill(name); err != nil {
			return err
		}
	}
	md, ok := files["SKILL.md"]
	if !ok {
		return fmt.Errorf("skill %q has no SKILL.md at its top level", name)
	}
	// The registry keys a skill by its frontmatter name, so a directory whose
	// SKILL.md says otherwise would install one name and register another.
	if fm := FrontmatterName(md); fm != "" && fm != name {
		return fmt.Errorf("skill %q: its SKILL.md says name: %s — install it as %s", name, fm, fm)
	}
	root := e.InstalledSkillsDir()
	tmp, err := os.MkdirTemp(filepathOrMk(root), "."+name+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for rel, data := range files {
		clean := filepath.Clean(filepath.FromSlash(rel))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("skill %q: path %q leaves the skill directory", name, rel)
		}
		dst := filepath.Join(tmp, clean)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return err
		}
	}
	// Swap the whole directory in, so a reload never sees half a skill.
	final := filepath.Join(root, name)
	old := final + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(final); err == nil {
		if err := os.Rename(final, old); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Rename(old, final)
		return err
	}
	_ = os.RemoveAll(old)
	return e.ReloadDefinitions()
}

// FrontmatterName is the `name:` in a SKILL.md's frontmatter, or "".
func FrontmatterName(md []byte) string {
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

func filepathOrMk(dir string) string {
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// shippedSkill reads a skill compiled into this binary.
func shippedSkill(name string) (map[string][]byte, error) {
	dir, err := assets.Extract(assets.SkillsName)
	if err != nil {
		return nil, fmt.Errorf("no skill named %q ships with this server — pass a path or a git URL", name)
	}
	src := filepath.Join(dir, name)
	if _, err := os.Stat(filepath.Join(src, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("no skill named %q ships with this server — pass a path or a git URL", name)
	}
	out := map[string][]byte{}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	return out, err
}

// RemoveInstalledSkill deletes one skill from the installed-skills root.
func (e *Engine) RemoveInstalledSkill(name string) error {
	if !skillNameRe.MatchString(name) {
		return fmt.Errorf("skill name %q is not valid", name)
	}
	dir := filepath.Join(e.InstalledSkillsDir(), name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no installed skill named %q", name)
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return e.ReloadDefinitions()
}

// InstalledSkills lists the installed-skills root.
func (e *Engine) InstalledSkills() []string {
	entries, _ := os.ReadDir(e.InstalledSkillsDir())
	var out []string
	for _, en := range entries {
		if en.IsDir() && !strings.HasPrefix(en.Name(), ".") && !strings.HasSuffix(en.Name(), ".old") {
			out = append(out, en.Name())
		}
	}
	sort.Strings(out)
	return out
}

// SaveMcp adds or replaces an MCP server, then reloads.
func (e *Engine) SaveMcp(m catalog.McpServer) error {
	if err := catalog.SaveMcp(e.cfg.McpConfig, m); err != nil {
		return err
	}
	return e.ReloadDefinitions()
}

// DeleteMcp removes an MCP server, then reloads.
func (e *Engine) DeleteMcp(name string) error {
	if err := catalog.DeleteMcp(e.cfg.McpConfig, name); err != nil {
		return err
	}
	return e.ReloadDefinitions()
}

// resolveMcpEnv injects the values an MCP entry names in envFrom into its
// environment, from the env store (system scope) and then this process. A
// name that resolves nowhere is an ERROR naming it — never a server started
// silently without its credential. Values never reach a log or an event.
func (e *Engine) resolveMcpEnv(ctx context.Context, name string, entry any) (any, error) {
	m, ok := entry.(map[string]any)
	if !ok {
		return entry, nil
	}
	raw, _ := m["envFrom"].([]any)
	if len(raw) == 0 {
		return entry, nil
	}
	store, err := e.platformEnv(ctx, "")
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for k, v := range m {
		if k != "envFrom" {
			out[k] = v
		}
	}
	env := map[string]any{}
	if have, ok := m["environment"].(map[string]any); ok {
		for k, v := range have {
			env[k] = v
		}
	}
	for _, n := range raw {
		key, _ := n.(string)
		v, ok := store[key]
		if !ok {
			v, ok = os.LookupEnv(key)
		}
		if !ok {
			return nil, fmt.Errorf("mcp server %q needs %s — set it with `wfx env set %s`", name, key, key)
		}
		env[key] = v
	}
	out["environment"] = env
	return out, nil
}

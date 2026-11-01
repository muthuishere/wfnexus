package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
)

// PackagesSnapshot is everything `wfx skill` and `wfx mcp` change on a server,
// as ONE document. The API puts it into the server's carry state after every
// change — one change, one carry version — so `wfx config log` lists it and
// `wfx config rollback <n>` brings it back (RestorePackages). It holds names
// and file contents, never a secret value: MCP credentials are env-store
// names (envFrom), and the env store is not part of it.
type PackagesSnapshot struct {
	Version int `json:"version"`
	// Mcp and SkillSources are the files verbatim; nil means "absent".
	Mcp          json.RawMessage `json:"mcp,omitempty"`
	SkillSources json.RawMessage `json:"skillSources,omitempty"`
	// Skills is the installed-skills root: skill → relative path → bytes.
	Skills map[string]map[string][]byte `json:"skills"`
}

// PackagesSnapshotPath is where the snapshot sits in the server's carry state.
func (e *Engine) PackagesSnapshotPath() string {
	p, err := filepath.Abs(filepath.Join(e.cfg.WorkDir, "packages.snapshot.json"))
	if err != nil {
		return filepath.Join(e.cfg.WorkDir, "packages.snapshot.json")
	}
	return p
}

// SnapshotPackages reads the current state off disk.
func (e *Engine) SnapshotPackages() (PackagesSnapshot, error) {
	s := PackagesSnapshot{Version: 1, Skills: map[string]map[string][]byte{}}
	if b, err := os.ReadFile(e.cfg.McpConfig); err == nil {
		s.Mcp = b
	} else if !os.IsNotExist(err) {
		return s, err
	}
	if b, err := os.ReadFile(e.skillSourcesPath()); err == nil {
		s.SkillSources = b
	} else if !os.IsNotExist(err) {
		return s, err
	}
	for _, name := range e.InstalledSkills() {
		root := filepath.Join(e.InstalledSkillsDir(), name)
		files := map[string][]byte{}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			b, err := os.ReadFile(p)
			files[filepath.ToSlash(rel)] = b
			return err
		})
		if err != nil {
			return s, err
		}
		s.Skills[name] = files
	}
	return s, nil
}

// RestorePackages makes the disk match a snapshot, then reloads: mcp.json and
// the skill-source list are rewritten (or removed), installed skills not in it
// are deleted and the ones in it are written. A skill source whose checkout is
// missing after the restore is re-fetched at its recorded ref.
func (e *Engine) RestorePackages(ctx context.Context, s PackagesSnapshot) error {
	write := func(path string, raw json.RawMessage) error {
		if raw == nil {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		tmp := path + ".restore"
		if err := os.WriteFile(tmp, raw, 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, path)
	}
	if err := write(e.cfg.McpConfig, s.Mcp); err != nil {
		return fmt.Errorf("mcp.json: %w", err)
	}
	if err := write(e.skillSourcesPath(), s.SkillSources); err != nil {
		return fmt.Errorf("skill sources: %w", err)
	}
	for _, have := range e.InstalledSkills() {
		if _, keep := s.Skills[have]; !keep {
			if err := os.RemoveAll(filepath.Join(e.InstalledSkillsDir(), have)); err != nil {
				return err
			}
		}
	}
	for name, files := range s.Skills {
		// InstallSkill reloads each time; cheap, and it keeps one write path.
		if err := e.InstallSkill(name, files); err != nil {
			return fmt.Errorf("skill %s: %w", name, err)
		}
	}
	list, err := e.readSkillSources()
	if err != nil {
		return err
	}
	for _, src := range list {
		if _, err := os.Stat(e.skillSourceDir(src.Name)); os.IsNotExist(err) {
			if _, err := e.resyncSkillSource(ctx, src.Name, src.Ref); err != nil {
				log.Printf("restore: skill source %s could not be re-fetched: %v", src.Name, err)
			}
		}
	}
	return e.ReloadDefinitions()
}

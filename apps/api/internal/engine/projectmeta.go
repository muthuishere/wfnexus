package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// Project metadata that is not WHERE the workflows live — the category, for
// now. Kept apart from sources.json because `local` is a project too and is
// not a source anyone imported, and one mechanism for every project beats a
// special case for the first one.

const projectMetaFile = "projects.json"

type projectMeta struct {
	Category string `json:"category,omitempty"`
}

var projectMetaMu sync.Mutex

func (e *Engine) projectMetaPath() string { return filepath.Join(e.cfg.WorkDir, projectMetaFile) }

func (e *Engine) readProjectMeta() map[string]projectMeta {
	out := map[string]projectMeta{}
	raw, err := os.ReadFile(e.projectMetaPath())
	if err != nil {
		return out
	}
	_ = json.Unmarshal(raw, &out)
	return out
}

// SetProjectCategory records what kind of work a project does. The project
// must exist, and the category must be one of workflow.Categories ("" clears
// it).
func (e *Engine) SetProjectCategory(name, category string) error {
	if err := workflow.ValidCategory(category); err != nil {
		return err
	}
	known := false
	for _, s := range e.Sources() {
		known = known || (s.Name == name && s.Name != templatesSource)
	}
	if !known {
		return fmt.Errorf("no project named %q", name)
	}
	projectMetaMu.Lock()
	defer projectMetaMu.Unlock()
	all := e.readProjectMeta()
	m := all[name]
	m.Category = category
	if m == (projectMeta{}) {
		delete(all, name)
	} else {
		all[name] = m
	}
	if err := os.MkdirAll(e.cfg.WorkDir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp := e.projectMetaPath() + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, e.projectMetaPath())
}

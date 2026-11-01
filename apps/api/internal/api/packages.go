package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/muthuishere/wfnexus/apps/api/internal/carryops"
	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
)

// `wfx skill add|rm|ls` and `wfx mcp add|rm|ls` against the current profile.
//
// Every change is applied, the catalog reloaded (live, no restart), and then
// the whole package state is put into the server's carry state as ONE file —
// one change, one carry version — so `wfx config log` shows it and
// `wfx config rollback <n>` restores it (carryRollback calls restorePackages).
// With carry off the change still applies; the response says it is not
// versioned and why, rather than pretending.

func (s *Server) packageRoutes(r chi.Router) {
	r.Get("/skills/installed", s.listInstalledSkills)
	r.Post("/skills/installed", s.installSkill)
	r.Delete("/skills/installed/{name}", s.removeInstalledSkill)
	r.Put("/mcp/{name}", s.saveMcp)
	r.Delete("/mcp/{name}", s.deleteMcp)
}

// versionPackages records the current package state in carry.
func (s *Server) versionPackages(ctx context.Context) map[string]any {
	c := carryops.FromEnv()
	if err := c.Ready(); err != nil {
		return map[string]any{"versioned": false, "reason": err.Error()}
	}
	snap, err := s.eng.SnapshotPackages()
	if err != nil {
		return map[string]any{"versioned": false, "reason": err.Error()}
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		return map[string]any{"versioned": false, "reason": err.Error()}
	}
	out, err := c.Put(ctx, "", s.eng.PackagesSnapshotPath(), raw, 0o600)
	if err != nil {
		return map[string]any{"versioned": false, "reason": err.Error()}
	}
	return map[string]any{"versioned": true, "version": out["version"]}
}

// restorePackages applies the snapshot a carry rollback brought back. A state
// that never held one (no package change was ever versioned) is left alone.
func (s *Server) restorePackages(ctx context.Context, state string) (bool, error) {
	c := carryops.FromEnv()
	if state != "" && state != c.DefaultState {
		return false, nil // a worker's state: nothing of the server's to restore
	}
	raw, err := c.Get(ctx, "", s.eng.PackagesSnapshotPath())
	if err != nil {
		if ce, ok := err.(*carryops.Error); ok && ce.Code == "not_found" {
			return false, nil
		}
		return false, err
	}
	var snap engine.PackagesSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return false, fmt.Errorf("packages snapshot: %w", err)
	}
	return true, s.eng.RestorePackages(ctx, snap)
}

func (s *Server) listInstalledSkills(w http.ResponseWriter, _ *http.Request) {
	sources, err := s.eng.SkillSources()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"installed": s.eng.InstalledSkills(), "sources": sources})
}

type installSkillBody struct {
	Name string `json:"name"`
	// Files is relative path → content (JSON []byte is base64). Empty means
	// the skill of that name this server ships.
	Files map[string][]byte `json:"files"`
}

func (s *Server) installSkill(w http.ResponseWriter, r *http.Request) {
	var b installSkillBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&b); err != nil || b.Name == "" {
		writeErr(w, 400, fmt.Errorf("body needs {\"name\": \"…\", \"files\": {…}}"))
		return
	}
	if err := s.eng.InstallSkill(b.Name, b.Files); err != nil {
		writeErr(w, 400, err)
		return
	}
	out := s.versionPackages(r.Context())
	out["skill"] = b.Name
	writeJSON(w, 200, out)
}

func (s *Server) removeInstalledSkill(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if err := s.eng.RemoveInstalledSkill(name); err != nil {
		writeErr(w, 404, err)
		return
	}
	out := s.versionPackages(r.Context())
	out["skill"] = name
	writeJSON(w, 200, out)
}

func (s *Server) saveMcp(w http.ResponseWriter, r *http.Request) {
	var m catalog.McpServer
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeErr(w, 400, err)
		return
	}
	m.Name = urlName(r, "name")
	if err := s.eng.SaveMcp(m); err != nil {
		writeErr(w, 400, err)
		return
	}
	out := s.versionPackages(r.Context())
	out["mcp"] = m.Name
	writeJSON(w, 200, out)
}

func (s *Server) deleteMcp(w http.ResponseWriter, r *http.Request) {
	name := urlName(r, "name")
	if err := s.eng.DeleteMcp(name); err != nil {
		writeErr(w, 404, err)
		return
	}
	out := s.versionPackages(r.Context())
	out["mcp"] = name
	writeJSON(w, 200, out)
}

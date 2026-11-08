package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/buildinfo"
)

// wfxInfo is the "wfx" card on the Modules page: what this server runs, the
// latest release (best effort, GitHub, 3s), its default provider and channels,
// and the one command that upgrades a server (run from a laptop; the server
// never upgrades itself).
func (s *Server) wfxInfo(w http.ResponseWriter, r *http.Request) {
	latest := ""
	c := &http.Client{Timeout: 3 * time.Second}
	if resp, err := c.Get("https://api.github.com/repos/muthuishere/wfnexus/releases/latest"); err == nil {
		var rel struct {
			Tag string `json:"tag_name"`
		}
		if resp.StatusCode == 200 && json.NewDecoder(resp.Body).Decode(&rel) == nil {
			latest = rel.Tag
		}
		resp.Body.Close()
	}
	upgrade := "wfx server deploy <host>"
	if latest != "" {
		upgrade += " --version " + latest
	}
	writeJSON(w, 200, map[string]any{
		"version":         buildinfo.Get(),
		"latest":          latest,
		"defaultProvider": s.eng.DefaultProvider(),
		"defaultChannel":  s.eng.DefaultChannel(),
		"channels":        s.eng.Channels(r.Context()),
		"upgrade":         upgrade,
	})
}

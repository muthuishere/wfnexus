package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/muthuishere/wfnexus/apps/api/internal/builtins"
)

// Built-in tools (internal/builtins): aos, ctx-optimize, jevx, tmvp, gh on every runner.
// The server keeps the DESIRED versions; every runner converges on them at its
// next heartbeat and reports what it has.

type runnerBuiltins struct {
	Worker string            `json:"worker"`
	Tools  []builtins.Status `json:"tools"`
	At     time.Time         `json:"at"`
}

var reported sync.Map // worker name -> runnerBuiltins

type builtinRow struct {
	Name    string            `json:"name"`
	Repo    string            `json:"repo"`
	Pinned  string            `json:"pinned"`
	Desired string            `json:"desired"`
	Latest  string            `json:"latest,omitempty"`
	Runners map[string]string `json:"runners"` // runner -> version ("✗ reason" when missing)
}

// listBuiltins: GET /api/builtins[?latest=1].
func (s *Server) listBuiltins(w http.ResponseWriter, r *http.Request) {
	dir := builtins.DefaultDir()
	want := builtins.Desired(dir)
	var rows []builtinRow
	// node runs the tools (bundled when missing); python is reported only.
	all := append(append([]builtins.Tool{}, builtins.Pinned...), builtins.Tool{Name: "node"}, builtins.Tool{Name: "python"})
	for _, t := range all {
		row := builtinRow{Name: t.Name, Repo: t.Package, Pinned: t.Version, Desired: t.Version, Runners: map[string]string{}}
		if v := want[t.Name]; v != "" {
			row.Desired = v
		}
		if r.URL.Query().Get("latest") == "1" && t.Package != "" {
			row.Latest, _ = builtins.Latest(t)
		}
		put := func(who string, st []builtins.Status) {
			for _, x := range st {
				if x.Name == t.Name {
					v := x.Version
					if x.Error != "" {
						v = "✗ " + x.Error
					}
					row.Runners[who] = v
				}
			}
		}
		put("server", builtins.Last())
		reported.Range(func(k, v any) bool { put(k.(string), v.(runnerBuiltins).Tools); return true })
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": rows})
}

// updateBuiltin: POST /api/builtins/{name}/update {"version": ""} — empty means
// the latest release. The server fetches and verifies it first; only then is
// it the desired version every runner moves to. An older version is a rollback.
func (s *Server) updateBuiltin(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	t, ok := builtins.Find(name)
	if !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no built-in tool %q", name))
		return
	}
	var b struct {
		Version string `json:"version"`
	}
	_ = json.NewDecoder(r.Body).Decode(&b)
	v := b.Version
	if v == "" {
		var err error
		if v, err = builtins.Latest(t); err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
	}
	dir := builtins.DefaultDir()
	want := builtins.Desired(dir)
	want[name] = v
	for _, st := range builtins.Ensure(dir, want) {
		if st.Name == name && st.Error != "" {
			// Not recorded: runners keep what they have.
			builtins.Ensure(dir, builtins.Desired(dir))
			writeErr(w, http.StatusBadGateway, fmt.Errorf("%s %s: %s", name, v, st.Error))
			return
		}
	}
	if err := builtins.SetDesired(dir, name, v); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "version": v})
}

// workerBuiltins: GET /api/workers/builtins — the desired versions, for a runner.
func (s *Server) workerBuiltins(w http.ResponseWriter, r *http.Request) {
	if s.authWorker(w, r) == nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"desired": builtins.Desired(builtins.DefaultDir())})
}

// workerBuiltinsReport: POST /api/workers/builtins [{name,version,error}].
func (s *Server) workerBuiltinsReport(w http.ResponseWriter, r *http.Request) {
	wk := s.authWorker(w, r)
	if wk == nil {
		return
	}
	var st []builtins.Status
	if err := json.NewDecoder(r.Body).Decode(&st); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	reported.Store(wk.Name, runnerBuiltins{Worker: wk.Name, Tools: st, At: time.Now()})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

package engine

import (
	"context"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// Catalog hot-reload. The server's catalog — providers and classifiers
// (registries.json), MCP servers (mcp.json), and every skill root — is
// re-read when any of its inputs changes on disk, without a restart. That
// covers `wfx skill add`, `wfx mcp add`, a carry rollback that rewrites
// mcp.json, and an operator editing a file by hand.
//
// A reload is the existing ReloadDefinitions: it builds a NEW registry and
// swaps the pointers under the engine lock, and a bad load changes nothing.
// A step that has already built its toolkit keeps it; the next step reads the
// new catalog. Nothing is mutated in place.
//
// Polling, not fsnotify: it adds no dependency, it works on every filesystem
// a volume can be (NFS, overlay, a ConfigMap's symlink swap), and a two-second
// fingerprint of a few stat calls costs nothing.

// catalogFingerprint hashes the mtime and size of every catalog input. Skill
// roots are walked two levels deep — <root>/<skill>/SKILL.md — which is where
// a skill appears, disappears or changes.
func (e *Engine) catalogFingerprint() uint64 {
	h := fnv.New64a()
	stamp := func(p string) {
		st, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(h, "%s:-;", p)
			return
		}
		fmt.Fprintf(h, "%s:%d:%d;", p, st.ModTime().UnixNano(), st.Size())
	}
	stamp(e.cfg.RegistriesPath)
	stamp(e.cfg.McpConfig)
	stamp(e.skillSourcesPath())
	dirs := []string{e.InstalledSkillsDir()}
	for _, r := range e.skillRoots() {
		dirs = append(dirs, r.Dir)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			fmt.Fprintf(h, "%s:-;", d)
			continue
		}
		fmt.Fprintf(h, "%s:%d;", d, len(entries))
		for _, en := range entries {
			if en.IsDir() {
				stamp(filepath.Join(d, en.Name(), "SKILL.md"))
			}
		}
	}
	return h.Sum64()
}

// StartCatalogWatcher reloads the catalog whenever its fingerprint changes.
// WFX_CATALOG_POLL sets the interval in seconds (default 2; 0 turns it off).
func (e *Engine) StartCatalogWatcher(ctx context.Context) {
	every := 2 * time.Second
	if v := os.Getenv("WFX_CATALOG_POLL"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			log.Printf("catalog: WFX_CATALOG_POLL=%q is not a number of seconds — using 2", v)
		} else if n == 0 {
			log.Printf("catalog: hot reload off (WFX_CATALOG_POLL=0)")
			return
		} else {
			every = time.Duration(n) * time.Second
		}
	}
	go e.watchCatalog(ctx, every)
}

func (e *Engine) watchCatalog(ctx context.Context, every time.Duration) {
	last := e.catalogFingerprint()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := e.catalogFingerprint()
		if now == last {
			continue
		}
		if err := e.ReloadDefinitions(); err != nil {
			// The old catalog stays live. Logged once per distinct state, so a
			// broken file does not fill the log every two seconds.
			log.Printf("catalog: change seen but not loaded (the previous catalog stays live): %v", err)
		} else {
			log.Printf("catalog: reloaded after a change on disk")
		}
		last = now
	}
}

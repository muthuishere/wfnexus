// wfnexus API: workflow engine on toolnexus, Postgres state, S3 artifacts.
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/api"
	"github.com/muthuishere/wfnexus/apps/api/internal/blob"
	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/skills"
	"github.com/muthuishere/wfnexus/apps/api/internal/store"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// logDoctor writes the startup summary: the default model every step falls back
// to, and every provider and classifier that could not run if named right now.
func logDoctor(d engine.Doctor) {
	log.Printf("default model: %s via %s (%s), key %s=%s",
		d.Default.Model, d.Default.BaseURL, d.Default.Style, d.Default.APIKeyEnv,
		map[bool]string{true: "set", false: "NOT SET"}[d.Default.KeySet])
	for _, p := range d.Providers {
		if !p.Ready {
			continue
		}
		log.Printf("  provider   %-14s %-5s %s %s", p.Name, p.Kind, p.Model, p.Detail)
	}
	for _, c := range d.Classifiers {
		if c.Ready {
			log.Printf("  classifier %-14s %-5s %s", c.Name, c.Kind, c.Model)
		}
	}
	for _, problem := range d.Problems {
		log.Printf("  NOT READY: %s", problem)
	}
	if len(d.Problems) > 0 {
		log.Printf("  %d entry/entries above will fail if a step names them — `wfx doctor` for the detail",
			len(d.Problems))
	}
}

func main() {
	cfg := config.Load()
	ctx := context.Background()

	if err := store.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("postgres: %v", err)
	}
	defer st.Close()

	bl, err := blob.Open(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket, cfg.S3UseSSL)
	if err != nil {
		log.Fatalf("s3: %v", err)
	}

	reg := skills.Load(skills.DefaultRoots(cfg.SkillsDir)...)
	log.Printf("skill registry: %d skills from %v", len(reg.List()), reg.Roots())
	for _, sk := range reg.Skipped() {
		log.Printf("  skipped %s (%s)", sk.Location, sk.Reason)
	}

	cat, err := catalog.Load(cfg.RegistriesPath, cfg.McpConfig)
	if err != nil {
		log.Fatalf("registries: %v", err)
	}
	log.Printf("registries: %d providers, %d classifiers, %d mcp servers",
		cat.Providers.Len(), cat.Classifiers.Len(), cat.Mcp.Len())

	// Boot loads the platform's own workflows only; the engine re-loads from
	// every imported source as soon as it exists, because the imported list
	// lives in the runtime directory the engine owns.
	defs, err := workflow.LoadDir(cfg.WorkflowsDir, catalog.NewValidator(reg, cat))
	if err != nil {
		log.Fatalf("workflows: %v", err)
	}
	for _, d := range workflow.Sorted(defs) {
		log.Printf("workflow %-16s %d steps  (%s)", d.Name, len(d.Steps), d.Path)
	}

	eng := engine.New(cfg, st, bl, defs, reg, cat)

	// Say what is actually wired before serving, not when a run fails on it.
	// Every registry entry is a NAME and a name resolves against THIS machine
	// (ADR 0011); an unset variable or an uninstalled CLI used to be invisible
	// until a step tried to use it. Key variables are reported by name and by
	// set/unset, never by value.
	if err := eng.ReloadDefinitions(); err != nil {
		log.Fatalf("workflow sources: %v", err)
	}
	for _, src := range workflow.SortedSources(eng.Sources()) {
		if src.Repo != "" {
			log.Printf("  source     %-14s %s", src.Name, src.Repo)
		}
	}
	for _, sk := range eng.SourceSkips() {
		log.Printf("  NOT LOADED %s: %s", sk.Location, sk.Reason)
	}

	logDoctor(eng.Doctor())

	// `on: schedule:` only means something if something ticks.
	eng.StartScheduler(ctx)
	for _, d := range workflow.Sorted(defs) {
		for _, sched := range d.On.Schedule {
			log.Printf("  schedule   %-16s %q", d.Name, sched.Cron)
		}
	}
	srv := &http.Server{Addr: cfg.Addr, Handler: api.New(eng, st, bl, cfg.UIDir), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("wfnexus api on %s  model=%s  llm=%s", cfg.Addr, cfg.Model, cfg.LLMBaseURL)
	log.Fatal(srv.ListenAndServe())
}

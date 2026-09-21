// bug-fixer-platform API: workflow engine on toolnexus, Postgres state, S3 artifacts.
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/api"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/blob"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/catalog"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/config"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/engine"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/skills"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/store"
	"github.com/muthuishere/bug-fixer-platform/apps/api/internal/workflow"
)

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

	defs, err := workflow.LoadDir(cfg.WorkflowsDir, catalog.NewValidator(reg, cat))
	if err != nil {
		log.Fatalf("workflows: %v", err)
	}
	for _, d := range workflow.Sorted(defs) {
		log.Printf("workflow %-16s %d steps  (%s)", d.Name, len(d.Steps), d.Path)
	}

	eng := engine.New(cfg, st, bl, defs, reg, cat)
	srv := &http.Server{Addr: cfg.Addr, Handler: api.New(eng, st, bl, cfg.UIDir), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("bug-fixer-platform api on %s  model=%s  llm=%s", cfg.Addr, cfg.Model, cfg.LLMBaseURL)
	log.Fatal(srv.ListenAndServe())
}

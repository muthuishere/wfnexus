// wfnexus API: workflow engine on toolnexus, Postgres state, S3 artifacts.
package main

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/muthuishere/wfnexus/apps/api/internal/api"
	"github.com/muthuishere/wfnexus/apps/api/internal/assets"
	"github.com/muthuishere/wfnexus/apps/api/internal/blob"
	"github.com/muthuishere/wfnexus/apps/api/internal/catalog"
	"github.com/muthuishere/wfnexus/apps/api/internal/config"
	"github.com/muthuishere/wfnexus/apps/api/internal/engine"
	"github.com/muthuishere/wfnexus/apps/api/internal/remoteuse"
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

// resolveAssets settles where the defaults come from, before anything reads
// them. Disk wins wherever it exists; the copy compiled into this binary is the
// fallback that makes a downloaded `wfx-server` work in an empty directory.
// Every decision is printed, because "which templates is it running?" should be
// readable in the boot log rather than inferred.
func resolveAssets(cfg *config.Config) {
	for _, it := range []struct {
		name     string
		target   *string
		explicit bool
	}{
		{assets.TemplatesName, &cfg.TemplatesDir, cfg.Explicit.Templates},
		{assets.SkillsName, &cfg.SkillsDir, cfg.Explicit.Skills},
		{assets.RegistriesName, &cfg.RegistriesPath, cfg.Explicit.Registries},
	} {
		path, origin, warn, err := assets.Resolve(it.name, *it.target, it.explicit)
		if warn != "" {
			log.Printf("  WARNING %s", warn)
		}
		if err != nil {
			log.Fatalf("%s: %v", it.name, err)
		}
		if origin == assets.Missing {
			log.Printf("  %-10s MISSING — nothing at %s and nothing embedded", it.name, *it.target)
			continue
		}
		*it.target = path
		log.Printf("  %-10s %-8s %s", it.name, origin, path)
	}

	// Workflows are USER DATA and stay on disk — never served from the binary.
	// They only have to exist, so an empty install starts instead of dying on
	// `open ./workflows: no such file or directory`.
	dir, warn, err := assets.EnsureWorkflowsDir(cfg.WorkflowsDir, cfg.Explicit.Workflows)
	if err != nil {
		log.Fatalf("workflows: %v", err)
	}
	if warn != "" {
		// Creating the directory is normal on a fresh install and is reported
		// as a note; a configured path that could NOT be used says so in the
		// same line, because that one is a mistake, not a first boot.
		log.Printf("  %s", warn)
	}
	cfg.WorkflowsDir = dir
	log.Printf("  %-10s %-8s %s", "workflows", "disk", dir)
}

// uiSource picks the bundle to serve: the configured directory if it really
// holds a build, otherwise the one embedded at release time.
func uiSource(cfg config.Config) (string, fs.FS) {
	if cfg.UIDir != "" {
		if _, err := os.Stat(filepath.Join(cfg.UIDir, "index.html")); err == nil {
			log.Printf("  %-10s %-8s %s", "ui", assets.FromDisk, cfg.UIDir)
			return cfg.UIDir, nil
		}
		if cfg.Explicit.UI {
			log.Printf("  WARNING ui: configured %s has no index.html", cfg.UIDir)
		}
	}
	if f, ok := assets.UI(); ok {
		log.Printf("  %-10s %-8s compiled into this binary", "ui", assets.FromEmbedded)
		return "", f
	}
	log.Printf("  %-10s %-8s no bundle on disk and none embedded — / will 404", "ui", assets.Missing)
	return "", nil
}

func main() {
	cfg, cerr := config.LoadWithFile(config.DefaultPath())
	if cerr != nil {
		// A stated-but-wrong setting stops the process. Falling back to the
		// default would run something the operator did not ask for.
		log.Fatalf("config: %v", cerr)
	}
	ctx := context.Background()
	resolveAssets(&cfg)

	// Which database runs is one line of config, exactly like the artifact
	// store below: a laptop gets a SQLite file it can delete, a deployment gets
	// Postgres. The queries are the same either way.
	if err := store.Migrate(cfg.StorageDriver, cfg.DatabaseURL); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	st, err := store.Open(ctx, cfg.StorageDriver, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("%s: %v", cfg.StorageDriver, err)
	}
	log.Printf("storage: %s %s", cfg.StorageDriver, cfg.DatabaseURL)
	defer st.Close()

	// Which artifact store runs is one line of config. A laptop writes files
	// into a folder and needs nothing running; a deployment writes to a bucket
	// because artifacts outlive the machine.
	var bl blob.Store
	switch cfg.ArtifactDriver {
	case "folder":
		bl, err = blob.OpenFolder(cfg.ArtifactDir)
		if err != nil {
			log.Fatalf("artifacts: %v", err)
		}
		log.Printf("artifacts: folder %s", cfg.ArtifactDir)
	case "s3":
		bl, err = blob.Open(ctx, cfg.S3Endpoint, cfg.S3AccessKey, cfg.S3SecretKey, cfg.S3Bucket, cfg.S3UseSSL)
		if err != nil {
			log.Fatalf("s3: %v", err)
		}
		log.Printf("artifacts: s3 %s/%s", cfg.S3Endpoint, cfg.S3Bucket)
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
	//
	// A remote `use:` — a git repository and a ref — resolves HERE, at load
	// time, through git, and is kept in the blob store already in hand. The
	// bundle it fetches carries skills a step will name, and those roots do
	// not exist until the fetch has happened, so the load runs again once with
	// the registry rebuilt over them. The second pass is a cache hit by
	// construction. A workflow whose every `use:` is a bare task name never
	// reaches any of this: no git process, no network, no cache.
	remote := remoteuse.New(bl, engine.BundleRootDirFor(cfg))
	loadOpt := workflow.WithRemoteResolver(remote)
	defs, err := workflow.LoadDir(cfg.WorkflowsDir, catalog.NewValidator(reg, cat), loadOpt)
	if roots := remote.Roots(); len(roots) > 0 {
		reg = skills.Load(append(roots, skills.DefaultRoots(cfg.SkillsDir)...)...)
		defs, err = workflow.LoadDir(cfg.WorkflowsDir, catalog.NewValidator(reg, cat), loadOpt)
	}
	if err != nil {
		log.Fatalf("workflows: %v", err)
	}
	for _, d := range workflow.Sorted(defs) {
		log.Printf("workflow %-16s %d steps  (%s)", d.Name, len(d.Steps), d.Path)
	}

	eng := engine.New(cfg, st, bl, defs, reg, cat)
	eng.UseRemoteResolver(remote)

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
	// Whether this process may serve at all, decided from the bind address
	// before the listener opens.
	if err := bootstrapIdentity(ctx, cfg.Addr, st); err != nil {
		log.Fatal(err)
	}

	uiDir, uiFS := uiSource(cfg)
	srv := &http.Server{Addr: cfg.Addr, Handler: api.New(eng, st, bl, cfg.Addr, uiDir, uiFS), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("wfnexus api on %s  model=%s  llm=%s", cfg.Addr, cfg.Model, cfg.LLMBaseURL)
	log.Fatal(srv.ListenAndServe())
}

// The engine names what it needs of persistence and artifact storage as its
// own interfaces, so that wfx-runner — which opens neither — does not compile
// a Postgres driver and an S3 client into itself. The server is where the two
// halves meet, so this is where the fit is checked: add a method to
// engine.Store without adding it to *store.Store and the SERVER stops
// building, rather than the mismatch surviving to a run.
var (
	_ engine.Store     = (*store.Store)(nil)
	_ engine.Artifacts = (blob.Store)(nil)
)

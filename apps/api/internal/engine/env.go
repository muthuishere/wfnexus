package engine

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/muthuishere/wfnexus/apps/api/internal/model"
	"github.com/muthuishere/wfnexus/apps/api/internal/secrets"
	"github.com/muthuishere/wfnexus/apps/api/internal/workflow"
)

// WHAT A STEP'S ENVIRONMENT ACTUALLY IS
//
//	system  → project → workflow → job → step
//
// Each level only overrides the names it mentions, and the file is the LAST
// word rather than the only one. The two levels above it are the platform's:
//
//	system   what every run on this machine gets — a proxy, a registry, a key
//	project  what one repository's workflows get, because a token that can push
//	         to one repo has no business reaching a workflow from another
//
// Those two are held encrypted (internal/secrets) because they live in the
// database, where a backup or a replica would otherwise hand them over. The
// file's own `env:` is not held anywhere: it is either a literal that was safe
// to commit, or a `${VAR}` naming something read on the machine that runs the
// step.
//
// A stored value is the right answer when the platform must hold the secret —
// a GitHub token the workflow uses on every project. A `${VAR}` reference is
// better wherever it fits, because then the platform never holds it at all.

// Secrets is the box the env store is written and read through. Nil when no
// key could be loaded, which makes the store unavailable rather than plaintext.
func (e *Engine) Secrets() model.Sealer { return e.secrets }

// SecretsSource names where the key came from, for the System page. The NAME —
// an env var or a path — never the key.
func (e *Engine) SecretsSource() string { return e.secretsFrom }

// ErrNoSecretStore is returned when the platform has no key, so nothing can be
// stored or read. Said plainly rather than falling back to plaintext.
var ErrNoSecretStore = fmt.Errorf("the env store is unavailable: %w", secrets.ErrNoKey)

func (e *Engine) requireSecrets() error {
	if e.secrets == nil {
		return ErrNoSecretStore
	}
	return nil
}

// platformEnv is system ∪ project, decrypted. Returned to be layered UNDER
// whatever the workflow file says.
func (e *Engine) platformEnv(ctx context.Context, project string) (map[string]string, error) {
	if e.secrets == nil || e.store == nil {
		// No key, or a worker engine: the platform's own store is not available
		// here, and a step relying on it will fail on the missing variable
		// rather than silently running without it.
		return nil, nil
	}
	sys, err := e.store.EnvFor(ctx, e.secrets, model.ScopeSystem, "")
	if err != nil {
		return nil, err
	}
	if project == "" {
		return sys, nil
	}
	proj, err := e.store.EnvFor(ctx, e.secrets, model.ScopeProject, project)
	if err != nil {
		return nil, err
	}
	return workflow.MergeEnv(sys, proj), nil
}

// PRECEDENCE, WRITTEN DOWN ONCE — later wins, and only for the names it
// mentions. This is the table env_test.go asserts, line for line:
//
//	1 system        stored, sealed    every run on this machine
//	2 project       stored, sealed    every run of one repository
//	3 run           WFX_RUN_ID, WFX_STEP_ID, WFX_WORKSPACE, WFX_PROJECT, and
//	                WFX_MOUNT_<AT> for each mounted folder — facts about THIS
//	                run, which is why they sit below the file: a workflow that
//	                sets one of these names meant it
//	4 workflow      `env:` at the top of the file
//	5 step          `env:` on the step
//
// Workflow-level `env:` is merged into each step at LOAD time (workflow.go's
// normalize, and jobs.go for the job form), so by the time a step is executed
// levels 4 and 5 have already become one map: step.Env. That is deliberate —
// it means exactly one place decides the file's cascade, and the engine layers
// the platform's levels under whatever that produced.
//
// Both kinds of step see the same result. A `run:` step gets it as the
// process environment (nodes.go); an agent step's `bash` tool gets it as
// assignments in front of the command and its CLI provider gets it in the
// child's environment (step.go, provider.go). The identical map, so a workflow
// cannot mean two things depending on which kind of step reads it.

// stepEnv is the whole cascade for one step: the platform's two levels, the
// run's own facts, then everything the file cascaded into the step already.
// workdir may be empty when the step is being PACKED for another machine: the
// workspace is that machine's, so it fills those names in itself.
func (e *Engine) stepEnv(ctx context.Context, runID uuid.UUID, step *workflow.Step, workdir string) (map[string]string, error) {
	base, err := e.platformEnv(ctx, e.projectOfRun(ctx, runID))
	if err != nil {
		return nil, err
	}
	run := e.mountEnv(runID)
	if workdir != "" {
		run = workflow.MergeEnv(run, RunEnv(runID.String(), step.ID, e.projectOfRun(ctx, runID), workdir))
	}
	return workflow.MergeEnv(workflow.MergeEnv(base, run), step.Env), nil
}

// RunEnv are the facts about this run that a step can read without being told
// them. The worker already exported exactly these names for a `run:` step; a
// local step did not, which meant a script that worked on a worker read an
// empty WFX_WORKSPACE here. One definition now, used in both places.
func RunEnv(runID, stepID, project, workdir string) map[string]string {
	return map[string]string{
		"WFX_RUN_ID":    runID,
		"WFX_STEP_ID":   stepID,
		"WFX_PROJECT":   project,
		"WFX_WORKSPACE": workdir,
	}
}

// projectOfRun is which repository this run belongs to, for the project scope.
func (e *Engine) projectOfRun(ctx context.Context, runID uuid.UUID) string {
	if e.store == nil || runID == uuid.Nil {
		return ""
	}
	run, err := e.store.GetRun(ctx, runID)
	if err != nil {
		return ""
	}
	return run.Project
}

// ---- managing the store ----

// SetEnvVar stores one entry. Refused without a key, rather than written in
// the clear.
func (e *Engine) SetEnvVar(ctx context.Context, scope, scopeName, key, value string, secret bool) error {
	if err := e.requireSecrets(); err != nil {
		return err
	}
	if key == "" {
		return fmt.Errorf("a variable needs a name")
	}
	if scope == model.ScopeProject && scopeName == "" {
		return fmt.Errorf("which project?")
	}
	if scope != model.ScopeSystem && scope != model.ScopeProject {
		return fmt.Errorf("scope must be %q or %q", model.ScopeSystem, model.ScopeProject)
	}
	return e.store.PutEnvVar(ctx, e.secrets, scope, scopeName, key, value, secret)
}

func (e *Engine) DeleteEnvVar(ctx context.Context, scope, scopeName, key string) error {
	if e.store == nil {
		return ErrNoSecretStore
	}
	return e.store.DeleteEnvVar(ctx, scope, scopeName, key)
}

// ListEnvVars is for display: a secret comes back as a name and a timestamp.
func (e *Engine) ListEnvVars(ctx context.Context, scope, scopeName string) ([]model.EnvVar, error) {
	if err := e.requireSecrets(); err != nil {
		return nil, err
	}
	return e.store.ListEnvVars(ctx, e.secrets, scope, scopeName)
}

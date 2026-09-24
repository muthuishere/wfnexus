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

// stepEnv is the whole cascade for one step: the platform's two levels, then
// everything the file cascaded into the step already.
func (e *Engine) stepEnv(ctx context.Context, runID uuid.UUID, step *workflow.Step) (map[string]string, error) {
	base, err := e.platformEnv(ctx, e.projectOfRun(ctx, runID))
	if err != nil {
		return nil, err
	}
	return workflow.MergeEnv(base, step.Env), nil
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

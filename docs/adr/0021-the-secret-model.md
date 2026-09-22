# ADR 0021 — The secret model

- **Status:** accepted
- **Date:** 2026-09-22

## Context

`registries.json` names a provider's credential rather than holding it:

```json
"apiKeyEnv": "OPENROUTER_API_KEY"
```

That property is right and it is load-bearing. A workflow file and a registry
file can be committed, reviewed, published and pasted into a bug report, because
the only thing in them is the *name* of a variable. `config.Load` says the same
thing in a comment: the name, never the value, so a key cannot reach config,
logs or an event.

What it does not solve:

- **A provider is machine-local.** The name resolves against the environment of
  whichever process happens to run the step, so adding a provider means a
  redeploy, and rotating a key means a restart of every worker. Under ADR 0019
  there are N workers and the environment must be identical on all of them.
- **There is one scope: the process.** Every step of every run sees every
  variable the server was started with. A step that needs a model key also has
  the git token, the database URL and whatever else the operator's shell
  exported.
- **Nothing records what was used.** A leaked key cannot be traced to the runs
  that could have leaked it.

## Decision

**Providers move into Postgres, and the indirection moves with them: a provider
row stores a secret *name*, never a value.**

- A `secrets` table holds `(tenant_id, name, ciphertext)`; values are encrypted
  at rest with a key the process gets from its environment — the one secret that
  remains environmental, because something has to be.
- **The API is write-only for values.** A secret can be created, replaced and
  deleted; it can never be read back through the API, by any scope, including
  `admin`. The only thing that decrypts a secret is the code path that injects
  it into a step.
- **Injection is by name and by need.** The runner (ADR 0015) receives the
  resolved values in its environment, and receives **only the secrets the step's
  providers and MCP servers actually name**. A step that uses `haiku` does not
  get the git token. This is ADR 0004's scoping argument applied to credentials:
  the step sees what its YAML lists and nothing else.
- **Values never enter an event, a log, an artefact or a prompt.** The existing
  `scrub` on step output stays as the last line of defence, and it is a last
  line — the design is that a value has no path to those places, not that we
  filter it out of them.
- **Environment names keep working.** A provider may name either a secret in the
  store or an environment variable, and the local-development path stays exactly
  as it is today. A file that names `apiKeyEnv` is still portable and still
  committable; that is the property being preserved, not replaced.

## Consequences

- **Rotation becomes an API call rather than a redeploy.** This is the concrete
  reason to do it, and under ADR 0019 it is the difference between rotating a
  key and restarting a fleet.
- Encryption moves the problem rather than solving it: the master key is in the
  environment of every worker. That is a real and deliberate limit — it makes
  rotation and scoping possible, which is the actual goal, and it is one secret
  to protect instead of a dozen.
- A secret is tenant-scoped (ADR 0020), so two tenants can both have a provider
  called `sonnet` with different keys, and neither can name the other's.
- The audit trail comes for free: a run records which provider it resolved, and
  a provider names its secret, so "which runs could have touched this key" is a
  query.
- Test and CI paths must never populate a real value. The rule already in force
  everywhere else applies here too: an example, a fixture or a doc uses an
  obvious fake, never a live value.

# ADR 0018 — The registry has a publish direction

- **Status:** proposed — **the storage half superseded 2026-09-25**, see
  [Superseded in part](#superseded-in-part-2026-09-25) at the end. The decision text
  below is left as it was written.
- **Date:** 2026-09-24

## Context

Everything this server knows, it read off a disk at boot. `skills.DefaultRoots`
walks three roots — the project's `skills/` (10 shipped here), `~/.claude/skills`
and `~/.agents/skills`; `catalog` loads `registries.json` (7 providers, 4
classifiers) with `mcp.json` merged (0 servers as committed); `templates/` holds
one, `bug-fixer.yaml`. There is no way to put an asset *into* any of them from
somewhere else.

ADR 0011 already decided the receiving end: **one registry type for everything a
step names**, with `Get`/`Has`/`List`/`Names`/`Missing`/`Require` defined once
and every name validated at load time. It said workflows are portable because
*names travel, endpoints and credentials do not*. That sentence describes a
transport that was never built. This ADR completes 0011 rather than adding a
concept beside it.

The pivot ([`research/the-pivot-2026-09-24.md`](../research/the-pivot-2026-09-24.md)
§3) is what makes the gap urgent. Nobody writes the YAML: the author works inside
their own agent — Claude Code, Codex, opencode — and the workflow is the receipt
it produces. A receipt that can only be written to the disk the server already
boots from is a single-machine artefact, and "build once in Claude, run it
against Codex on your own machine" is then a claim with no verb behind it.

The pull half already exists and sets the standard. `wfx install --skills`
(`cmd/wfx/skillinstall.go`) copies skill directories into *both* agent roots and
prints each root and a count, because — its own comment — *a tool that writes
into another tool's configuration behind your back is a tool nobody can audit*.
It is explicit for the same reason `playwright install` is.

Unoccupied ground: npm and OCI registries hold code and images; `npx skills add`
is a *public* installer with no private registry and no runtime. A self-hostable
registry for agent assets **with a typed runtime attached** is what nobody has.

## Decision

**One verb: `wfx publish <kind> <path>`.** Kinds are the registry kinds ADR 0011
already names — `skill`, `mcp`, `workflow`, `template`, `provider`. Assets are
namespaced by project (projects exist in the schema) and versioned. The command
prints what it sent and where, exactly as `install` prints what it wrote.

**A published workflow is a bundle, not a set of references.** The skills it
names and its MCP server declarations are resolved at publish time and travel
with it, addressed by content digest the way an OCI image manifest addresses its
layers. The argument is the portability claim itself: a workflow whose step says
`skills: [fix-author]` fails at *load* time on a machine that does not have that
skill (ADR 0011 made that a boot error on purpose), so references resolved at
pull time turn "portable" into "portable if the destination already agreed". They
also make a workflow silently mean something different on two machines, because
skills are discovered across roots and the first root wins. The cost of bundling
is duplication and staleness — the honest price, and the one npm pays with a
lockfile. A bundle records the digest of each skill it carries, so a later pull
can say *this bundle carries fix-author@sha256:… and you have a different one*.

**Publishing is immutable, and versions are semver.** Publishing `name@version`
twice is refused, not overwritten — npm's rule, for npm's reason. A moving tag
(`latest`) may point at a version; a version never moves. This is the opposite of
registry *loading*, where first name wins and a duplicate is recorded in `Skips`,
and deliberately so: shadowing is tolerable on one machine, invisible mutation
across machines is not.

**A credential is never published.** `apiKeyEnv` is the NAME of an environment
variable, and this is already enforced on save: `catalog.SaveProvider` and
`SaveClassifier` refuse when `looksLikeSecret(APIKeyEnv)` — anything over 64
characters or containing a byte outside `[A-Za-z0-9_]`. `workflow/env.go` applies
the same rule to step env. Publish reuses those checks unchanged; the wire
carries names, and a provider entry that names a variable the destination has not
set fails loudly there rather than arriving with a key in it.

## The three-scale test

- **One person: no registry server at all.** Files on disk work exactly as they
  do today. Publishing is *absent*, not merely unused — the server boots and runs
  with no registry configured, and `wfx publish` without a host is an error, not a
  degraded mode.
- **A small org: one server.** A shared set of skills and workflows, `wfx publish`
  from a laptop against `wfx login --url …`. The server is still the same binary.
- **An enterprise: air-gapped, their own registry.** Every asset records who
  published it and when — provenance, in git's sense of an author, not a
  signature. An org-controlled skill registry is *itself a security control*: it
  is the answer to "what SKILL.md is my agent reading", which today is whatever
  landed in `~/.claude/skills`. Vetting what an agent may load becomes a property
  of the registry rather than of each laptop.

## Consequences

- The registry stops being a boot-time read and becomes a store with a lifetime.
  `Skips` and first-name-wins remain the *local* resolution rule; they are not the
  publish rule.
- `install` and `publish` become one pair with one property: each says what it
  moved and where. Neither modifies another tool's config silently.
- Bundling means a workflow can be pulled onto a machine that has never seen its
  skills, which is the whole of "a unit that travels".

## Not decided here

Named as open, not solved: the **storage layout** on the server (filesystem,
object store, or the existing DB); **garbage collection** of unreferenced blobs;
**signing and verification** of a bundle's provenance beyond a recorded
publisher; and **dependency resolution between assets** — what happens when two
bundles carry different versions of the same skill. Each wants its own ADR, and
none of them changes the shape of the verb.

## Superseded in part (2026-09-25)

**What is superseded: WHERE a bundle lands, and nothing about what a bundle is.**
The decision above is kept verbatim rather than edited, because the reasoning for a
blob store is still the reasoning a reader needs — it is what makes the replacement
legible as a change of mind rather than a change of text.

The replacement is *a git remote is the registry* (`openspec/changes/a-git-remote-is-the-registry/`).
`wfx publish --to <git remote>` writes the bundle as a **committed tree** under
`workflows/<name>/<version>/` — the same manifest, the same fixed paths — commits it,
tags `<name>/v<version>` and pushes. Actions, Go modules and Terraform all resolve a
repository plus a ref and none of them runs an artifact server; the repo's own rule is
ADOPT, NEVER INVENT.

Stands, unchanged:

- **A published workflow is a bundle, not a set of references.** Skills and MCP
  declarations still resolve at publish time and travel, addressed by digest.
- **Publishing is immutable.** Now enforced by git: a tag that already exists is a push
  that fails, which is the same refusal `unique (project, kind, name, version)` gives.
- **A credential is never published.** `looksLikeSecret` and the step-env rule run
  before a commit is made, not after.
- **One verb, and it says what it moved and where.**

Superseded:

- **The storage layout, which this ADR left open, is answered — and answered differently.**
  Not "filesystem, object store, or the existing DB" but *a git repository*, because a
  committed **tree** is reviewable in a PR and a committed tarball is one binary blob
  changed. The pivot's whole argument for the YAML is that it is a receipt: readable,
  diffable, reviewable by someone who never touched the generator. A tar defeats every
  word of that. The tarball survives as a **derived** artifact — `bundle.Pack` over the
  tree reproduces it, because entry digests are over canonical content and not over tar
  ordering.
- **The premise that a publish targets *our* server.** It no longer has to. `wfx login`
  stays for the host sink and for running; it stops being a precondition of sharing a
  workflow. The host path is retained as a mirror — dropping a working feature to make a
  point about registries would be the worse trade — but `internal/blob` is demoted from
  *where bundles live* to *where fetched bundles are kept*, which is safe only because
  the content is addressed by digest. The test for the demotion is literal: `rm -rf` the
  cache, rerun, get the same digests.
- **"A small org: one server."** A small org needs a repository they already have, with
  the access control they already administer and the PR review they already require. No
  wfnexus server is required to share a workflow. A server is where runs happen.
- **`published_by` as *the* provenance mechanism.** This ADR wanted provenance "in git's
  sense of an author"; a commit **is** that, with an author, a committer, a date, a
  message and a parent, and a signed tag is the tamper-evidence this ADR said it could
  not claim. The column is kept and demoted to a cached local attribution, and the index
  row now also records the remote and the resolved **commit** it was read from
  (`git_remote`, `git_commit`, migration `000013`) so the two views reconcile. Nothing
  is deleted: a bundle uploaded straight to a host has no commit, and the recorded
  publisher is all there is for it.
- **Garbage collection of unreferenced blobs**, named open here, stops being a
  correctness problem. Evicting from a cache is always safe, so it is an eviction
  policy.

Still open, and not closed by the replacement: **signing and verification** (git can
sign a commit and a tag; whether we *verify* one is not decided — see
`docs/not-now.md`), and **dependency resolution between assets**.

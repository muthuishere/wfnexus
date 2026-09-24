# Design — a git remote is the registry

## Context

`publish-from-your-own-agent` is implemented. `wfx publish <file.yaml> --version <semver>`
(`apps/api/cmd/wfx/publish.go`) resolves every skill and MCP declaration against the publishing
machine's roots, builds a manifest (`bundle/manifest.go:46-57` — `schemaVersion`, kind, project,
name, version, and an `Entry{name,path,digest,files}` per carried dependency), packs a gzipped tar
(`bundle/pack.go`), and uploads it. The server recomputes every digest, refuses a mismatch, and
indexes the result (`api/bundles.go`, `store/bundles.go`, `migrations/000011_published_bundles`).
The bytes land in `internal/blob` under `bundles/<hex>/bundle.tar.gz` and
`bundles/<hex>/manifest.json` (`bundle/blobstore.go:18-19`). None of that is being thrown away.

What it costs: a publish requires a host and a token. `publish.go:41-42` refuses with *"no host: run
`wfx login --url <host>` first"*. To hand a colleague a workflow you must first run a server and
give them an account on it. That is a registry we built.

The repo's own rule is ADOPT, NEVER INVENT (`openspec/config.yaml`), and the thing to adopt is
sitting in the vocabulary already borrowed. **GitHub Actions has no registry.** `uses:
actions/checkout@v4` is a git repository plus a ref, fetched at run time; the Marketplace is a search
index over git tags, not a store. **Go modules** are the same shape: `go install
github.com/x/y@v1.2.3` is a repo and a tag, and `proxy.golang.org` is a cache in front of git that
`GOPRIVATE`/`GOFLAGS=-mod=mod` routes around. **Terraform** takes `source =
"git::ssh://git@host/x.git?ref=v1.2.0"`. Three ecosystems with our exact problem; none solved it with
an artifact server.

And the verb is already here. `Use` (`workflow/task.go:32-47`) has a `Task string` field with the
YAML key `use` (`:34`); `Definition.Uses` is a field (`workflow/workflow.go:329`); `expandUses`
(`task.go:117`) looks the name up in a `map[string]*Task` loaded by `LoadTasks` from one local
directory. `use:` today means *a file next to me*. This change finishes it.

Reading of the ADRs this touches:

- **ADR 0011** is unaffected. One registry type, names validated at load time, names travel and
  endpoints and credentials do not. A remote `use:` is resolved *before* validation, so what the
  registry sees is unchanged.
- **ADR 0018** is partially superseded. §"A published workflow is a bundle, not a set of references",
  §"Publishing is immutable", §"A credential is never published" all stand verbatim. The parts that
  do not: the premise that a publish targets *our* server (`wfx publish` against `wfx login --url`),
  the three-scale line *"A small org: one server"*, the storage-layout open question (it is answered
  here, differently), and `published_by` as *the* provenance mechanism.

## Goals / Non-Goals

**Goals:**

- A `use:` reference that names a git repository and a ref, in a grammar borrowed from Actions and
  Go rather than coined.
- A published-bundle layout in a git repo that a human can review in a PR diff.
- A fetch-and-cache path on top of the existing `blob.Store`, and an explicit offline path.
- A pin recorded per run, so a moving tag does not make a run unreproducible.
- One honest account of what already-working code changes.

**Non-Goals:** signing and verification policy; transitive `use:` (a fetched bundle that itself
declares a remote `use:`); rate limits and fetch quotas; cache invalidation on a moved tag; a public
index or search; dropping the server publish path; git LFS; submodules.

## Decisions

### 1. A published bundle is a committed tree, with the tarball derived

**Decision: commit the tree, not the tarball.** One bundle is one directory in the repo:

```
workflows/<name>/<version>/
    manifest.json          # bundle.Manifest, canonical bytes — the digest is over THIS file
    workflow.yaml          # the workflow as published
    mcp.json               # the MCP declarations its steps name (absent if none)
    skills/<skill>/…       # each skill directory, verbatim
```

The paths are the ones `bundle/manifest.go:27-32` already fixes (`ManifestPath`, `WorkflowPath`,
`McpPath`, `SkillsDir`) — the tar member names become file names under a version directory, so one
manifest describes both forms and no second format exists.

**Argued from the reviewer.** The whole of `the-pivot-2026-09-24.md` §2.1 is that nobody writes the
YAML and the YAML is therefore a *receipt*: "readable, diffable, committable, reviewable by someone
who never touched the generator". A committed tar defeats every word of that — a PR that bumps a
workflow shows one binary blob changed. A committed tree shows the prompt that changed, the skill
that was added, the MCP server that appeared. Vetting what an agent may load (ADR 0018's enterprise
argument) becomes `git diff` plus the org's existing CODEOWNERS, which is a control they already run.
Actions makes the same call: an action is a repo you can read, not an artifact you download.

The tarball is not lost — it is *derived*. `bundle.Pack` over the tree reproduces the same bytes,
because entry digests are defined over canonical content, not over tar ordering, mtimes or
permissions (`bundle/manifest.go`, proven by the existing round-trip test in `bundle_test.go`). So
the mirror upload (decision 4) and the cache (decision 3) keep working on the same artifact.

The version directory, not a branch per version, because a repo holding many workflows is the normal
case (`acme/workflows`), and one tag over the whole repo then publishes a coherent set — the Actions
arrangement.

*Unverified:* that `bundle.Pack` output is byte-identical across Go versions and filesystems; the
round-trip test asserts digest equality, not tar-byte equality. The pin (decision 3) is on the
manifest digest, not the tar bytes, so this does not have to hold.

### 2. Reference grammar — Actions' short form over Go's long form

```yaml
uses:
  - use: reproduce-bug                              # (a) local task — UNCHANGED, still works
  - use: acme/bug-fix@v1.2.0                        # (b) github.com, Actions' form
  - use: git.acme.internal/team/wf@v1.2.0           # (c) any host, Go's module-path form
  - use: ssh://git@nas.lan/srv/wf.git@a1b2c3d       # (d) an explicit git URL and a ref
```

**Two segments and no dots in the first ⇒ GitHub.** That is exactly Actions' rule
(`actions/checkout` is github.com/actions/checkout) and exactly why Go requires a dot in the first
path element of a module path — the dot is what distinguishes a host from a namespace. Adopting both
rules at once means (b) and (c) disambiguate without a scheme, a prefix, or a flag.

- **(a) survives untouched.** No dot, no slash, no `@` ⇒ a local task name, resolved by `expandUses`
  against `LoadTasks` exactly as today. A repo that never uses a remote sees no change at all.
- **(d) is the escape hatch**: anything with a scheme (`ssh://`, `https://`, `file://`) is handed to
  git verbatim as the remote, with the text after the final `@` as the ref. A bare repo on a NAS, a
  gitolite host, a path on a mounted share. Terraform's `git::` source is the precedent; we do not
  need its prefix because a scheme is already unambiguous.
- **GitHub Enterprise is case (c)**, not a setting: `ghe.acme.com/team/wf@v1.2.0`. There is no
  configurable "default host", because a default host is how a tool ends up meaning github.com.
- **`@ref` is a tag, a branch or a commit SHA** — git's own vocabulary, resolved by git. A 40- or
  7-to-40-hex ref is a commit; anything else is resolved through `git ls-remote` and recorded
  (decision 3). Actions accepts all three in the same position; so do we.
- **Bundle selection inside the repo.** `acme/bug-fix@v1.2.0` means *the repo `acme/bug-fix`, ref
  `v1.2.0`*; which bundle inside it is the workflow named at `workflows/<name>/<version>/`. When the
  repo holds exactly one, the ref alone is enough. When it holds many, a `#` fragment selects —
  `acme/workflows@v1.2.0#bug-fix` — Go's `/subpackage` position being unavailable to us because our
  first two segments are already spoken for.

Rejected: coining `wfx://`, a `registry:` block in the YAML, and an `owner/repo` form with a
configurable default host. Each one re-invents what a URL already says.

### 3. Resolution, caching, pinning, and the offline path

**When.** At **load** time, not run time, and not step time. The repo's rule is that a typo is a boot
error naming what is known (ADR 0011), and `expandUses` already runs before validation
(`task.go:117`, called before the step checks) precisely so expanded steps are checked like
hand-written ones. A remote `use:` resolves in the same place: fetch, verify, materialise the task's
steps, then validate. A step that discovered its dependencies mid-run would reintroduce the failure
mode ADR 0018 exists to remove.

**Where.** `internal/blob`, unchanged code. The keys are already content-addressed
(`bundle/blobstore.go:18-19`) and `Folder.pathFor` (`blob/folder.go:45-56`) already refuses a key
that escapes the directory — hex digests cannot. One addition, a ref→digest index, because a cache
needs a name to look up as well as a digest:

```
bundles/<hex>/bundle.tar.gz      # existing
bundles/<hex>/manifest.json      # existing
refs/<remote-hash>/<ref>         # new: the resolved commit SHA + manifest digest for a ref
```

The blob store stops being *the place bundles live* and becomes *the place fetched bundles are
kept*. That demotion is only safe because the content is addressed by digest: a cache that can be
deleted at any time without changing what a workflow means is a cache; one that cannot is a store.
The test for the demotion is therefore literal — **`rm -rf` the cache, rerun, get the same digests**.

**Pinning: the ref is what was written, the commit is what ran.** A tag can move, a commit cannot —
the reason Actions' own hardening guidance is to pin third-party actions by SHA. On resolution we
record, on the run row and in the run's event stream: the reference text as authored, the remote
URL, the resolved **commit SHA**, and the **manifest digest**. A rerun of a recorded run resolves
from the commit, never from the ref, so a moved tag cannot change a rerun. A first run of a workflow
authored with a mutable ref is reproducible *after the fact*, not before — which is true of Actions
too, and is the honest statement rather than a claim of hermeticity.

**Offline / air-gapped, explicitly.** A host with no github.com has three paths, in order:

1. **The cache is a hit** ⇒ nothing is fetched, no network call is attempted. A digest-pinned `use:`
   whose manifest digest is in the cache resolves entirely locally. This is the normal steady state.
2. **A mirror remote.** `git` already solves this with `insteadOf`: an org points
   `url."ssh://git.internal/mirror/".insteadOf = "https://github.com/"` in its own git config and
   every reference in every workflow retargets with no workflow edit. We adopt it by *using git as
   the transport* rather than an HTTP client of our own — that is a large part of the argument for
   this change. There is no wfnexus-side mirror setting to get wrong.
3. **A sideload.** `wfx bundle import <dir|tar>` writes a bundle into the cache under its own
   recomputed digest, with the same server-side verification the upload path already does
   (`api/bundles.go` recomputes every entry digest and refuses a mismatch). Copy a directory onto a
   USB stick, import it, pin by digest. Nothing about publish or run reaches a network.

And the refusal that makes the mode real: with the cache cold and the remote unreachable, a remote
`use:` is a **load error naming the reference and the remote** — never a silent fall back to a local
task of the same name, and never a degraded run. Absent, not permissive.

### 4. What happens to `wfx publish` — a second sink, not a replacement

**Decision: `wfx publish --to <git remote>` is added; the host path is retained as a mirror; neither
supersedes the other. Honest accounting of what changes in working code:**

| today | after |
|---|---|
| `publish.go` resolves skills/MCP on the publishing machine | **unchanged** — this is the whole ADR 0018 bundle decision |
| `bundle/build.go`, `manifest.go`, `pack.go` | **unchanged**; `pack.go` gains a *tree* writer beside the tar writer, same manifest |
| `looksLikeSecret` refusal (`catalog/save.go:85`), unresolvable-reference refusal, digest refusal | **unchanged, and now run before a commit is made** |
| `publish.go:41-42` — no token ⇒ refuse | **narrowed**: that refusal now applies only when the sink is a host. With `--to`, no token is needed and none is asked for |
| `api/bundles.go`, `store/bundles.go`, `000011_published_bundles` | **retained**, reframed: the server-side index is a mirror and a query surface (who published what, when, which digest), not the only home |
| `published_bundles.published_by` | **retained but demoted** — see decision 5 |

The publish becomes: build the bundle → run every refusal → write the tree under
`workflows/<name>/<version>/` in a clone of the remote → commit → tag `<name>/v<version>` (Actions'
and Go's monorepo tag convention) → push. **Immutability is git's**: a tag that already exists is a
push that fails, which is the same refusal `unique (project, kind, name, version)` gives today, now
enforced by the thing everyone already trusts to enforce it.

Not superseded, because dropping the server path would break a working feature to make a point, and
because the two answer different questions: git answers *where does this live and who approved it*,
the server index answers *what is running here right now*. A server that has both can serve a run
from cache without any remote at all.

### 5. Auth and provenance are git's

Publishing to a remote uses whatever git uses: the SSH agent, the credential helper, the PAT already
in `~/.git-credentials`. **wfnexus never reads, stores, prompts for or transports a git credential**
— it shells out to git and git does what it does for every other push that user makes. That is
consistent with the secrets rule in `openspec/config.yaml` by construction rather than by care.

What this removes from our surface, concretely:

- **No account with us to publish.** The `wfx login` device grant (RFC 8628) stays for the server
  path and for running; it stops being a precondition of sharing a workflow.
- **No access-control model for bundles.** Who may publish `acme/workflows` is a repo permission the
  org already administers. We do not gain a second, weaker copy of it.
- **Provenance stops being a column.** ADR 0018 wanted `published_by`/`published_at` "in git's sense
  of an author" — a commit *is* that, with an author, a committer, a date, a message and a parent,
  and a signed tag is the tamper-evidence ADR 0018 explicitly could not claim. `published_by` on the
  mirror row becomes a cached copy of the commit author, not the record of record.
- **No blob GC problem for published content.** ADR 0018 named GC as open. A cache does not need GC
  in the same sense: evicting is always safe, so it is an eviction policy, not a correctness problem.

What we do **not** get for free and must not claim: signature *verification* (decision below — we
record what git reports and enforce nothing), and any guarantee that a repo's history was not
rewritten. A force-pushed tag is exactly the moved tag the pin defends against.

### 6. The three-scale test

**One person: no remote at all, and the capability is ABSENT.** `use: reproduce-bug` resolves
against local task files exactly as it does today (`task.go:117-133`); no git call, no network, no
cache directory created. A workflow with no remote `use:` never touches any of this code. `wfx
publish --to` without a remote argument is an error, not a local-only mode that writes into
`skills/`. The one-person path is the path that exists today, untouched — which is the strongest
form of this test.

**A small org: a repo they already have.** `acme/workflows`, private, on the GitHub org they are
already paying for. Access control is the repo's; review is the PR they already require; publishing
is a push from a laptop whose SSH key is already trusted. **No wfnexus server is required to share a
workflow** — that is the change. A server, if they run one, is where runs happen, not where
distribution lives.

**An enterprise: their own host, air-gapped, pinned, vetted.** GitHub Enterprise or a bare repo is
case (c)/(d) of the grammar, not a configuration. Air-gapped is decision 3's three paths, with
`insteadOf` doing the retargeting in the org's own git config. Pinned by commit SHA, recorded per
run. Vetting what a workflow loads is a `git diff` over a readable tree plus CODEOWNERS — the reason
decision 1 commits the tree rather than the tarball. Provenance is a signed tag by an identity their
CA already issued, not a row in our database.

### 7. The manifest declares what the bundle needs to RUN, and a pull may refuse

A bundle carries the workflow, its skills and its MCP declarations. It does not carry the
*execution requirements* its steps imply, so a bundle can land on a host that can never run it and
nothing says so until a run fails mid-flight — after the run row, the worktree and the first tokens.

**Decision: the manifest records, gathered at publish time from the steps themselves:**

| recorded | why it cannot be inferred later |
|---|---|
| each step's `provider:` and its KIND (http / cli / acp) | an `http` provider needs a key; a `cli`/`acp` provider needs a **binary on that machine**. The receiving host cannot tell which from the name alone |
| the `runs-on:` labels any step asks for | a label nobody holds means the step waits forever, which `wfx dryrun` already reports locally and a pull cannot |
| the MCP servers named | same class: an allowlist entry that does not exist here |

`wfx pull` checks that manifest against the receiving host and **refuses, naming exactly what is
absent**. This is the rule this change already applies at publish time — refuse here rather than at
run time — pointed the other way at the receiving end, and it reuses the same vocabulary so the two
refusals read alike.

**The constraint that shapes it: the platform can never hold a CLI's credential.** `provider: devin`
means *that machine's* Devin seat; `claude-cli` means *that person's* subscription. That is the
bargain a self-hosted Actions runner and a Jenkins node both make, and it is what makes a licence
dongle, an SSO-bound seat or an air-gapped model usable at all. So the manifest records a
**requirement, never a credential**, and the host's answer is "I have it / I do not" — never "let me
log in for you".

**A satisfied requirement is still not authentication.** `engine/doctor.go` `checkProvider` reports
a `cli`/`acp` provider ready on a PATH lookup alone — its own comment says "no key, because the CLI
holds its own credential". So a green tick means the binary exists, not that anyone is logged into
it, and a pull that passes can still fail on turn one with an auth error. Distinguishing *present*
from *authenticated* is real work, it is **not decided here**, and it is listed below.

## Risks / Trade-offs

- **Git becomes a runtime dependency of load** → shell out to `git`, require it only when a remote
  `use:` is present, and state the version floor. A workflow with no remote `use:` never calls it.
  *Unverified:* whether to use a pure-Go implementation instead; not investigated, and shelling out
  matches `insteadOf`/credential-helper behaviour for free, which a library would have to re-implement.
- **A mutable ref makes a first run unreproducible** → the pin is recorded on resolution, so the
  *rerun* is exact. Documented as after-the-fact reproducibility, not hermeticity. Actions has the
  same property.
- **A rewritten history or a force-pushed tag changes what a ref means** → the recorded commit SHA
  detects it; nothing prevents it. Stated, not defended.
- **Cloning a large repo to fetch one small bundle** → partial clone (`--filter=blob:none`) plus
  sparse checkout of the one version directory. *Unverified:* not measured; the fallback is a full
  shallow clone and the cache means it happens once per ref.
- **Two publish sinks is two code paths** → mitigated by sharing everything above the sink: one
  bundle builder, one manifest, one set of refusals, two writers.
- **A committed tree duplicates skill content across versions** → real, and the same price ADR 0018
  already accepted for bundling; git's own delta compression absorbs most of it since versions of a
  skill differ slightly.
- **The cache is a second place a bundle can be stale** → content addressing means a stale entry is
  never *wrong*, only old. Whether a moved tag must evict is explicitly not decided (below).

## Not decided here

- **Present vs authenticated for a CLI provider.** A PATH lookup says the binary is there; nothing
  says anyone is logged into it, so a pull can pass and the first turn can still fail on auth. Each
  CLI would need its own cheap probe (a `whoami`, a `--version` that differs when logged out), which
  is per-vendor work and changes when they change.

Named as open, not solved:

- **Signing and verification policy.** Git can sign a commit and a tag; whether wfnexus *verifies*
  one, which trust roots it would consult, and whether an unsigned bundle is refused, warned about or
  accepted, is not decided. This change records what git reports and enforces nothing. ADR 0018
  already deferred signing; it stays deferred, with a better mechanism available.
- **Transitive `use:`.** A fetched bundle whose own workflow declares a remote `use:`. Depth,
  cycles, and whether a transitive dependency is carried at publish time (which would make a bundle
  self-contained again, at a cost) or resolved on fetch — none of it is decided. This change resolves
  one level and **refuses** a nested remote `use:` with a message saying so, rather than resolving it
  half-way.
- **Rate limits and fetch quotas.** A load that resolves several remotes hits a host's API limits;
  what the backoff is, whether resolutions are batched, and what a shared runner does when throttled
  are unanswered. The cache makes this rare rather than solved.
- **Whether a moved tag invalidates a cache entry.** A cached `@v1` that points at an older commit
  than the remote's `v1` today is stale but not wrong. Whether to re-resolve on every load, on a TTL,
  or never; and whether a detected move is a warning, an error or silent, is open. Go's module cache
  never re-resolves a version; Actions re-resolves every run. We have not chosen.

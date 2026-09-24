## Why

`publish-from-your-own-agent` shipped the publish direction against **our** server: `wfx publish`
builds a bundle (`apps/api/internal/bundle/`), uploads it (`apps/api/internal/api/bundles.go`),
indexes it (`apps/api/internal/store/bundles.go`, migration `000011_published_bundles`) and stores
the bytes in `internal/blob`. That works. What it also does is make a workflow's distribution
depend on a wfnexus host: to share a workflow you must run a server, hold an account on it, and
`wfx login` before `wfx publish` will do anything (`apps/api/cmd/wfx/publish.go:40-45` refuses
without a token).

That is a registry we invented. The convention this repo adopted says not to. **GitHub Actions —
whose `uses:` vocabulary this repo already borrowed — has no registry**: `uses: actions/checkout@v4`
is a git repo plus a ref, resolved at run time. Go modules are the same (`github.com/x/y@v1.2.3` —
repo and tag; the proxy is a cache, not the source of truth). Terraform modules likewise take a git
source. Every one of them has the distribution problem we have and none of them solved it by
standing up an artifact server.

We are one step from that shape and did not notice: `Use` already exists
(`apps/api/internal/workflow/task.go:31-47`), `Definition.Uses` is already a field
(`workflow/workflow.go:329`), and `expandUses` (`task.go:123-133`) already resolves it — but only
against a map of tasks loaded from a local directory, so `use:` means "a file next to me". Finishing
it is the change:

```yaml
uses:
  - use: acme/bug-fix@v1.2.0
```

## What Changes

- **`use:` accepts a remote reference**, not only a local task name. Grammar adopted from Actions
  and Go, not coined: `owner/repo@ref` for github.com, and a full module-style path
  (`git.acme.internal/team/workflows@v1.2.0`, `ssh://git@nas.lan/srv/wf.git@<sha>`) for everything
  else, because "host it wherever you like" cannot mean github.com only.
- **A published bundle becomes a committed tree in a git repository**, not an opaque tarball in our
  blob store. The manifest and the workflow are readable files in a PR diff; the skills are
  directories. The receipt claim (`docs/research/the-pivot-2026-09-24.md` §2.1) only holds if a
  reviewer can read what changed.
- **`internal/blob` becomes the fetch cache, not the mechanism.** Same content-addressed keys, same
  digests, same code — demoted from the place bundles live to the place they are kept after a
  fetch. Content addressing is what makes that demotion safe.
- **`wfx publish` gains `--to <git remote>`** and keeps its existing server path as a mirror. The
  bundle builder, the digest rules and every publish-time refusal are reused unchanged; only the
  sink changes.
- **Auth and provenance become git's.** Publishing to a remote needs the SSH key or token the org
  already manages — no account with us. A commit, a tag and (if the org signs) a signature are the
  provenance ADR 0018 wanted as a `published_by` column.
- **A pinned resolution is recorded per run**: a ref is what the author wrote, a commit SHA is what
  ran. A tag can move; a commit cannot.
- **Supersedes part of ADR 0018.** Survives: the bundle (dependencies resolved at publish time and
  travelling with the workflow, content-addressed), the three refusals (literal credential via
  `catalog.looksLikeSecret`, unresolvable reference, digest mismatch), failing at publish time not
  run time, immutable versions, and publish-time provenance. Revised: *where a bundle lives* — a git
  remote rather than our own store — and therefore that a registry server is required at all.
- **NOT in this change:** signing and verification policy, transitive `use:` (a bundle that itself
  uses another), rate limits and fetch quotas, and whether a moved tag invalidates a cache entry.
  Named so they are not assumed solved.

## Capabilities

### New Capabilities

- `remote-use`: the `use: owner/repo@ref` reference — its grammar for GitHub and for any other git
  host, when it is fetched, and the rule that an unresolvable or unpinned reference fails loudly.
- `git-published-bundle`: what a published bundle looks like as files in a git repository — layout,
  manifest placement, what is committed — and the immutability and provenance that a commit, a tag
  and a signature carry.
- `bundle-cache`: the local cache of fetched bundles, its content-addressed keys, the recorded
  pin that makes a run reproducible, and the offline/air-gapped path that never reaches a network.

### Modified Capabilities

<!-- None. `openspec/specs/` does not exist: no capability has been promoted from a change to a
     published spec yet, so `asset-publishing` (openspec/changes/publish-from-your-own-agent/
     specs/asset-publishing/spec.md) is still a change-local delta. The requirements this change
     revises live there; they are named explicitly above and in design.md rather than delta'd
     against a spec that has no published home. -->

## Impact

- `apps/api/internal/workflow/task.go:31-47,123-133` — `Use.Task` is a bare name resolved against a
  local map; it gains a parsed remote form and a resolution step before `expandUses` runs.
- `apps/api/cmd/wfx/publish.go` — a second sink (`--to <git remote>`); the existing host path and
  its `no host: run wfx login` refusal (`:40-45`) stay, but stop being the only way to publish.
- `apps/api/internal/bundle/` — `manifest.go`, `pack.go`, `build.go` are reused; `pack.go` gains a
  tree writer beside its tar writer, since the same manifest must describe both.
- `apps/api/internal/blob/` — unchanged code, changed role: cache rather than store. `folder.go`
  already refuses escaping keys and hex digests cannot escape.
- `apps/api/internal/api/bundles.go`, `apps/api/internal/store/bundles.go`, migration
  `000011_published_bundles` — retained as the mirror/index path; nothing is dropped in this change.
- `docs/adr/0018-the-registry-has-a-publish-direction.md` — partially superseded; a new ADR records
  which parts and why. `docs/adr/0011` is unaffected: names still travel, endpoints and credentials
  still do not.
- No new credential surface. Git transport auth is the user's existing SSH agent or token, invoked
  by git, never read or stored by us.

# Design — ship a gettable binary

## Context

Verified on this machine, 2026-09-24 (`git tag`, `ls -a`, `find`, `grep`):

| fact | evidence |
|---|---|
| no releases have ever been cut | `git tag` returns nothing |
| no CI has ever run | there is no `.github/` directory at all |
| no release tooling | no `.goreleaser.yml`, no npm package, no brew tap |
| the cross-compile already exists | `Taskfile.yml:101-117` — 6 targets × 3 binaries = 18 files into `bin/release/` |
| the air-gapped artifact already exists | `Taskfile.yml:119-137` → `bin/wfx-server.tar.gz` |
| nothing publishes | `release` (`:94-99`) ends at `ls -1 bin/release` |
| no binary knows its version | no `version` case in `apps/api/cmd/wfx/main.go:51-105`, no `-X` in `Taskfile.yml`, no version variable in `apps/api` |
| the repository is public and MIT | `gh repo view muthuishere/wfnexus` → `PUBLIC`, `mit`; `LICENSE` at the root |
| the module path is right | `apps/api/go.mod:1` — `github.com/muthuishere/wfnexus/apps/api`, so `go install` needs no rename first |

Two build invariants constrain every option below.

1. **The embed staging is the build.** `assets:stage` (`Taskfile.yml:51-70`) builds the UI and
   copies `apps/ui/dist`, `templates/`, `skills/` and `registries.json` into
   `apps/api/internal/assets/embedded/` — staged, never committed — and a missing
   `apps/ui/dist/index.html` **exits 1 with a message** (`:59-61`) rather than embedding nothing.
   `build:api`, `build:all` and `release:binaries` all `deps: [assets:stage]`. Any release path
   that does not run it produces a `wfx-server` that serves a 404 at `/`.
2. **`go.work` exists and points `./spikes` at unreleased toolnexus** (`go.work:1-17`, with the
   `replace` commented out). `apps/api` is pinned to `toolnexus/golang v0.19.0`
   (`apps/api/go.mod:13`) by ADR 0007. CI must build `apps/api`, not the workspace.

## Goals / Non-Goals

**Goals:**

- A stranger with no Go, no Node and no repository gets a working `wfx` in one command.
- Every published file has a published SHA-256, and the install path verifies it.
- A binary reports its own version, and an untagged build says it is untagged.
- The air-gapped path is a first-class artifact of every release, not a thing you build yourself.
- CI exists, and merging is gated on it.
- The embed invariant survives contact with CI.

**Non-Goals** (proposal "NOT in this change", repeated so they are not assumed): a Homebrew tap,
an npm wrapper, deb/rpm packages, code signing / macOS notarisation / Windows Authenticode, SLSA
provenance attestations, bit-for-bit reproducible builds, a public asset registry, and any change
to what the binaries *do*.

## Decisions

### 1. `task release` stays the build. goreleaser is not adopted.

goreleaser's value is the 80% it does for a project that has nothing: cross-compile matrix,
archives, checksums, changelog, a GitHub Release, brew/nfpm/Docker. This project already has the
expensive half, and it has it in a shape goreleaser cannot express.

| goreleaser gives | we already have | verdict |
|---|---|---|
| cross-compile matrix | `Taskfile.yml:101-117`, 6 targets, `-trimpath -ldflags="-s -w"`, `CGO_ENABLED=0` | duplicate |
| ldflags version injection | not yet — one line to add | ~10 lines of Taskfile |
| `checksums.txt` | not yet | 1 line: `shasum -a 256` |
| archives per target | we ship **raw binaries** plus **one composite tarball** that is not per-target: it carries linux binaries *and* `ui/`, `workflows/`, `templates/`, `skills/`, `registries.json`, `k8s/`, and its own Dockerfile + compose (`Taskfile.yml:119-137`) | goreleaser's `archives` does not model this; it would be an `extra_files` hack or a hook calling the Taskfile anyway |
| a pre-build hook for the UI | `assets:stage` — and its *failure* is load-bearing | a `before.hooks` entry re-stating a dependency Task already enforces |
| brew / nfpm / npm | explicitly out of scope | unused |

Adopting it means two descriptions of the build that must agree, and the one that runs locally
(`task release`, what a contributor debugs with) would be the one that is *not* what a release
runs. The rule in `openspec/config.yaml` is adopt-never-invent for **vocabulary and standards** —
semver, OCI, GitHub Releases, `go install` are all adopted here — not "adopt every tool".

**What we adopt instead:** `gh release create --generate-notes` in the release job. The 20 lines
goreleaser would earn its config for, from a CLI that is already on every GitHub runner.

**The trigger that would change our mind** (in the shape `docs/not-now.md` uses): the first
package manager we actually ship to. A brew tap or nfpm packages are where goreleaser stops
duplicating the Taskfile and starts doing something we would otherwise write. Recorded there.

### 2. Version injection: one variable, one ldflag, three binaries

One package — `apps/api/internal/buildinfo` — with `var Version = "dev"`, `var Commit = ""`,
`var Date = ""`. Nothing else imports anything; it must be importable by `main.go`,
`cmd/wfx` and `cmd/wfx-runner` without a cycle.

```
-X github.com/muthuishere/wfnexus/apps/api/internal/buildinfo.Version=$VERSION
```

added to the existing `-ldflags="-s -w"` in `build:*` and `release:binaries`.

- `$VERSION` comes from `git describe --tags --always --dirty`. On a tag it is `v1.2.3`; off a
  tag it is `v1.2.3-7-gabc1234` or, with no tags at all (today), `abc1234`. **A build made
  outside a tag reports what it actually is.** A binary that claims `v1.2.3` when it is seven
  commits past it is worse than one that says `dev`.
- **Why an ldflag rather than `runtime/debug.ReadBuildInfo`:** `ReadBuildInfo` gives real VCS
  stamping for `go install` builds — which is exactly the path where an ldflag is *not*
  available, because the user runs `go install`, not our Taskfile. So: **both**. `buildinfo`
  reads the ldflag if set and falls back to `ReadBuildInfo`'s `vcs.revision` / module version.
  A `go install …@v1.2.3` binary then reports `v1.2.3` without us controlling the build at all.
- Surface: `wfx version` (a new case in the switch at `apps/api/cmd/wfx/main.go:51-105`, listed
  in `usage()`), `--version` on all three, one line on server boot, and a `version` field on
  `/api/health`. The health field is what lets a *remote* operator answer "which build is that
  server" without shell access, and `wfx-runner` reports it on join so the Workers page can show
  a worker running a build older than the server.

### 3. CI: two workflows, and what is gated

`.github/workflows/ci.yml` — on `push` and `pull_request`:

- `setup-go` (1.26, per `apps/api/go.mod:3`) and `setup-node` (24, per `infra/Dockerfile:12`),
  both with dependency caching.
- `task check` (`Taskfile.yml:162`) = `test`, `vet` (`go vet` **and** `gofmt -l`, `:172-176`),
  `build:all` (5 targets compiled to `/dev/null`, `:204`), `ui:check` (`tsc --noEmit` + the real
  Vite build, `:177-181`), `workflows:validate` (every `workflows/*.yaml` through the built
  `wfx validate`, `:182-187`).
- plus `task test:race` (`:168` — the planner and scheduler run steps in parallel) and
  `task e2e` (`:79` — UI tests in jsdom against the built binary; sqlite, no Postgres, no model,
  no browser, so it needs no services).
- **`GOWORK=off`**, and the job runs in `apps/api`. `go.work` uses `./spikes`, which builds
  against unreleased toolnexus; a green CI run must prove what `apps/api` builds against, which
  ADR 0007 pins. `task spikes` is **not** in CI for the same reason — it is a local verification
  tool, and `docs/spikes/FINDINGS.md` is where its output lives.
- Matrix: `ubuntu-latest` for the full run, plus `macos-latest` and `windows-latest` for
  `task test` only. The product runs steps as subprocesses in a working directory
  (ADR 0015) and ships windows binaries; a path-separator or exec bug that only CI would catch
  is the reason the two extra legs exist. Windows has no `task` guarantee — the leg runs
  `go test ./internal/... ./cmd/...` directly rather than assuming the Task binary.
- **Gate:** a branch protection rule on `main` requiring the ubuntu job. That is a repository
  setting, not a file, and is called out as a task so it is not forgotten.
- **CI has no route around `assets:stage`**: it runs Task targets, all of which declare it as a
  dependency. Nobody adds a bare `go build` step.
- **One CI-only check that is not in `task check`:** the paired-migration rule from
  `openspec/config.yaml`. Every file added under `apps/api/migrations/` must have a counterpart
  under `apps/api/migrations/sqlite/` and both an `.up.sql` and a `.down.sql`. It is a shell
  loop, it costs nothing, and the convention is currently enforced only by review.

`.github/workflows/release.yml` — on `push: tags: ['v*']`:

1. re-run `task check` (a tag does not skip the gate),
2. `task release`,
3. `shasum -a 256` over every file in `bin/release/` **and** `bin/wfx-server.tar.gz` →
   `checksums.txt`,
4. `gh release create "$TAG" --generate-notes bin/release/* bin/wfx-server.tar.gz checksums.txt`,
5. build and push the OCI images (§5).

`GITHUB_TOKEN` with `contents: write` and `packages: write`. No other secret: nothing here needs
a model key, a signing key or a registry password.

### 4. Install paths, ranked by the three-scale test

**One person — zero config, no auth, one binary.**

```sh
curl -fsSL https://raw.githubusercontent.com/muthuishere/wfnexus/main/install.sh | sh
```

The script detects `uname -s`/`uname -m` → one of the six release targets, resolves the latest
tag through the GitHub API (or `WFX_VERSION` if set), downloads `wfx-$os-$arch` **and**
`checksums.txt`, **verifies the SHA-256 and refuses to install on a mismatch**, then installs to
`$WFX_INSTALL_DIR` defaulting to `~/.local/bin` — the same place `task install`
(`Taskfile.yml:153-160`) already puts it, so the two do not fight. It prints the PATH line if the
directory is not on PATH. It is ~60 lines of POSIX `sh`, readable before it is piped, and it is
the artifact of a public MIT repository rather than a vendor endpoint.

*Why not `go install` as the headline:* it requires a Go toolchain, and it cannot produce
`wfx-server` — the UI and defaults are staged into `internal/assets/embedded/` by
`assets:stage` and **not committed**, so a `go install` of the server package would embed an
empty directory. It is documented as the second path, correct for `wfx` and `wfx-runner`, and
honest about the third:

```sh
go install github.com/muthuishere/wfnexus/apps/api/cmd/wfx@latest        # the client
go install github.com/muthuishere/wfnexus/apps/api/cmd/wfx-runner@latest # a worker
# wfx-server: download the release binary or use the tarball — see below
```

*Windows:* `install.ps1`, same shape, same checksum verification, installing to
`%LOCALAPPDATA%\wfx\bin`. No winget manifest, no Chocolatey package.

**A small org — a shared server, pinned versions.** The install line honours `WFX_VERSION`, so
their own `Taskfile`/`Makefile` pins it and every developer and CI runner gets the same build,
checksum-verified:

```yaml
wfx:install:
  vars: { WFX_VERSION: v0.3.1 }
  cmds: ["curl -fsSL …/install.sh | WFX_VERSION={{.WFX_VERSION}} sh"]
```

This is the reason the version must be pinnable *and* the reason the binary must report it: the
pin is worthless if you cannot ask a machine what it actually has.

**An enterprise — air-gapped.** `wfx-server.tar.gz` + `checksums.txt`: download once on a
connected machine, verify, carry it in. Inside, `docker compose up` needs **no registry and no Go
toolchain**, because the linux binaries are inside the tarball and its Dockerfile copies them
(`infra/package/Dockerfile:1-9,18-19`; `infra/package/README.md:1-7`). Everything the server
needs on disk travels too — `ui/`, `workflows/`, `templates/`, `skills/`, `registries.json`,
`k8s/`, `config.example.yaml` (`Taskfile.yml:119-137`). What it still needs from outside is a
model provider, which is the customer's own endpoint under the product claim.

Three changes make it first-class rather than incidental:

- it is **published**, with a checksum, on every release;
- the release notes state the checksum verification command, because "verify a checksum" is not a
  step an enterprise should have to invent;
- `README.md`'s install section names it **before** the container registry, since the registry is
  the path an air-gapped site cannot use.

**The gap this exposes, and the call.** `infra/package/Dockerfile:18-19` copies
`wfx-server-linux-amd64` and `wfx-linux-amd64` only. The release builds linux/arm64 too, and
`release:tar` (`Taskfile.yml:126-128`) copies only the amd64 three. So the tarball's container is
**amd64-only** while the binaries beside it are not. **Decision: ship both tarball architectures
—** `wfx-server-linux-amd64.tar.gz` and `wfx-server-linux-arm64.tar.gz`, identical but for
`bin/` — rather than one multi-arch tarball with a `TARGETARCH` Dockerfile. An air-gapped site
downloads one file for one machine; making them resolve an architecture at `docker build` time
inside an airgap is the opposite of the property we are selling. It doubles a tarball that is
mostly the UI bundle, which is cheap. The `bin/` layout inside is unchanged, so the Dockerfile
keeps copying fixed names.

### 5. Containers: published to GHCR, and the tarball stays independent

`ghcr.io/muthuishere/wfx-server` and `ghcr.io/muthuishere/wfx-runner`, built from
`infra/Dockerfile` and `infra/Dockerfile.runner` (which build from source — deliberately not the
tarball's, per `infra/package/Dockerfile:7-8`), pushed by the release job, tagged `v1.2.3`,
`1.2`, and `latest`. `linux/amd64` and `linux/arm64` via buildx.

*Why GHCR over Docker Hub:* it authenticates with the `GITHUB_TOKEN` the release job already
holds, needs no second account and no second secret, has no anonymous pull rate limit for our
users, and lives next to the source and the releases. OCI is OCI; nothing about the image is
GitHub-specific, so this is reversible.

`infra/docker-compose.yml` and `infra/k8s/*.yaml` currently name a locally built image
(unverified — the exact `image:` values were not read). They should name the GHCR tag so
`docker compose up` works before a `docker build`. **The tarball's compose is not touched**: its
whole point is working with no registry, and pointing it at GHCR would silently delete the
air-gapped path.

### 6. Versioning policy

Semver 2.0.0, `v`-prefixed tags (Go's convention, and GitHub Releases'). **`v0.x` until the HTTP
API envelope and the workflow YAML schema are stable** — the pivot doc §4 records that
`POST /runs` and `GET /runs/{id}` disagree on their envelope, which is a breaking fix waiting to
happen. Under semver, `0.x` says that out loud; tagging `v1.0.0` now would either lock in the bug
or make the first real fix a major bump.

What a bump means, stated so it can be held to: **MINOR** = a new verb, a new field, a new
workflow key; **PATCH** = a fix that changes no contract; **MAJOR** (after 1.0) = a workflow
YAML that used to validate no longer does, an API response shape changes, or a CLI verb is
removed. The migrations are paired and forward-only; a release never promises a downgrade of the
schema, only that `.down.sql` exists.

## Risks / Trade-offs

- **[Unsigned macOS binaries are quarantined by Gatekeeper.]** A downloaded `wfx` will be refused
  on first run. → Documented workaround `xattr -d com.apple.quarantine ./wfx`, printed by
  `install.sh` on Darwin. Signing and notarisation need an Apple Developer account and a signing
  identity in CI, both out of scope here; `go install` and the container path are unaffected
  because neither sets the quarantine attribute. Stated in the release notes rather than
  discovered by the first user.
- **[`curl … | sh` is a supply-chain pattern security teams refuse.]** → It is the *one person*
  path only. The org path pins a version and the enterprise path is the verified tarball; neither
  pipes anything. The script is in a public MIT repository at a readable URL, and it verifies a
  checksum that the release published — which is more than most such scripts do.
- **[A checksum published beside the artifact proves integrity, not provenance.]** Anyone who can
  write the release can write both files. → Accepted for now, and named: SLSA attestations and
  signing are out of scope. Checksums close the transport-corruption and mirror-tampering case,
  which is the realistic one for a project with no releases at all today.
- **[The first CI run will probably be red.]** Nothing has ever enforced `gofmt -l`, `tsc
  --noEmit` or `workflows:validate`, and the working tree currently has ~20 modified files and a
  dozen untracked ones. → The first task is to run `task check` locally and fix what it finds,
  *before* the workflow file exists. A CI that lands red teaches everyone to ignore it.
- **[Two tarballs double the release's byte size.]** → Accepted; it is mostly the UI bundle, and
  the alternative costs an airgapped operator a `docker buildx` decision.
- **[`git describe` in CI needs full history.]** `actions/checkout` defaults to `fetch-depth: 1`,
  which makes `git describe --tags` fail or lie. → `fetch-depth: 0` on both workflows, and a CI
  assertion that the built binary's `wfx version` equals the tag on a tag build. A version that
  is wrong in the one build that matters is the exact failure this change exists to prevent.
- **[`go install @latest` resolves through the Go module proxy, which caches.]** A tag is
  immutable there — a retracted or re-pointed tag is not fixable. → Never move a tag. If a
  release is bad, tag the next patch.

## Migration Plan

There is nothing deployed to migrate. The order is: version plumbing → CI green on `main` →
first tag `v0.1.0` → install script → the README rewrite pointing at it. Rollback for a bad
release is a new patch tag, never a moved or deleted one (the Go proxy caches tags immutably, and
so do the checksums anyone already recorded).

## Open Questions

- Does anything assert the exact body of `/api/health`? Adding a field is additive, but the UI or
  a test may match it strictly. **Unverified** — not read.
- What `image:` do `infra/docker-compose.yml` and `infra/k8s/*.yaml` name today? **Unverified**.
  Whether they change is decided by that value, not by this document.
- Is `v0.1.0` the right first tag, or does the existing feature surface justify `v0.2.0`? A
  judgement call, not a technical one; it is left to whoever cuts it.
- The pre-1.0 stability window has no stated exit. The honest trigger is the API envelope fix
  from the pivot doc §4 landing plus the workflow schema going a release without a breaking
  change — but that is a proposal for later, not a decision here.

## Why

Under the pivot the **client is the product surface** — you author a workflow inside your own
agent and publish it to a server you host — and the client cannot be obtained. Verified on this
machine, 2026-09-24: `git tag` returns nothing, there is no `.github/` directory at all, no
goreleaser config, no npm package and no brew tap. `task release` (`Taskfile.yml:94`) builds 18
binaries and a self-contained `wfx-server.tar.gz` **onto one disk** and publishes none of it.
The only documented way to get `wfx` is `task install` (`Taskfile.yml:153`), which requires the
repository, Go 1.26 and Node — i.e. it is not distribution, it is a developer convenience.

`docs/research/the-pivot-2026-09-24.md` §5 makes this item 1 of the order of work, ahead of
login/publish. `openspec/changes/publish-from-your-own-agent/` names it a hard prerequisite in
its proposal Impact and again in `tasks.md`: until this lands, that change is verified by
`go run`, not by a binary anyone has.

Second, a binary that cannot say what it is cannot be supported. Verified: there is no `version`
verb in the `wfx` switch (`apps/api/cmd/wfx/main.go:51-105`), no `-X` ldflag anywhere in
`Taskfile.yml`, and no version variable in `apps/api`. A bug report today cannot identify a build.

## What Changes

- **Semantic versioning on git tags.** `vMAJOR.MINOR.PATCH` (semver 2.0.0, the vocabulary Go
  modules and GitHub Releases already use). Pre-1.0 while the API and the workflow schema still
  move; that is stated rather than implied. The tag is the only release trigger.
- **A build learns its own version from the tag.** One `-X` ldflag into one variable, set by
  `Taskfile.yml`, consumed by all three binaries. New verb **`wfx version`** and `--version`,
  matching `wfx-server` and `wfx-runner`, and a version field on the existing `/api/health`
  response. A build made outside a tag says so (`dev` + commit + `-dirty`), it does not lie.
- **The repository's first CI**, GitHub Actions (the vocabulary this project already adopted —
  `docs/not-now.md`: "the shape we are keeping: GitHub Actions"). On push and pull request:
  `task check` — `test`, `vet` (which includes `gofmt -l`), `build:all`, `ui:check`,
  `workflows:validate` (`Taskfile.yml:162-187`), plus `test:race` and `e2e`. **Merging to `main`
  is gated on green**; today nothing is gated on anything.
- **A release job on tag** that runs `task release` and uploads to **GitHub Releases**: the 18
  binaries, a `checksums.txt` (SHA-256), and `wfx-server.tar.gz`. **`task release` stays the
  build; goreleaser is not adopted** — the argument is in `design.md`, but in short the Taskfile
  already does the cross-compile, the embed staging and the tarball, and the tarball is the
  air-gapped artifact goreleaser has no opinion about.
- **One install line for one person**, ranked by the three-scale test: a `curl … | sh` script
  published from the repository that detects OS/arch, downloads from the tag, **verifies the
  checksum before installing**, and drops `wfx` in `~/.local/bin` — exactly where `task install`
  already puts it. `go install github.com/muthuishere/wfnexus/apps/api/cmd/wfx@latest` is
  documented as the second path for people who have Go (it works for `wfx` and `wfx-runner`, and
  **not** for `wfx-server`, whose embedded UI is staged, not committed — `Taskfile.yml:51-70`).
  Windows: a `.ps1` of the same shape; no package manager.
- **A pinned version for a small org** — the download line takes `WFX_VERSION`, so a Taskfile or
  Makefile in their repo pins a version and re-verifies the checksum on every machine.
- **Air-gapped is first-class, not an afterthought.** `wfx-server.tar.gz` plus `checksums.txt` is
  the enterprise artifact: download once, verify, carry it in, `docker compose up` with no
  registry and no Go toolchain (`infra/package/Dockerfile:1-9`). It is named in the install docs
  before the container registry is, and the release is not considered good if only the tarball's
  checksum is missing.
- **Container images published to GHCR** (`ghcr.io/muthuishere/wfx-server`, `…/wfx-runner`),
  built by the release job from `infra/Dockerfile` and `infra/Dockerfile.runner`, tagged with the
  semver tag and `latest`. OCI vocabulary, the registry that needs no second account. The
  tarball's own Dockerfile stays as it is — it is the no-registry path and must not start
  depending on GHCR.
- **The embed invariant is preserved.** Every release path goes through `assets:stage`, and a
  missing `apps/ui/dist/index.html` **fails the build** (`Taskfile.yml:59-61`) rather than
  embedding nothing. CI must not have a route around it.
- **NOT in this change**, named so they are not assumed: a Homebrew tap, an npm wrapper, a public
  asset/skill registry (that is `publish-from-your-own-agent` and it is a *private* registry),
  code signing and macOS notarisation, Windows Authenticode, SLSA provenance attestations,
  reproducible-build bit-for-bit verification, and Linux distro packages (deb/rpm).
  **Known consequence, stated rather than ignored:** an unsigned downloaded macOS binary is
  quarantined by Gatekeeper. The documented workaround is
  `xattr -d com.apple.quarantine ./wfx`; `go install` and the container path avoid it entirely
  because neither carries the quarantine attribute.

## Capabilities

### New Capabilities

- `binary-versioning`: a build knows and reports its own identity — semver tags, the ldflag path,
  `wfx version`, the untagged-build rule, and the health endpoint's version field.
- `continuous-integration`: what runs on push and pull request, what is gated on green, and the
  invariants CI must not be able to route around (the embed staging, the paired migrations).
- `release-artifacts`: what a tag publishes — the 18 binaries, `checksums.txt`, the self-contained
  tarball, the OCI images — and the integrity requirement that every published file is covered by
  a published checksum.
- `install-paths`: how a stranger obtains the thing, stated at all three scales, including the
  air-gapped one, and what each path does and does not verify.

### Modified Capabilities

<!-- None. openspec/specs/ is empty — publish-from-your-own-agent is the only other change and is
     not archived, so there is no published capability to delta against. -->

## Impact

- `Taskfile.yml:94-137` — `release`, `release:binaries`, `release:tar` already do the work; they
  gain ldflags and a checksum step. `Taskfile.yml:162` `check` is what CI runs; it does not
  currently include `test:race` (`:168`) or `e2e` (`:79`).
- `apps/api/cmd/wfx/main.go:51-105` — the verb switch; a `version` case is added. `usage()` lists
  the verbs and must list it.
- `apps/api/main.go`, `apps/api/cmd/wfx-runner/` — the same version variable, printed on boot by
  the server and reported by the runner on join, so an operator can see which build a worker is.
- `apps/api/internal/api/api.go` — `/api/health` gains a version field (unverified: whether any
  UI or test asserts the exact shape of that response body; not read).
- **New**: `.github/workflows/ci.yml` and `.github/workflows/release.yml`. The repository has no
  `.github/` directory today, so this is also CODEOWNERS-less, template-less first contact with
  GitHub's own conventions.
- **New**: an install script in the repository (`install.sh`, `install.ps1`) that the release
  publishes; `README.md:94-106` ("A release") describes `task release` as if it were the end of
  the story and becomes the install section instead.
- `infra/package/` — the tarball is promoted to the named air-gapped artifact. Note a real gap:
  `infra/package/Dockerfile:18-19` copies **amd64 only**, so the shipped tarball's container is
  amd64-only while the release builds arm64 binaries. This change decides what that means.
- `openspec/changes/publish-from-your-own-agent/` — its stated prerequisite. Nothing in it
  changes; it is unblocked.
- `docs/adr/` — no existing ADR covers distribution (0001-0021 are runtime and product
  decisions). The goreleaser-vs-Taskfile call and the GHCR call are argued in `design.md`; if
  they survive review they are worth an ADR 0022, which this change does not write.

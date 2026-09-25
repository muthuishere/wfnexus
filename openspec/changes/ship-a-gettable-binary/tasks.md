# Tasks — ship a gettable binary

Ordering is by dependency. Group 1 is a prerequisite for CI existing at all — the working tree
has never been through `task check` and the first workflow must not land red. Groups 2–3 are the
version plumbing and ship on their own. Group 4 is the first CI this repository has ever had.
Groups 5–7 are the release: artifacts, then the offline path, then the images. Group 8 is the
install surface, group 9 the documentation, group 10 the first tag.

Tasks marked **[GATE]** change what can be merged. Tasks marked **[VERIFY]** MUST land with the
named observed output — this repository's rule is that a claim without observed output is not
done, and there is nowhere to record it but the task itself and the pull request.

**This change writes no product behaviour.** Nothing here alters what a workflow does, what a
step runs, or what the API returns beyond one additive field.

## 1. Make the tree green before anything enforces it

- [ ] 1.1 Run `task check` (`Taskfile.yml:162`) on a clean checkout of `main` and record every
      failure. The working tree currently has ~20 modified and a dozen untracked files and has
      never been through `gofmt -l`, `tsc --noEmit` or `workflows:validate`. **[VERIFY]** paste
      the full output into the pull request.
- [ ] 1.2 Fix what 1.1 found, in a separate commit per check, touching only what the check names.
      No drive-by refactors: a formatting fix and a type fix are different commits.
- [ ] 1.3 Run `task test:race` (`:168`) and `task e2e` (`:79`) and fix or record what they find.
      `e2e` needs no Postgres, no model and no browser; if it does, that is a defect to record,
      not a reason to drop it from CI.
- [ ] 1.4 Confirm `GOWORK=off task check` passes from `apps/api`. `go.work:8-9` uses `./spikes`,
      which builds against unreleased toolnexus; CI must prove what `apps/api/go.mod:13`'s pinned
      `v0.19.0` builds against (ADR 0007). **[VERIFY]** both invocations' exit codes.

## 2. The version variable

- [ ] 2.1 Create `apps/api/internal/buildinfo` with `Version`, `Commit`, `Date` and a `String()`.
      It SHALL import nothing from the rest of the tree, so `main.go`, `cmd/wfx` and
      `cmd/wfx-runner` can all take it without a cycle.
- [ ] 2.2 Make `buildinfo` fall back to `runtime/debug.ReadBuildInfo()` — module version and
      `vcs.revision` — when the ldflags are unset. This is the only thing that gives a
      `go install` build a real version, and that path never runs our build system.
- [ ] 2.3 Unit-test the fallback: ldflags set wins; ldflags unset reads build info; neither
      available yields a stated placeholder and never an empty string. **[VERIFY]** `go test`.
- [~] 2.4 **Taskfile half DONE** (`VERSION`/`COMMIT`/`DATE` vars + `LDFLAGS` wired into
      `build:api`, `build:cli`, `build:runner`, `release:binaries`, `build:runner:all`). Verified:
      an `-X` whose target package/symbol does not exist is a SILENT NO-OP in the Go linker
      (`go build` exit 0), so the flags are correct to carry before `internal/buildinfo` lands —
      they start working the moment 2.1/2.2 do, with no Taskfile rework. Original task text:
      Add `VERSION: {sh: git describe --tags --always --dirty}` to `Taskfile.yml` vars and
      the `-X …/buildinfo.Version=` ldflag to the existing `-trimpath -ldflags="-s -w"` in
      `build:api`, `build:cli`, `build:runner`, `build:all` (`:204`), `build:runner:all` (`:139`)
      and `release:binaries` (`:101`). Commit and date the same way.

## 3. The version surface

- [ ] 3.1 Add a `version` case to the verb switch in `apps/api/cmd/wfx/main.go:51-105` and list
      it in `usage()`. It SHALL contact no server and exit zero.
- [x] 3.2 Add `--version` to all three binaries, handled before any config load, so it answers on
      a machine where the config is broken.
      **Done 2026-09-25, and TWO of the three binaries were wrong — the second worse than
      the first.** `wfx` already answered. `wfx-runner --version` was `unknown command`.
      The server fell THROUGH to `config.LoadWithFile`, which resolves the asset roots and
      MATERIALISES the embedded defaults into the data directory — so asking it what
      version it was created files under `~/.local/share/wfnexus` and then started a
      server. That is why this task says "before any config load": the load has side
      effects, and the first question anyone asks a new binary must not have any.
      Both now answer before anything is loaded, and `version_cli_test.go` asserts all
      three against a HOME that does not exist yet, failing if asking for a version
      creates a single entry in it.
- [ ] 3.3 Print the version on one line at server boot. Note `docs/research/the-pivot-2026-09-24.md`
      §4 records that boot already prints ~40 duplicate-skill warnings twice; do not add to the
      noise — one line, before them.
- [ ] 3.4 Add a `version` field to the health endpoint response in
      `apps/api/internal/api/api.go`. First check whether the UI or any test asserts that body's
      exact shape — recorded as unverified in `design.md`. **[VERIFY]** `task e2e` still passes.
- [ ] 3.5 Have `wfx-runner join` report its version, and store it on the worker row (paired
      migration: `apps/api/migrations/` **and** `apps/api/migrations/sqlite/`, up and down each).
- [ ] 3.6 Show the worker's version on the Workers page and in `wfx workers`, so a worker older
      than the server is visible rather than inferred.

## 4. The first CI [GATE]

- [x] 4.1 Write `.github/workflows/ci.yml`: `push` and `pull_request`, `actions/checkout` with
      `fetch-depth: 0` (shallow checkout makes `git describe --tags` lie), Go 1.26 per
      `apps/api/go.mod:3`, Node 24 per `infra/Dockerfile:12`, both cached, `GOWORK=off`.
- [x] 4.2 The ubuntu job runs `task check`, `task test:race`, `task e2e`. No bare `go build` of
      the server package anywhere in the file — every server binary goes through `assets:stage`
      (`Taskfile.yml:51-70`), whose failure on a missing `apps/ui/dist/index.html` is the thing
      that keeps an empty embedded UI impossible.
- [x] 4.3 Add `macos-latest` and `windows-latest` legs running the Go test suite only. The
      windows leg calls `go test ./internal/... ./cmd/...` directly rather than assuming Task is
      installed on the runner.
- [x] 4.4 Add a paired-migration check: every file under `apps/api/migrations/` has a counterpart
      under `apps/api/migrations/sqlite/`, and every migration has both `.up.sql` and `.down.sql`.
      A shell loop. It is the one convention in `openspec/config.yaml` that nothing but review
      enforces today. **[VERIFY]** prove it fails on a deliberately unpaired file, then remove
      that file.
- [ ] 4.5 **[GATE]** Enable branch protection on `main` requiring the ubuntu job. This is a
      repository setting, not a file — it is a task so that it is not assumed done because a
      workflow exists.
- [ ] 4.6 **[VERIFY]** Open a throwaway pull request that breaks formatting and confirm it cannot
      merge. A gate nobody has seen block anything is not known to be a gate.

## 5. The release job and its artifacts

- [x] 5.1 Add `release:checksums` to `Taskfile.yml`: `shasum -a 256` over every file in
      `bin/release/` and every tarball, written to `bin/release/checksums.txt`. Make `release`
      (`:94`) depend on it, so the checksums exist for a local release too.
- [x] 5.2 Write `.github/workflows/release.yml`: on `push: tags: ['v*']`, `fetch-depth: 0`,
      run the full check suite **first** (a tag does not skip the gate), then `task release`.
- [~] 5.3 Written into `release.yml` ("Assert the built binary reports the tag") but it
      currently `exit 0`s with a warning, because `wfx version` does not exist yet. Delete that
      escape hatch when 3.1 lands. Original task text: **[VERIFY]** Assert in the job that the built binary's `wfx version` equals the tag,
      and fail the release if it does not. This is the one build where a wrong version is
      unrecoverable, because a tag is never moved.
- [x] 5.4 `gh release create "$TAG" --generate-notes` with the 18 binaries, both tarballs and
      `checksums.txt`. Permissions `contents: write`, `packages: write`; no other secret.
- [x] 5.5 Assert every uploaded file appears in `checksums.txt` before uploading. A release with
      an uncovered file is defective by the spec, and the cheapest place to catch it is here.
- [x] 5.6 Put the checksum-verification command and the macOS quarantine consequence
      (`xattr -d com.apple.quarantine ./wfx`) into the release notes body. Unsigned and
      un-notarised is a stated consequence, not a surprise for the first user.

## 6. The air-gapped tarball, made first-class

- [x] 6.1 Change `release:tar` (`Taskfile.yml:119-137`) to produce one tarball per Linux
      architecture — `wfx-server-linux-amd64.tar.gz` and `wfx-server-linux-arm64.tar.gz` —
      identical but for `bin/`. Today it copies amd64 only (`:126-128`) while the release builds
      arm64 binaries, and `infra/package/Dockerfile:18-19` copies fixed amd64 names.
- [x] 6.2 Keep the fixed names inside `bin/` so `infra/package/Dockerfile` is unchanged. The
      architecture is chosen when the file is downloaded, never at `docker build` time on a
      disconnected machine.
- [ ] 6.3 **[VERIFY]** Unpack an arm64 tarball on an arm64 machine with no registry access and
      run `docker compose up`, then load the UI. Record the observed output. The claim in
      `infra/package/README.md:1-7` is currently unproven for arm64.
- [ ] 6.4 Confirm the tarball's compose and Dockerfile still reference no container registry for
      the application after group 7 lands. The offline path is the property being sold; a
      registry reference silently deletes it.

## 7. Container images

- [x] 7.1 Add image build and push to `release.yml`: `ghcr.io/muthuishere/wfx-server` and
      `…/wfx-runner`, from `infra/Dockerfile` and `infra/Dockerfile.runner`, buildx
      `linux/amd64,linux/arm64`, tagged `v1.2.3`, `1.2` and `latest`. Authenticates with the
      job's own token — no second account, no second secret.
- [ ] 7.2 Read the `image:` values in `infra/docker-compose.yml` and `infra/k8s/*.yaml` (recorded
      unverified in `design.md`) and point them at the published tags, so `docker compose up`
      works before a `docker build`. **Do not touch `infra/package/`.**
- [ ] 7.3 **[VERIFY]** `docker pull` both images at the tag on a machine that has never built
      them, and start the server.

## 8. The install paths

- [x] 8.1 Write `install.sh` at the repository root: POSIX `sh`, detects `uname -s`/`uname -m` →
      one of the six targets, resolves the newest tag or honours `WFX_VERSION`, downloads the
      binary **and** `checksums.txt`, verifies, installs to `${WFX_INSTALL_DIR:-~/.local/bin}` —
      the same directory `task install` (`Taskfile.yml:153-160`) uses — and prints the PATH line
      if needed. It must be short enough to read before it is piped.
- [x] 8.2 Refuse to install when the checksum does not match or cannot be obtained: remove the
      partial download, exit non-zero, name the mismatch. Not a warning.
- [x] 8.3 Refuse an unpublished platform, naming the detected pair and the supported ones.
- [x] 8.4 Print the macOS quarantine workaround on Darwin, after a successful install.
- [x] 8.5 Write `install.ps1` with the same behaviour, installing to `%LOCALAPPDATA%\wfx\bin`.
      No winget manifest and no Chocolatey package — out of scope, and say so in the docs.
- [ ] 8.6 **[VERIFY]** Run `install.sh` in a container with no Go, no Node and no repository, for
      linux/amd64 and linux/arm64, and on a macOS machine. Record `wfx version` output for each.
- [ ] 8.7 **[VERIFY]** Run it with `WFX_VERSION` pinned to an older release and confirm the
      installed binary reports that version. The pin is worthless if the binary cannot confirm it.
- [ ] 8.8 **[VERIFY]** `go install github.com/muthuishere/wfnexus/apps/api/cmd/wfx@<tag>` on a
      machine with only Go, and confirm `wfx version` reports the tag through the build-info
      fallback from 2.2. Do the same for `wfx-runner`.
- [ ] 8.9 **[VERIFY]** Confirm `go install …/apps/api@<tag>` produces a server that serves a 404
      at `/`, and record it — that observed failure is why the documentation names the two paths
      that work instead of pretending this one does.

## 9. Documentation

- [x] 9.1 Rewrite `README.md:94-106` ("A release"). It currently ends at `task release` as though
      that were distribution. It becomes an install section: the one-line install first, then the
      pinned version, then the tarball, then the images. `task release` moves to a contributor
      note.
- [x] 9.2 State the offline tarball **before** the registry path everywhere both appear. The
      registry is the path an air-gapped site cannot use.
- [x] 9.3 Document `go install` with its limit stated: it works for `wfx` and `wfx-runner`, and
      not for `wfx-server`, because `assets:stage` stages the UI and defaults into
      `internal/assets/embedded/` and they are not committed.
- [x] 9.4 Document the versioning policy from `design.md` §6: `v0.x` until the API envelope and
      the workflow schema settle, and what MINOR, PATCH and a future MAJOR mean.
- [x] 9.5 Add the goreleaser decision to `docs/not-now.md` in that file's shape — why not now
      (the Taskfile already does the cross-compile, the embed staging and the composite tarball)
      and the trigger that would change our mind (the first package manager we actually ship to,
      where nfpm or a brew tap stops duplicating the Taskfile).
- [x] 9.6 Remove the stale distribution claim from anywhere that implies a release exists, and
      update `openspec/changes/publish-from-your-own-agent/tasks.md`'s prerequisite note once
      this ships — its verification can stop being `go run`.

## 10. Cut the first release

- [ ] 10.1 Decide the first tag. `design.md` leaves it open between `v0.1.0` and `v0.2.0`; it is
      a judgement call, and whoever cuts it records the reason in the release notes.
- [ ] 10.2 Tag and push. **[VERIFY]** Record the full artifact list, the `checksums.txt` and the
      output of `wfx version` from a binary downloaded off that release by the install line — not
      from the build. The end state of this change is a stranger's path, proven from a stranger's
      position.
- [ ] 10.3 Consider an ADR 0022 for the two decisions worth outliving this change: the release
      build stays the Taskfile, and the offline tarball is a first-class artifact rather than a
      convenience. `docs/adr/0001-0021` cover runtime and product; none covers distribution.
      Not written here — noted so it is a decision rather than a drift.

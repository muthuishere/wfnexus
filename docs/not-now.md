# Not now — and what would change our mind

Things that get proposed, get researched, and then get built too early. They are
written down so the research is not repeated and so nobody mistakes "not yet"
for "not thought about".

The shape we are keeping: **GitHub Actions**. A workflow file, jobs, steps, a
working directory, a runner. People already know it. Every item below adds a
concept that is not in that vocabulary, and each one has to earn its place by
something actually going wrong — not by being a good idea.

| idea | why not now | what would change our mind |
|---|---|---|
| **A container / namespace sandbox per run** | ADR 0015 fixes what actually broke three times — a step landing in the wrong directory — with a subprocess and a working directory. A container fixes a threat we have not met. It also costs 307 ms per `docker run` and drags in an image to build, version and ship. | We run somebody else's workflow, or somebody else's repository, on our machine. That is the moment isolation stops being hypothetical. |
| **Egress allowlist through a proxy** | Same reason. There is one operator and the workflows are ours. A CONNECT proxy is a component to run, configure and debug. | The same trigger: untrusted input reaching a step that can run commands. Prompt injection through a stranger's bug report is the realistic version. |
| ~~**Snapshot resume instead of replay**~~ — **answered 2026-09-23, and it is neither** | The competitor pass settled this. DBOS, Inngest and Flyte all do the same cheap thing: persist each step's output and SKIP the step on re-drive. We already store `step_runs.output` as jsonb, so most of it exists. Temporal-style deterministic replay is the expensive wrong branch — it imports determinism discipline and a history cap for nothing that memoised output does not give us. | Nothing. It moved to the roadmap: add an input hash and ship `wfx run --recover` / `--only <step-id>`. |
| **Horizontal scale (Postgres `SKIP LOCKED` + leases)** | One machine is not the bottleneck and there is no queue backing up. Leases bring lease TTLs, heartbeat intervals and double-execution — three new correctness parameters for a problem we do not have. | Runs actually queueing behind the concurrency cap for long enough to notice. |
| **Auth and tenancy** | There is none, and on localhost that is correct. | **The moment this is exposed on any address that is not localhost.** This is the one item on the list that is a hard gate rather than a judgement call — it is not "when it hurts", it is "before anyone else can reach it". |
| **Secrets in Postgres** | `apiKeyEnv` already keeps values out of workflow files, which is the property that matters: a workflow is portable and committable because it names a variable. A secret store adds encryption, a master key and a rotation story. | More than one machine running steps (which is the scale item above), or more than one tenant. |
| **Set failure kinds (`unavailable` / `exited` / `invalid`)** | Worth remembering: an exit code cannot tell "could not start" from "ran and failed" — `docker run` exits 125 for both — so retry cannot tell a flaky worker from a bad command. Today there is one executor and no worker to be flaky. | Arrives with the first thing on this list, or the fourth. Whichever comes first, this comes with it. |

## Answered, not deferred

Two entries above were settled by the 2026-09-23 competitor pass rather than by us building
anything, which is the cheapest way to close a question:

- **Durable resume** is memoised step output, not snapshots and not replay (see the table).
- **Expression evaluation** — we were on course to write our own parser for `if:`. We should not.
  `rhysd/actionlint` (MIT) parses and type-checks Actions expressions, and `nektos/act`'s
  `exprparser` (MIT) evaluates them with a typed environment and models the implicit `success()`.
  Writing our own would mean diverging from GitHub's semantics in ways nobody would notice until a
  workflow behaved differently here than in a repository's own Actions file — which is the exact
  failure the whole "adopt, never invent" rule exists to prevent.

## The one rule worth keeping from all of it

Every mechanism that fails enumerates escapes; every mechanism that works
enumerates inclusions. ADR 0015 is an inclusion rule — the cwd *is* the
workspace — which is why it is the one that got built.

## Answered by building something smaller (2026-09-25)

| idea | why not now | what would change our mind |
|---|---|---|
| **goreleaser** | `Taskfile.yml` already does the expensive half — the 6-target cross-compile with `-trimpath -ldflags="-s -w" CGO_ENABLED=0`, the embed staging whose *failure* is load-bearing (a missing `apps/ui/dist/index.html` exits 1 so a binary can never ship an empty UI), and a **composite** tarball carrying the binaries, the UI, workflows, templates, skills, `registries.json` and its own Dockerfile. goreleaser's per-target `archives` cannot model that tarball; it would become an `extra_files` hack or a hook that calls the Taskfile anyway. What it would genuinely add is about twenty lines — ldflags, `checksums.txt`, a GitHub Release — and those were adopted directly (`gh release create --generate-notes`). | The first package manager we actually ship to. At a brew tap or a deb/rpm, nfpm or goreleaser stops duplicating the Taskfile and starts doing something it does not. |
| **Signing and notarisation** | One operator, no paid Apple Developer account, and a checksum already answers "is this the file they published". Notarisation answers a different question — "did Apple see it" — that nobody has asked us. | Someone installs this who did not build it and is not willing to run `xattr -d com.apple.quarantine`. That is the same trigger as the first package manager, and they will probably arrive together. |

## Found by the first CI run (2026-09-24)

| idea | why not now | what would change our mind |
|---|---|---|
| **Containment on Windows** | ADR 0006 calls containment a guardrail, and on Windows it does not guard: `cd /etc && cat passwd`, `/tmp/pwned`, `pushd /var/log` and `git --git-dir=/elsewhere/.git` are all ALLOWED, and `/etc/passwd` is rewritten *into* the workspace rather than refused, because every check is written against POSIX absolute paths. It went unnoticed because nothing had ever run the suite on Windows. Doing it properly means `C:\`, UNC paths (`\\server\share`), drive-relative paths (`C:foo`), reserved device names (`NUL`, `CON`) and case-insensitive comparison — a real piece of work, and the inclusion rule ADR 0015 chose (the cwd IS the workspace) is the part that already holds on both. | A step actually runs on a Windows worker. `runs-on: windows` is documented and the binaries ship, so this is "when someone uses it", not "if". Until then the Windows CI leg says so out loud rather than skipping quietly. |
| **A Windows test harness** | The devinadapter fakes are shell scripts; Windows cannot exec them. Rewriting them as Go test binaries is easy and buys coverage of the CLI/ACP adapters on the platform where argv quoting differs most — but it is harness work, and the containment gap above is the one that matters. | The same trigger. They arrive together or the Windows leg is theatre. |

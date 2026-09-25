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

## Left open by "a git remote is the registry" (2026-09-25)

| idea | why not now | what would change our mind |
|---|---|---|
| **Transitive `use:`** — a fetched bundle whose own workflow declares a remote `use:` | Depth, cycles, and whether a transitive dependency is carried at publish time (a self-contained bundle, at the cost of duplicating everything it depends on) or resolved on fetch (a load that reaches the network N times) are four decisions, and nothing has needed even one of them. One level resolves; a nested remote `use:` is **refused with a message saying so**, which is the honest half-answer rather than a resolver that works to an undocumented depth. | Somebody publishes a bundle that is genuinely worth composing from — a task everyone's workflow wants to `use:` — and the alternative is every author copying it. That is when the duplication argument flips, and the shape it wants is npm's lockfile, not a resolver we invent. |
| **Signing and verification policy** | Git already signs a commit and a tag, and a signed tag is better tamper-evidence than the `published_by` column ADR 0018 had. What is undecided is whether wfnexus *verifies* one: which trust roots it would consult, and whether an unsigned bundle is refused, warned about or accepted. Each answer is a policy somebody has to administer, and picking one before anyone has an opinion is how a tool ends up with a verification mode everybody disables. This change **records what git reports and enforces nothing**. | A workflow is consumed from a remote the consumer does not control — a public repo, or a vendor's. The pin (a recorded commit SHA) detects a moved tag; it says nothing about who wrote the commit. That is the moment a trust root has an owner and the policy has someone to set it. |
| **Present vs authenticated for a CLI provider** | `engine/doctor.go`'s `checkProvider` reports a `cli`/`acp` provider ready on a **PATH lookup** — its own comment says "no key, because the CLI holds its own credential". So a green tick means the binary exists, not that anyone is logged into it, and a pull that passes can still fail on turn one with an auth error. Doing better means a cheap per-vendor probe (a `whoami`, a `--version` that differs when logged out) for each CLI, which is per-vendor work that changes when the vendor changes. The design says so and **does not decide it**. Until it is decided, the wording at both the doctor and the pull site says *present*, never *ready to run*. | A pull passes and the first turn fails on auth often enough that an operator stops trusting the check. One vendor's probe is also enough: if `claude --version` or a `whoami` reliably distinguishes logged-out, that CLI gets one and the others keep saying *present*. Per-vendor is fine; a green tick that lies is not. |

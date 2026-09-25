## Context

Three pieces exist and point at each other with a gap in the middle.

A bundle's manifest records `Requires` — each step's provider with its KIND (`http`/`cli`/`acp`), each
`runs-on:` label, each MCP server. `internal/bundle/require.go` checks that list against a `Host`
interface and refuses a pull that cannot run. And `wfx-runner join --url --token --labels` brings a
machine into the pool by connecting OUT.

Between "this host cannot run it" and "this host has joined" there is nothing, and that gap is filled by
hand today.

Two constraints are already settled and this design does not revisit them. **The platform never holds a
`cli` provider's credential** — `provider: claude-cli` means that machine's subscription, which is the
bargain a self-hosted Actions runner makes and what lets an SSO-bound seat work at all. And **presence is
not authentication**: `checkProvider` reports a `cli` provider ready on a PATH lookup, already carrying
`AuthUnknown` and a per-vendor `Login` (`presetLogin`: `claude`, `gh auth login`, `codex login`,
`opencode auth login`). Honest, but inert.

## Goals / Non-Goals

**Goals:**

- One command on a machine takes it from bare to serving a pool, and says plainly what it could not do.
- A credential reaches the machine from its vendor, in the operator's own terminal.
- Readiness stops reporting presence as readiness.

**Non-Goals:**

- **No SSH and no remote execution.** wfx does not reach into a machine. An operator who wants to get
  onto a box has `ssh` already, and the join model is outbound for good reasons that stand.
- **Not a credential vault.** Nothing accepts, stores, forwards or proxies a provider credential.
- **Not configuration management.** This does not converge a machine to a desired state, own its
  packages, or run on a schedule. It prepares a machine once, when asked, by the person asking.
- **Not per-vendor auth probing for every vendor.** Only where a vendor's CLI answers cheaply and
  unambiguously. An invented probe is worse than an honest "unknown".

## Decisions

### 1. The runner sets itself up, because whoever runs it is already there

`wfx-runner setup` runs on the machine being prepared. This is the whole reason the change is small: the
operator is in a terminal on that box with their own privileges, so there is no transport to build, no
credential to move, no connection to store, and no inbound port to open.

*Alternative rejected:* `wfx host setup <ssh target>` from the operator's laptop. It sounds more
convenient and costs a new SSH transport, a store of connections, a remote-command allowlist, and a way
to relay an interactive vendor login to a terminal that is not attached to it. Every one of those is a
place to get security wrong, to serve a convenience that `ssh box && wfx-runner setup` already provides.

*Consequence:* setting up ten machines is ten invocations. That is a real cost and the right one to pay
first; if it hurts, the answer is a loop in the operator's own shell, not a transport in ours.

### 2. Only enumerated installers run, and an unknown one is a refusal with an instruction

What may be installed is an allowlist of named installers per provider preset. `docs/not-now.md` states
the rule: *every mechanism that fails enumerates escapes; every mechanism that works enumerates
inclusions.* A preset with no installer is reported with the command the operator should run.

Nothing from a workflow, a bundle, or any published content may reach this allowlist. An install command
travelling inside published content is remote code execution by publication — the same thing the `ext::`
refusal established, pointed at a different door.

### 3. A login runs in the foreground, in the operator's own terminal

`setup` prints each provider's login command. With `--login` it runs them in the foreground, attached to
the terminal, so the vendor's own interactive flow — device code, browser, prompt, whatever it is — works
exactly as the vendor built it.

The platform's part is to know WHICH command, and nothing else. It does not read the vendor's config,
does not capture anything from the stream, does not store a token, and has no code path that could.

*Why not a device-code relay:* that was designed for a machine the operator is not attached to. Here
they are attached to it, so the vendor's own flow needs no help. The relay was solving a problem created
by the transport in decision 1's rejected alternative.

### 4. `present` becomes a state, and does not block a run by default

A provider is `missing`, `present` or `ready`. Only a vendor-specific check moves `present` to `ready`,
and only where that check is cheap and unambiguous — a `whoami`, a no-op that fails distinctly. Where
none exists the state stays `present` and says authentication was not checked.

`present` is not a refusal by default. An operator may knowingly run on a machine whose auth we cannot
verify, and a tool that blocked them would be worked around within a week. It is a refusal when asked
for (`--require-authenticated`), and it is always the truth in the report.

*Why not block:* the failure it prevents is cheap and loud — one turn, one auth error. The false refusal
it would cause is expensive and silent, and we cannot enumerate every vendor's login state.
Overclaiming and overblocking are the same error in opposite directions.

### 5. Setup reports per provider, and never reports success while anything is unmet

Three outcomes per provider — installed, already present, could not — plus whether a login is still
needed. A partially set-up machine is the ordinary case, not an error, and saying "done" over it is the
one thing that would make the command untrustworthy.

## Risks / Trade-offs

- **Installers run arbitrary vendor install scripts** → they are enumerated, they are the vendor's own
  documented installer, and they run as the operator who invoked the command on their own machine. The
  blast radius is what that operator already had.
- **Ten machines means ten invocations** → accepted, per decision 1. A shell loop is the operator's, and
  cheaper than a transport that has to be secured.
- **A vendor's login command can change** → `presetLogin` is already a small per-vendor table, and a
  wrong entry names a command that does not work, which is recoverable. An invented generic login would
  hang.
- **An authentication check can be wrong in the safe direction** → a check that cannot distinguish
  logged-out from broken must report `present`, not `ready`. The table is built by trying vendors, not by
  reasoning about them.

## Migration Plan

Additive. `join`, `run`, `status` and `leave` are untouched, a machine set up by hand stays set up, and
`setup` on a machine that needs nothing reports that and changes nothing. Rollback is removing the verb;
nothing persists that anything else reads. The one change outside the new surface is readiness gaining a
third state, which is additive on `DoctorProvider` and already half-present.

## Open Questions

- **Which vendors can answer "am I logged in?" cheaply and unambiguously?** Must be established by
  trying them. Until an entry exists a provider stays `present`.
- **Does `setup` read a specific bundle's requirements, or the platform's whole provider list?** The
  bundle-specific form is more precise and needs the resolver in the runner binary; the platform-wide
  form needs only what the runner already talks to. Starting platform-wide.
- **Is an MCP server ever installable?** An MCP server is an arbitrary program, so it is the same
  allowlist question as decision 2 and is left open. Reported, not installed.

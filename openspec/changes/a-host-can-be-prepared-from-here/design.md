## Context

Three pieces already exist and point at each other with a gap in the middle.

A bundle's manifest records `Requires` — every step's provider with its KIND (`http` / `cli` / `acp`),
every `runs-on:` label, every MCP server. `internal/bundle/require.go` checks that list against a
`Host` interface and refuses a pull that cannot run, naming every gap at once. And `wfx-runner join
--url --token --labels windows,devin` brings a machine into the pool by connecting OUT.

Between "this host cannot run it" and "this host has joined" there is nothing. The operator reads the
refusal and then goes to be at that machine.

Two constraints are already settled and this design does not get to revisit them:

**The platform never holds a `cli` provider's credential.** `provider: claude-cli` means *that
machine's* subscription; `devin` means *that machine's* seat. That is the bargain a self-hosted
Actions runner and a Jenkins node both make, and it is what makes an SSO-bound seat, a licence dongle
or an air-gapped model usable at all. `internal/bundle/require.go` records a requirement and never a
credential, asserted reflectively.

**Presence is not authentication.** `engine/doctor.go` `checkProvider` reports a `cli`/`acp` provider
ready on a PATH lookup — "no key, because the CLI holds its own credential". It already carries
`AuthUnknown` and a per-vendor `Login` string (`presetLogin`: `claude`, `gh auth login`,
`codex login`, `opencode auth login`), and prints "present; authentication not checked" rather than a
bare tick. That is honest but inert.

The relevant prior art is in this repo, pointed elsewhere: `wfx login` is an RFC 8628 device-code flow
— it prints a code and a URI, polls, and honours `slow_down` by adding five seconds. It exists because
a machine you are not sitting at cannot receive a browser callback. That is exactly the problem of
logging a CLI into a box over SSH.

## Goals / Non-Goals

**Goals:**

- An operator sitting here can take a bare machine to the point where a named bundle will actually run
  on it, and can tell the difference between "will run" and "has the binary".
- A credential reaches the far machine from its vendor, never through wfx.
- One readiness path. A far host is another implementation of the `Host` interface, not a second copy
  of the rules.
- Every remote action is a named operation an operator can read back afterwards.

**Non-Goals:**

- **Not a credential vault.** wfx does not accept, store, forward or proxy a provider credential, in
  any direction, for any convenience.
- **Not a remote shell.** An operator who wants a shell has `ssh`. A verb that runs arbitrary remote
  commands would make the allowlist below decorative.
- **Not a replacement for the outbound join.** The pull model stays exactly as it is; this adds the
  step before it. A host that can only reach out and never be reached is still a first-class host,
  prepared by hand.
- **Not configuration management.** wfx does not converge a host to a desired state, own its packages,
  or run on a schedule. It prepares a host for a bundle, once, when asked.
- **Not per-vendor auth probing for every vendor.** Only where a vendor's CLI answers cheaply and
  unambiguously. An invented probe is worse than an honest "unknown".

## Decisions

### 1. An operator connection is a separate thing from a worker, and is stored as a reference

An operator connection is an SSH target plus a name: `wfx host add build-box --ssh
deploy@10.0.0.4`. It records the target, not the means. Key material, known-hosts, jump hosts,
agent forwarding and per-host options stay in the operator's own `~/.ssh/config` and agent, and wfx
shells out to `ssh` so that every one of those already works.

*Why not an SSH library with our own key store:* because we would then own a second, worse copy of
`ssh_config`, and the first time somebody needed a bastion or a hardware key we would be implementing
OpenSSH badly. Shelling out also means the connection is auditable with tools the operator already
has, and it keeps private keys out of our process.

*Consequence:* if `ssh build-box` does not work for the operator, no wfx verb will either, and the
refusal says so in those terms rather than inventing a wfx-specific failure.

### 2. A far host implements the SAME `Host` interface the local check uses

`internal/bundle/require.go` already asks a `Host` three questions: `Provider(name)`, `HasMcp(name)`,
`LabelHolders(label)`. A remote host answers them by running the readiness check ON the far machine
and reading back a structured answer, rather than by re-deriving the rules here.

The mechanism: `wfx host doctor` copies (or finds) `wfx-runner` on the far machine and runs its
existing doctor in a JSON mode, then decodes it. The readiness rules therefore live in one place and
ship as one binary, and a host running an older binary is a version mismatch we can name rather than a
silent disagreement about what "ready" means.

*Alternative rejected:* probing from here — `ssh host command -v claude`. It works for the PATH
question and for nothing else, it re-implements `checkProvider` in shell, and it would drift from the
Go version the moment either changed.

### 3. Preparation is driven by the bundle's manifest, not by a list of things we know how to install

`wfx host prepare <host> --for acme/bug-fix@v1.2.0` resolves the reference through the SAME resolver
`use:` uses, reads `Requires`, asks the host, and acts on the difference. The unit of preparation is
therefore "this bundle on this host", which is the question an operator actually has. A bundle that
records nothing needs no preparation and the host is never questioned.

What may be installed is an **allowlist of named installers per provider preset**, not a general
package-manager escape hatch. `docs/not-now.md` already states the rule this follows: *every mechanism
that fails enumerates escapes; every mechanism that works enumerates inclusions.* A provider whose
preset we have no installer for is reported as unmet with the command the operator should run, which
is the same shape as every other refusal in this system.

*Why not accept a user-supplied install command in the workflow or bundle:* because a bundle comes
from somebody else. An install command travelling inside published content is remote code execution
with extra steps, and this repo just spent a change refusing `ext::sh -c` for that exact reason.

### 4. `wfx host login` relays the vendor's device code and touches nothing else

The operator runs `wfx host login build-box claude-cli`. wfx runs the vendor's own login command on
the far machine over SSH — `presetLogin` already knows it — streams its output, and surfaces the
device code and URI it prints. The operator opens the URI in their own browser and enters the code.
The vendor's CLI writes the credential into its own config on that machine.

What wfx does here is carry bytes in ONE direction: from the far machine's stdout to the operator's
terminal. It does not read the vendor's config, does not capture a token out of the stream, does not
store anything, and has no code path that could — asserted the way `require.go`'s no-credential rule
is asserted, on the types and on the output.

*Why this and not an API key in our env store:* for an `http` provider, our encrypted env store is
already the right answer and already exists. This verb exists precisely for the providers where that
is impossible, because the credential is a seat or a subscription belonging to a person.

*The honest limit:* a vendor whose login demands an interactive TTY with no device-code path cannot be
driven this way. That is reported as "this one you must do yourself, here is the command", not
papered over. Naming which vendors those are is a task, not an assumption.

### 5. `present but not authenticated` becomes a state, not a footnote

`DoctorProvider.AuthUnknown` exists and is printed as a note. This promotes it: a provider is
`missing`, `present` or `ready`, and only a vendor-specific check moves `present` to `ready`.

The check is per vendor and only where it is cheap and unambiguous — a `whoami`, a no-op prompt that
fails distinctly, a `--version` that differs when logged out. Where no such check exists the state
stays `present` and says so. **`present` is not a refusal by default**: an operator may knowingly run
a workflow on a host whose auth we cannot verify, and a tool that blocked them would be replaced by a
shell script within a week. It is a refusal when the operator asks for one (`--require-authenticated`),
and it is always the truth in the report.

*Why not make it a hard refusal:* because the failure it prevents is cheap and loud — one turn, one
auth error — while the false refusal it would cause is expensive and silent, and we cannot enumerate
every vendor's login state. Overclaiming and overblocking are the same error in opposite directions.

### 6. Joining is the last step of preparing, and reuses the existing command verbatim

`wfx host join <host> --labels …` fetches the join command the Workers page already generates and runs
it on the far machine. Nothing new is invented about joining; the point is that the operator does not
have to change windows between preparing a host and having it in the pool.

The label matters because a `runs-on:` label nobody holds is a step that waits forever — already an
unmet requirement in the check. So `prepare --for <bundle>` can see which labels the bundle asks for
and suggest the join it needs, closing that loop too.

## Risks / Trade-offs

- **This is remote command execution as the operator, by design** → every command is a named operation
  from an allowlist, there is no arbitrary-command verb, and the set of commands a verb may run is a
  test, not a convention. The blast radius is exactly the operator's existing SSH access, which they
  had before wfx existed.
- **A relayed device code is a phishable artifact** → wfx prints the URI the vendor's own CLI printed
  and never one of its own, so an operator who checks the domain is checking the vendor's. A wfx verb
  that displayed a wfx-branded login page for a third-party credential would be indistinguishable from
  an attack, so it is not built.
- **Shelling out to `ssh` inherits the operator's config, including things they forgot** → agent
  forwarding and `ProxyJump` are the operator's decisions and wfx neither adds nor removes them. It
  does not pass `-A`, ever.
- **A partially prepared host is a real state** → `prepare` reports what it did, what it could not, and
  what remains, per requirement; it never reports success on a partial run. A host in that state is
  the ordinary case, not an error.
- **`wfx-runner` on the far machine may be older than the one asking** → the readiness answer carries
  its version and a mismatch is named. An unversioned structured answer would let two binaries
  disagree about "ready" in silence, which is the failure this whole change is against.
- **Verb naming has already bitten us once** → `wfx import` exists and means importing a source
  repository. `host` is chosen as a noun group with no existing meaning here, and every verb under it
  must be checked against `main.go`'s dispatch before it is added.
- **A vendor's login command can change** → `presetLogin` is already a small per-vendor table, and a
  wrong entry produces a refusal naming a command that does not work, which is recoverable. An invented
  generic login would produce a hang.

## Migration Plan

Nothing is replaced, so there is nothing to migrate. Every verb is additive and the outbound join path
is untouched: a host prepared by hand today stays prepared, and a pool member never learns that this
change happened. `wfx host` with no connections configured is an empty list, not an error.

Rollback is deleting the verb group; no schema, no published artifact and no worker protocol depends on
it. The one lasting change outside the new surface is readiness gaining a third state, which is
additive on `DoctorProvider` and already half-present.

## Open Questions

- **Which vendors can answer "am I logged in?" cheaply and unambiguously?** Decision 5 depends on a
  per-vendor table that must be established by trying them, not by reasoning. Until an entry exists a
  provider stays `present`.
- **Where does an operator connection live?** A local file beside the contexts wfx already keeps, or a
  row on the host so a team shares it. The second is more useful and drags in who-may-prepare-what,
  which is an authorization question this change has not answered.
- **Does `prepare` ever install `wfx-runner` itself, or must the operator?** Installing it is the one
  bootstrap that cannot be checked by the thing being bootstrapped, and the verifying installer from
  `ship-a-gettable-binary` is the natural mechanism — that change is at 23/49 and this depends on the
  part that is done.
- **Is a MCP server ever installable?** An MCP requirement is currently reported as unmet with an
  instruction. Whether wfx should install one, given an MCP server is an arbitrary program, is the
  same allowlist question as decision 3 and is deliberately left open.

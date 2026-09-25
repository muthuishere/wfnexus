## Why

A bundle now says what it needs to run, and a pull refuses a host that cannot run it — naming every
missing provider, label and MCP server at once. That is a diagnosis with no treatment. The operator
reads "install `claude` on PATH and log into it", and then has to go and be at that machine.

Worse, the one signal they have lies by omission. `engine/doctor.go` reports a `cli` provider ready on
a PATH lookup alone — its own comment says "no key, because the CLI holds its own credential" — so a
green tick means the binary exists, not that anyone is logged in. A host can satisfy every recorded
requirement and still die on turn one with an auth error, after the run row, the worktree and the
first tokens are spent.

Today's only story for a machine that is not this one is `wfx-runner join --url --token --labels`:
the machine connects OUT, which is why it works behind NAT with no inbound port and nothing of ours
on it. That is the right trust direction and it stays. What is missing is the step BEFORE joining —
getting a bare box to the point where joining is worth doing — and that step currently happens in a
terminal nobody is watching, by hand, differently every time.

## What Changes

- A new **operator connection** to a machine over SSH, distinct from a worker: it is used to prepare
  a host, not to run steps. `wfx host add`, `wfx host ls`, `wfx host rm`.
- **`wfx host doctor <host>`** runs the SAME readiness check the receiving end already runs, on the
  far machine, over that connection. One readiness path, two places it can be asked.
- **`wfx host prepare <host> --for <bundle ref>`** reads the bundle manifest's recorded `Requires`,
  checks them against that host, installs what is missing, and reports what it cannot fix. This is
  what the `Requires` field was gathered for.
- **`wfx host login <host> <provider>`** runs the VENDOR's own login on the far machine and **relays
  its device code back to the operator's terminal**. The operator authenticates in their own browser;
  the credential is written by the vendor's CLI on that machine and never passes through wfx.
- **Readiness gains a third state.** `present but not authenticated` becomes a state a host can
  report and a workflow can be refused for, rather than a caveat printed beside a tick. This closes
  what `docs/not-now.md` records as per-vendor work, for the vendors whose CLIs can answer cheaply.
- **`wfx host join <host> --labels …`** ends the preparation by running the existing join command on
  the far machine, so a prepared host becomes a pool member without a second manual step.
- **NOT a credential vault, and not a shell.** wfx never accepts, stores or forwards a provider
  credential; every command it runs on a host is a named, auditable operation rather than an
  arbitrary remote shell. An operator who wants a shell already has `ssh`.

## Capabilities

### New Capabilities

- `operator-connection`: what an SSH connection to a host IS in this system — how it is named,
  recorded, verified and removed; how it differs from a worker's outbound join; and what it is
  forbidden from carrying.
- `host-readiness`: asking a far machine the readiness question, including the three states
  (missing / present but not authenticated / ready) and how an unauthenticated CLI is detected per
  vendor without inventing a probe we cannot trust.
- `host-preparation`: turning a bundle's recorded requirements into actions on a host — what may be
  installed automatically, what must be refused, and what a partially prepared host reports.
- `device-code-relay`: running a vendor's own login on a remote machine and relaying its device code
  to the operator, with the rule that no credential ever enters the platform stated as a testable
  requirement.

### Modified Capabilities

- `execution-requirements`: the requirement check gains the authenticated-vs-present distinction, so
  a satisfied `cli` requirement stops being reported as readiness. The recorded manifest fields do
  not change.

## Impact

- `apps/api/cmd/wfx/` — a new `host` verb group. `wfx import` ALREADY EXISTS and means importing a
  source repository, so verb naming here is deliberate and must not collide again.
- `apps/api/internal/engine/doctor.go` — `checkProvider` and `DoctorProvider` already carry
  `AuthUnknown` and a per-vendor `Login`; this makes the distinction actionable rather than a note.
- `apps/api/internal/bundle/require.go` — `Host` is already an interface, so a far machine becomes
  another implementation rather than a second readiness path.
- New: an SSH transport, and a store for operator connections. Known hosts and key material are the
  operator's, held by their own SSH agent and configuration.
- `wfx-runner`'s outbound join is unchanged. Nothing about the pull model is replaced.
- Security surface: this is remote command execution as the operator, by design. The allowlist
  discipline this repo already applies to a `use:` (`docs/not-now.md`: "every mechanism that fails
  enumerates escapes; every mechanism that works enumerates inclusions") applies to what may be run
  on a host.

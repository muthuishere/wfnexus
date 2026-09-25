## Why

A bundle records what it needs to run, and a pull refuses a host that cannot run it — naming every
missing provider, label and MCP server. That is a diagnosis with no treatment. The operator reads
"install `claude` on PATH and log into it", and then does it by hand, differently every time.

Worse, the signal lies by omission. `engine/doctor.go` reports a `cli` provider ready on a PATH lookup
alone — its own comment says "no key, because the CLI holds its own credential" — so a green tick means
the binary exists, not that anyone is logged in. A machine can satisfy every recorded requirement and
still die on turn one with an auth error.

The fix belongs **on the runner**. A machine joins by connecting out (`wfx-runner join --url --token
--labels`), which is why it works behind NAT with nothing of ours listening on it. Whoever runs that
join command is already sitting on that machine, in a terminal, with the ability to install things and
log in interactively. So the runner sets itself up. Nothing needs to reach in.

## What Changes

- **`wfx-runner setup`** — one command on the machine being set up. It reports what the platform's
  providers need, installs the CLIs it has an installer for, names what it cannot install, and says
  which providers still need a login.
- **`wfx-runner setup --join …`** accepts the same flags as `join` so setting up and joining is one
  step rather than two.
- **Logins happen in the operator's own terminal.** `setup` prints the vendor's login command and, with
  `--login`, runs it in the foreground so the vendor's own interactive flow works. The platform never
  accepts, stores or forwards a provider credential.
- **Readiness gains a third state** where a vendor can be asked cheaply: `missing`, `present`
  (authentication not checked) and `ready`. This is already half-built — `DoctorProvider.AuthUnknown`
  exists and is printed as a note.
- **NO SSH, no remote execution, no operator connection.** wfx does not reach into machines. The
  outbound join model is unchanged.

## Capabilities

### New Capabilities

- `runner-setup`: what a machine can do to prepare itself to serve a pool — what may be installed, what
  must be reported rather than installed, how a login is handled, and what a partial setup reports.

### Modified Capabilities

- `execution-requirements`: the requirement check gains the authenticated-versus-present distinction, so
  a satisfied `cli` requirement stops being reported as readiness. The recorded manifest fields do not
  change.

## Impact

- `apps/api/cmd/wfx-runner/` — a new `setup` verb beside `join`, `run`, `status`, `leave`.
- `apps/api/internal/engine/doctor.go` — `checkProvider` and `DoctorProvider` already carry
  `AuthUnknown` and a per-vendor `Login`; this makes the distinction actionable rather than a note.
- No new transport, no new store, no schema change. Nothing about joining is replaced.
- Security surface: the runner runs installers on its own machine as the operator who invoked it. The
  allowlist discipline this repo already applies to a `use:` applies to what may be installed.

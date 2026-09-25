## 1. The verb, and what it asks the platform

- [ ] 1.1 Check the verb name against `cmd/wfx-runner/main.go`'s dispatch before adding it. `wfx import`
      already exists on the CLI and means importing a source repository — the collision has bitten once.
- [ ] 1.2 Add `setup` beside `join`, `run`, `status` and `leave`, with its own flag set and a usage line.
- [ ] 1.3 `setup` asks the platform which providers it has, and their kinds, so the machine learns what to
      prepare for rather than being told by a flag. Reuse the existing `call` helper and the worker token
      when the machine has already joined; before joining, take `--url` and `--token` as `join` does.
- [ ] 1.4 Verification: `wfx-runner setup` against a test server that reports a `cli`, an `acp` and an
      `http` provider lists exactly those three with their kinds.

## 2. Readiness, on this machine, with three states

- [ ] 2.1 Report each provider as `missing`, `present` or `ready`. `present` means the binary was found and
      authentication was NOT established — a PATH hit alone is never `ready`.
- [ ] 2.2 Establish the per-vendor authentication check table BY TRYING THE VENDORS — `claude`, `codex`,
      `opencode`, `copilot`/`gh`, `devin`. For each, record in this file what was tried and whether it is
      cheap and unambiguous. Write an entry only when it is both.
- [ ] 2.3 A vendor with no such check stays `present` and says authentication was not checked. Do NOT
      invent a probe whose failure cannot be told apart from a logged-out CLI.
- [ ] 2.4 An `http` provider's readiness stays what it is today: the named environment variable is set or
      it is not. Do not read its value.
- [ ] 2.5 Verification: a fake CLI on PATH that reports authenticated, one that reports logged out, and one
      with no check at all produce `ready`, `missing`-or-`present` per its answer, and `present`.

## 3. Installing, from an allowlist

- [ ] 3.1 Define installers as an enumerated table per provider preset — data, not scattered call sites.
      `docs/not-now.md`: a mechanism that works enumerates inclusions.
- [ ] 3.2 A preset with no installer is reported not-installed with the command the operator should run. No
      package manager or shell is invoked on the operator's behalf.
- [ ] 3.3 [SEC-TEST] Nothing from a workflow, a bundle or any published content can reach this table.
      Assert it, because an install command travelling inside published content is remote code execution by
      publication — the `ext::` refusal, pointed at a different door.
- [ ] 3.4 `--dry-run` prints what would be installed and installs nothing. This is the flag people will
      actually use first on a machine they care about.
- [ ] 3.5 Verification: a preset with an installer installs (against a fake installer the test controls);
      a preset without one is reported with its instruction; `--dry-run` changes nothing.

## 4. Logging in, in the operator's own terminal

- [ ] 4.1 Print each unauthenticated provider's login command, from `engine/doctor.go` `presetLogin` —
      the table already exists, do not write a second one.
- [ ] 4.2 `--login` runs those commands in the FOREGROUND, attached to the terminal, so the vendor's own
      interactive flow works as the vendor built it. Without the flag, print and do not run.
- [ ] 4.3 [SEC-TEST] Assert no type or parameter in this path can carry a credential value, and that no
      code path reads a vendor's credential store, parses a token out of a stream, or logs one — the way
      `internal/bundle/require_test.go` asserts it reflectively.
- [ ] 4.4 A provider whose login command is unknown is reported plainly, with no guess. An invented generic
      login would hang.
- [ ] 4.5 Verification: a fake vendor CLI proves `--login` attaches and that the run is the vendor's own
      process; the no-flag path proves it prints and does not execute.

## 5. What it reports, and joining

- [ ] 5.1 Per provider: installed / already present / could not, plus whether a login is still needed.
      Never overall success while anything is unmet; never "changed" for a machine that needed nothing.
- [ ] 5.2 `--require-authenticated` makes a `present` provider an unmet requirement. Without it, `present`
      is reported and does not block — the report always states which it was.
- [ ] 5.3 `setup --join --url … --token … --labels …` accepts `join`'s flags and joins at the end, so
      preparing and joining is one invocation. Reuse `cmdJoin`; do not reimplement joining.
- [ ] 5.4 Verification: a machine missing one installable provider and one that is not gets both reported,
      the installable one installed, the overall result not success, and no join attempted on failure
      unless the operator asked for one.

## 6. Documentation

- [ ] 6.1 The runner's own usage text, and the README's worker section: `setup`, `--dry-run`, `--login`,
      and the one sentence that matters — the platform never holds a provider's credential, so a login runs
      in your terminal and is never collected.
- [ ] 6.2 Update `docs/not-now.md`: present-versus-authenticated is no longer "not now" for the vendors that
      got a table entry in 2.2, and still is for the rest. Say which are which.
- [ ] 6.3 Record what this change deliberately did NOT build and why: no SSH, no operator connection, no
      device-code relay, no configuration management. The rejected alternative is in design.md decision 1
      and is worth keeping findable, because it will be proposed again.

## 1. The operator connection, and the verb group it lives under

- [ ] 1.1 Check every verb name this change adds against `cmd/wfx/main.go`'s dispatch BEFORE writing it.
      `wfx import` already exists and means importing a source repository — the collision has bitten
      once already. Record the checked names in this file.
- [ ] 1.2 Add the `host` verb group with `add`, `ls` and `rm`. A connection is a NAME and an SSH TARGET:
      no keys, no passphrase, no known-hosts, no password field anywhere in the type.
- [ ] 1.3 [SEC-TEST] Assert the stored record has no field that could carry key material, the way
      `internal/bundle/require_test.go` asserts it reflectively — and that a reader of the store could
      not replay anything in it to obtain access.
- [ ] 1.4 Reach a host by invoking the operator's own `ssh`, so `~/.ssh/config`, their agent and
      `ProxyJump` apply unchanged. Never pass agent forwarding; assert the argv does not contain `-A`.
- [ ] 1.5 An unreachable host is reported as an SSH failure naming the target, reproducible by the
      operator with plain `ssh`. Not a wfx-specific error.
- [ ] 1.6 Verification: add, list and remove a connection against a real `sshd` the test controls, or
      against a loopback target the test can stand up; no live remote machine in the suite.

## 2. Named operations only — the allowlist, before anything uses it

- [ ] 2.1 Define the enumerated set of operations that may run on a host, as data rather than as
      scattered call sites. `docs/not-now.md`: a mechanism that works enumerates inclusions.
- [ ] 2.2 [SEC-TEST] Assert no verb accepts a command string for the far machine, and that a code path
      attempting an unenumerated operation is refused naming what it tried.
- [ ] 2.3 [SEC-TEST] Assert nothing in a bundle, workflow or any published content can reach this
      allowlist — an install command arriving by publication is remote code execution with extra steps,
      which the `ext::` refusal already established.

## 3. Readiness on a machine that is not this one

- [ ] 3.1 Teach the readiness answer a structured, VERSIONED output mode on `wfx-runner`, so the rules
      execute in one binary and one place.
- [ ] 3.2 Implement the remote `bundle.Host` by running that output mode over the connection and
      decoding it. Do NOT re-derive readiness from shell probes — `engine/doctor.go` `checkProvider`
      stays the only implementation.
- [ ] 3.3 A version mismatch between the asking binary and the answering one is reported with both
      versions, never silently interpreted.
- [ ] 3.4 `wfx host doctor <host>` prints the far machine's readiness in the shape `wfx doctor` already
      uses locally, so the two read alike.
- [ ] 3.5 Verification: the same bundle checked locally and remotely produces the same verdict for the
      same host state, asserted by running both paths against one fixture.

## 4. Present, ready, missing — making the third state real

- [ ] 4.1 Promote `DoctorProvider.AuthUnknown` from a printed note to a state the check and the API
      carry: `missing` / `present` / `ready`.
- [ ] 4.2 Establish the per-vendor authentication check table BY TRYING THE VENDORS — `claude`,
      `codex`, `opencode`, `copilot`/`gh`, `devin`. For each, record what was tried and whether it is
      cheap and unambiguous. An entry is written only when it is both.
- [ ] 4.3 A vendor with no such check stays `present` and says authentication was not checked. Do NOT
      invent a probe whose failure cannot be told apart from a logged-out CLI.
- [ ] 4.4 `present` does not block a run by default; `--require-authenticated` makes it an unmet
      requirement. Assert both, and assert the report always states which it was.
- [ ] 4.5 Surface the three states where they are read: `wfx doctor`, the requirement refusal, the API,
      and the System page and boot log — group 8 of the registry change left `AuthUnknown` in the JSON
      and not in the UI or the startup output.

## 5. Preparing a host for a bundle

- [ ] 5.1 `wfx host prepare <host> --for <bundle ref>`: resolve through the SAME resolver `use:` uses,
      read the manifest's `Requires`, ask the host, act on the difference. Report the resolved commit.
- [ ] 5.2 A bundle recording no requirements asks the host nothing and reports that there was nothing to
      do — the no-op the registry change already asserts for the local check.
- [ ] 5.3 Enumerated installers per provider preset, from group 2's allowlist. A preset with no
      installer is an unmet requirement naming the command the operator runs.
- [ ] 5.4 Per-requirement reporting: changed, could not change, remains. Never success while anything is
      unmet; never "changed" for a host that already satisfied everything.
- [ ] 5.5 A `runs-on:` label no worker holds is reported unmet with the join that would satisfy it.
- [ ] 5.6 Verification: prepare a host that is missing one provider and one label; assert both reported,
      the installable one installed, and the overall result not success.

## 6. The device-code relay

- [ ] 6.1 `wfx host login <host> <provider>` runs the VENDOR's own login command on the host —
      `engine/doctor.go` `presetLogin` already holds the table — and streams its output.
- [ ] 6.2 Present the code and URI the vendor printed, unchanged. Never a wfx-branded page or domain for
      a third-party credential: that would be indistinguishable from phishing.
- [ ] 6.3 [SEC-TEST] Bytes flow ONE way. Assert no code path parses, extracts, stores or logs
      credential-like material from the stream, and that no type or parameter in this path could carry a
      credential value.
- [ ] 6.4 A vendor whose login cannot be completed through a relayed code is reported plainly with the
      command to run by hand — never a hang, a retry loop, or an attempt to supply input.
- [ ] 6.5 Verification: a fake vendor CLI that prints a device code proves the relay; a second that
      demands a TTY proves the refusal. Both without a real vendor account.

## 7. Joining, as the last step of preparing

- [ ] 7.1 `wfx host join <host> --labels …` runs the EXISTING join command on the far machine. Invent
      nothing about joining; `wfx-runner`'s outbound model is unchanged.
- [ ] 7.2 Assert an operator connection alone puts no host in the pool, and that removing a connection
      leaves a joined worker running — it joined outward and does not depend on the connection.
- [ ] 7.3 `prepare --for <bundle>` suggests the join whose labels that bundle asks for, closing the loop
      from 5.5.

## 8. The whole loop, observed

- [ ] 8.1 End to end against a host the test controls: a bare machine, a published bundle it cannot run,
      `prepare`, `login` against a fake vendor, `join`, and then the bundle's requirement check passing —
      with every step's report asserted, not just the final state.
- [ ] 8.2 The blast radius is the operator's existing SSH access and nothing more: assert no verb widens
      it, adds forwarding, or runs as another user.
- [ ] 8.3 A host that can only reach out and never be reached is still first-class: assert the pull path
      is untouched and a hand-prepared host needs none of this.

## 9. Documentation, and the questions this change does not answer

- [ ] 9.1 README: the `host` verb group, the trust model in one paragraph (push convenience, pull trust,
      no vault), and the sentence that matters — the platform never holds a CLI's credential, so a login
      is relayed and never collected.
- [ ] 9.2 Update `docs/not-now.md`: present-vs-authenticated is no longer "not now" for the vendors that
      got a table entry in 4.2, and is still not now for the rest. Say which are which.
- [ ] 9.3 Record the open questions from design.md that remain open after implementation: where a
      connection lives (local file versus a shared row, which drags in authorization), whether `prepare`
      may install `wfx-runner` itself, and whether an MCP server is ever installable.
- [ ] 9.4 An ADR for the trust direction: why push provisioning was added without adopting a push
      execution model, and why the credential path was refused rather than encrypted.

## ADDED Requirements

### Requirement: An operator connection names an SSH target and carries no means of reaching it

The system SHALL record an operator connection as a name and an SSH target only. It SHALL NOT store
private keys, passphrases, passwords or known-hosts entries, and SHALL reach a host by invoking the
operator's own `ssh`, so that `~/.ssh/config`, the operator's agent, jump hosts and per-host options
apply unchanged.

#### Scenario: Adding a connection stores a reference, not a secret

- **WHEN** an operator adds a connection for a host
- **THEN** the stored record contains the name and the SSH target and no key material, passphrase or
  password, and nothing in the record could be replayed by a reader to obtain access

#### Scenario: A host the operator's own ssh cannot reach is refused in those terms

- **WHEN** a verb is run against a connection whose target the operator's `ssh` cannot reach
- **THEN** the refusal names the SSH target and reports the failure as an SSH failure the operator can
  reproduce with `ssh` directly, rather than as a wfx-specific error

#### Scenario: Agent forwarding is never added

- **WHEN** wfx invokes `ssh` for any verb
- **THEN** it does not pass agent forwarding, and the operator's own configuration is the only thing
  that decides whether an agent is forwarded

### Requirement: An operator connection is distinct from a worker and grants no ability to run steps

The system SHALL keep operator connections separate from pool membership. An operator connection
SHALL NOT cause a host to receive steps, and removing one SHALL NOT remove a joined worker.

#### Scenario: A connection alone does not put a host in the pool

- **WHEN** a host has an operator connection and has not joined
- **THEN** it holds no labels, receives no steps, and does not appear as a worker

#### Scenario: Removing a connection leaves a joined worker running

- **WHEN** an operator removes the connection for a host that has joined the pool
- **THEN** the worker continues to run steps, because it joined outward and does not depend on the
  connection

### Requirement: Every remote action is a named operation, and there is no arbitrary-command verb

The system SHALL run only named operations on a host, drawn from an enumerated set. It SHALL NOT
provide a verb that runs an operator-supplied or bundle-supplied command on a host.

#### Scenario: An arbitrary remote command has no verb

- **WHEN** the full set of host verbs is enumerated
- **THEN** none of them accepts a command string to run on the far machine

#### Scenario: A command outside the allowlist is refused

- **WHEN** a code path attempts to run a command on a host that is not in the enumerated set
- **THEN** it is refused, and the refusal names the operation that was attempted

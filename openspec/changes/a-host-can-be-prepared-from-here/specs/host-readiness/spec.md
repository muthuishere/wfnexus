## ADDED Requirements

### Requirement: A far host answers the same readiness question as the local one, through one implementation of the rules

The system SHALL obtain a remote host's readiness by running the platform's own readiness check ON
that host and decoding a structured answer. It SHALL NOT re-derive readiness rules from shell probes
or duplicate them for the remote case.

#### Scenario: Remote readiness uses the same interface as the local check

- **WHEN** a bundle's requirements are checked against a remote host
- **THEN** the check consumes the same host interface the local check consumes, and the readiness rules
  execute in exactly one place

#### Scenario: A version mismatch is named rather than reconciled

- **WHEN** the readiness answer comes from a platform binary of a different version than the one asking
- **THEN** the mismatch is reported with both versions, rather than the answer being interpreted as if
  the two versions agreed on what ready means

### Requirement: Readiness has three states, and presence is never reported as ready

The system SHALL report a `cli` or `acp` provider as `missing`, `present` or `ready`, where `present`
means the binary was found and authentication was not established. A PATH hit alone SHALL NOT be
reported as ready.

#### Scenario: A binary on PATH with no verified login reports present

- **WHEN** a required CLI provider's binary is found on a host and no authentication check is available
  for that vendor
- **THEN** the host reports the provider as present, states that authentication was not checked, and
  gives the command the operator runs to authenticate

#### Scenario: A vendor with a cheap check reaches ready

- **WHEN** a vendor's CLI can answer whether it is authenticated cheaply and unambiguously and answers
  yes
- **THEN** the provider is reported as ready, and readiness means a step would not fail for want of a
  login

#### Scenario: No authentication check is invented for a vendor that has none

- **WHEN** a vendor offers no cheap, unambiguous way to ask whether it is authenticated
- **THEN** the state remains present and the report says authentication was not checked, rather than a
  probe being inferred whose failure could not be distinguished from a logged-out CLI

### Requirement: Present is reported truthfully but does not block a run unless the operator asks

The system SHALL allow a run against a host whose provider is `present` rather than `ready` by
default, and SHALL refuse it when the operator explicitly requires authenticated providers.

#### Scenario: An unverifiable login does not block by default

- **WHEN** an operator runs a workflow on a host whose required CLI is present with authentication
  unchecked
- **THEN** the run proceeds and the report records that authentication was not verified

#### Scenario: The operator can demand verified authentication

- **WHEN** an operator requires authenticated providers
- **THEN** a provider that is only present is an unmet requirement and the run is refused, naming the
  provider and the login command

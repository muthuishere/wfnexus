## ADDED Requirements

### Requirement: A remote login runs the vendor's own command and relays its device code outward only

The system SHALL authenticate a `cli` or `acp` provider on a host by running that vendor's own login
command on the host and presenting the code and URI the vendor printed. Bytes SHALL flow only from the
host's output to the operator's terminal.

#### Scenario: The operator receives the vendor's code and URI

- **WHEN** an operator logs a provider in on a host and the vendor's CLI prints a device code and a
  verification URI
- **THEN** both are shown to the operator unchanged, and the operator completes authentication in their
  own browser

#### Scenario: The credential is written by the vendor on the host

- **WHEN** authentication completes
- **THEN** the credential exists only where the vendor's CLI wrote it on that host, and the platform holds
  no copy in memory, on disk, in a log or in a database

#### Scenario: The relay displays only the vendor's URI

- **WHEN** a verification URI is shown for a third-party provider
- **THEN** it is the URI the vendor's own CLI emitted, and the system never presents a login page or
  domain of its own for a third-party credential

### Requirement: The platform has no path that accepts, stores or forwards a provider credential

The system SHALL NOT read a vendor's credential store, capture a credential from a stream, or expose any
interface that accepts one for a `cli` or `acp` provider.

#### Scenario: No interface accepts a CLI provider's credential

- **WHEN** the types and interfaces involved in preparing and authenticating a host are enumerated
- **THEN** none of them has a field or parameter that could carry a credential value

#### Scenario: A token appearing in output is not retained

- **WHEN** a vendor's login output happens to contain credential-like material
- **THEN** the system does not parse, extract, store or log it, and no code path exists that would

### Requirement: A vendor that cannot be driven non-interactively is reported, not worked around

The system SHALL report a provider whose login requires interaction it cannot relay, naming the command
the operator must run themselves.

#### Scenario: A login with no device-code path

- **WHEN** a vendor's login cannot be completed through a relayed code
- **THEN** the result says so plainly and gives the operator the command to run on that host, rather than
  hanging, retrying, or attempting to supply input on the operator's behalf

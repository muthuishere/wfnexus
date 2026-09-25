## MODIFIED Requirements

### Requirement: The platform never holds a CLI provider's credential

The system SHALL NOT store, transmit, or request the credential of a `cli` or `acp` provider. A
manifest requirement for such a provider SHALL be satisfiable only by that binary being present
and authenticated on the machine that runs the step, under the operator's own account.

Where the platform helps an operator authenticate such a provider on a host, it SHALL do so only by
running the vendor's own login command on that host and relaying the code and URI the vendor printed,
outward to the operator. Assisting with authentication SHALL NOT become a path by which the platform
accepts, stores or forwards the credential.

#### Scenario: A pull reports presence and never offers to authenticate

- **WHEN** a host is missing an authenticated CLI a bundle requires
- **THEN** the system reports that the requirement is unmet and gives the command the operator runs
  themselves, and at no point accepts, stores or forwards that provider's credential

#### Scenario: A requirement check distinguishes present from authenticated

- **WHEN** a required `cli` provider's binary is present on PATH and no authentication check is
  available for that vendor
- **THEN** the requirement check reports presence only and does not report the provider as ready, because
  presence is not authentication

#### Scenario: A vendor that can be asked reaches ready

- **WHEN** a vendor's CLI can cheaply and unambiguously report that it is authenticated
- **THEN** the requirement check may report the provider ready, and readiness then means a step will not
  fail for want of a login

#### Scenario: Helping an operator log in does not put the platform in the credential path

- **WHEN** the platform assists authentication of a CLI provider on a host
- **THEN** the credential is written by the vendor's own CLI on that host, and the platform holds no copy
  and has no interface that could accept one

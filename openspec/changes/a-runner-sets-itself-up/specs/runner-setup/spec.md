## ADDED Requirements

### Requirement: A machine prepares itself, and nothing reaches into it

The system SHALL provide setup as a command run ON the machine being prepared, by the operator preparing
it. It SHALL NOT open an inbound port, accept a remote instruction to install or configure, or require a
connection from elsewhere.

#### Scenario: Setup runs locally with the operator's own privileges

- **WHEN** an operator runs setup on a machine
- **THEN** every action is taken on that machine as that operator, and no network listener is opened

#### Scenario: The outbound join is unchanged

- **WHEN** a machine that was prepared by hand joins the pool
- **THEN** it joins exactly as it did before setup existed, and needs nothing from this capability

### Requirement: Only enumerated installers may run, and an unknown one is reported with an instruction

The system SHALL install a provider only through an installer enumerated for that provider preset. It
SHALL NOT accept an install command from a workflow, a bundle, or any published content.

#### Scenario: A provider with no known installer is reported, not improvised

- **WHEN** setup finds a required provider for which no installer is enumerated
- **THEN** it reports the provider as not installed and names the command the operator should run, and no
  package manager or shell is invoked on the operator's behalf

#### Scenario: An install command arriving in published content is never executed

- **WHEN** published content carries anything resembling an install command or hook
- **THEN** it is never executed, because published content comes from somebody else

### Requirement: A login runs in the operator's own terminal and the platform holds no credential

The system SHALL authenticate a `cli` or `acp` provider by running that vendor's own login command in the
foreground, attached to the operator's terminal. It SHALL NOT read a vendor's credential store, capture a
credential from a stream, or expose any interface that accepts one.

#### Scenario: The vendor's own interactive flow is used

- **WHEN** an operator asks setup to log a provider in
- **THEN** the vendor's own login command runs attached to the terminal, and whatever flow the vendor
  implements works as the vendor built it

#### Scenario: No interface accepts a provider credential

- **WHEN** the types and parameters involved in setup are enumerated
- **THEN** none of them has a field that could carry a credential value

#### Scenario: Without the login flag the command is printed, not run

- **WHEN** setup finds a provider present but not authenticated and was not asked to log in
- **THEN** it prints the login command for the operator to run and does not run it

### Requirement: Setup reports per provider and never claims success while anything is unmet

The system SHALL report, per provider, whether it was installed, already present, or could not be
installed, and whether a login is still needed. It SHALL NOT report overall success while any provider is
unmet.

#### Scenario: Some providers installed and others not

- **WHEN** setup installs one provider and cannot install another
- **THEN** both outcomes are named separately and the overall result is not success

#### Scenario: A machine needing nothing is not reported as changed

- **WHEN** every provider is already present and authenticated
- **THEN** setup says there was nothing to do and records no change

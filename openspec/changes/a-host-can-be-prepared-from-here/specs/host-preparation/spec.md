## ADDED Requirements

### Requirement: Preparation is driven by a bundle's recorded requirements

The system SHALL prepare a host for a named bundle by resolving that bundle through the same resolver a
`use:` uses, reading the requirements its manifest records, asking the host, and acting on the
difference. A bundle that records no requirements SHALL cause no action and no question to the host.

#### Scenario: Preparing for a bundle acts only on what that bundle needs

- **WHEN** an operator prepares a host for a bundle that requires one CLI provider and one label
- **THEN** only that provider and that label are acted on, and nothing is installed for a provider the
  bundle does not name

#### Scenario: A bundle with no requirements prepares nothing

- **WHEN** an operator prepares a host for a bundle whose manifest records no requirements
- **THEN** the host is not questioned, nothing is installed, and the result says there was nothing to do

#### Scenario: A moved tag does not change what was prepared

- **WHEN** a bundle reference is resolved for preparation
- **THEN** the resolved commit is reported, so a later preparation against a moved tag is distinguishable
  from the earlier one

### Requirement: Only enumerated installers may run, and an unknown one is a refusal with an instruction

The system SHALL install a requirement only through an installer enumerated for that provider preset. It
SHALL NOT accept an install command from a workflow, a bundle, or any published content.

#### Scenario: A provider with no known installer is reported, not improvised

- **WHEN** a bundle requires a provider for which no installer is enumerated
- **THEN** the requirement is reported unmet with the command the operator should run, and no
  package manager or shell is invoked on the operator's behalf

#### Scenario: An install command travelling inside a bundle is ignored

- **WHEN** published content carries anything resembling an install command or hook
- **THEN** it is never executed, because a bundle comes from somebody else and executing its commands
  would be remote code execution by publication

### Requirement: A partial preparation reports itself as partial

The system SHALL report, per requirement, what it changed, what it could not change and what remains. It
SHALL NOT report success when any requirement remains unmet.

#### Scenario: Some requirements satisfied and others not

- **WHEN** preparation installs one missing provider and cannot satisfy a second
- **THEN** the result names both outcomes separately and the overall result is not success

#### Scenario: A host needing nothing is not reported as changed

- **WHEN** every requirement is already satisfied on the host
- **THEN** the result says so and records no change

### Requirement: A label a bundle needs is surfaced as part of preparing

The system SHALL treat a `runs-on:` label that no worker holds as an unmet requirement during
preparation, and SHALL report the join that would satisfy it.

#### Scenario: A bundle asking for a label nobody holds

- **WHEN** a bundle requires a label and no worker in the pool holds it
- **THEN** preparation reports the label as unmet and names the join that would give this host that label

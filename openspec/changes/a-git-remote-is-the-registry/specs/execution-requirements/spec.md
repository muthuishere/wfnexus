# execution-requirements

What a bundle needs in order to RUN, recorded when it is published and checked when it is
pulled — so a host that cannot run a workflow says so before a run starts rather than after the
first tokens are spent.

## ADDED Requirements

### Requirement: A published bundle records the execution requirements of its steps

At publish time the system SHALL gather, from the steps themselves, every `provider:` named and
its kind (`http`, `cli` or `acp`), every `runs-on:` label asked for, and every MCP server named,
and SHALL record them in the manifest. The manifest SHALL record a requirement and SHALL NOT
record a credential, a key, or the value of any environment variable.

#### Scenario: A step naming a CLI provider records the binary requirement

- **WHEN** a workflow whose step names `provider: devin` is published
- **THEN** the manifest records that provider with kind `acp`, so a receiving host knows a binary
  is required on the machine rather than a key in an environment

#### Scenario: A step naming an HTTP provider records the key's variable NAME only

- **WHEN** a workflow whose step names an `http` provider is published
- **THEN** the manifest records the provider and the NAME of the environment variable its key is
  read from, and never the value, which is the same rule `apiKeyEnv` follows everywhere else

#### Scenario: A placed step records its label

- **WHEN** a step carrying `runs-on: windows` is published
- **THEN** the manifest records the label `windows`, because a label nobody holds means the step
  waits indefinitely and the receiving host is the only place that can know whether it is held

### Requirement: A pull refuses a bundle this host cannot run, and names what is absent

When a bundle is pulled, the system SHALL check its recorded requirements against the receiving
host and SHALL refuse the pull when a requirement is unmet, naming each unmet requirement and
what would satisfy it. The refusal SHALL use the same vocabulary as the publish-time refusals,
because it is the same rule pointed the other way.

#### Scenario: A missing CLI is refused by name

- **WHEN** a bundle requiring `provider: devin` is pulled onto a host with no `devin` on PATH
- **THEN** the pull is refused, naming `devin` and the requirement it failed, and no run is
  created and no partial bundle is left installed

#### Scenario: A label no worker holds is refused

- **WHEN** a bundle with a step `runs-on: windows` is pulled onto an installation whose pool holds
  no worker advertising `windows`
- **THEN** the pull is refused naming the label, rather than accepting a bundle whose step would
  queue forever

#### Scenario: A satisfied requirement pulls cleanly

- **WHEN** every recorded requirement is met on the receiving host
- **THEN** the pull succeeds and the bundle is installed, with no warning about requirements

### Requirement: The platform never holds a CLI provider's credential

The system SHALL NOT store, transmit, or request the credential of a `cli` or `acp` provider. A
manifest requirement for such a provider SHALL be satisfiable only by that binary being present
and authenticated on the machine that runs the step, under the operator's own account.

#### Scenario: A pull reports presence and never offers to authenticate

- **WHEN** a host is missing an authenticated CLI a bundle requires
- **THEN** the system reports that the requirement is unmet and gives the command the operator runs
  themselves, and at no point accepts, stores or forwards that provider's credential

#### Scenario: A requirement check does not claim the CLI is authenticated

- **WHEN** a required `cli` provider's binary is present on PATH but nobody is logged into it
- **THEN** the requirement check reports presence only, and does not report the provider as ready
  in a way that implies a run will succeed — because presence is not authentication, and the
  distinction is not resolved by this change

### Requirement: Requirements are absent, not empty, for a workflow that has none

A workflow whose steps name no provider, no label and no MCP server SHALL publish and pull with no
requirement checking at all, and the absence SHALL NOT be reported as a warning.

#### Scenario: A deterministic workflow needs nothing

- **WHEN** a workflow of `run:` steps only is published and pulled
- **THEN** no requirements are recorded, no check runs, and a single-machine installation sees
  nothing about providers, labels or workers

## ADDED Requirements

### Requirement: The client installs the server or a runner where it is told

The system SHALL install `wfx-server` or `wfx-runner` locally, over ssh, or onto Kubernetes, through
the existing checksum-verified install script, the published images, or the Helm chart, and SHALL
print every command before it runs it.

#### Scenario: Remote binary install

- **WHEN** an operator runs `wfx install server --to ssh://ops@box`
- **THEN** the client runs install.sh with `WFX_BIN=wfx-server` on that host over ssh, and prints the `wfx context add … --via` line to reach it

#### Scenario: Secrets are passed by name

- **WHEN** an image install passes carry or AWS configuration
- **THEN** it is passed as `-e NAME` and no value appears in the command

#### Scenario: Dry run

- **WHEN** `--dry-run` is given
- **THEN** the plan is printed and nothing is run

## ADDED Requirements

### Requirement: Servers are named profiles the client switches between

The system SHALL keep named server profiles (URL, optional SSH tunnel, credential) and SHALL send
every command to the current one. It SHALL provide `wfx profile create|use|switch|ls|current|rm` and
keep `wfx context` working on the same records.

#### Scenario: Switching
- **WHEN** an operator runs `wfx profile switch dev`
- **THEN** `wfx profile current` prints `dev` and the next command talks to dev's server

#### Scenario: Provision and register
- **WHEN** an operator runs `wfx profile create box --install ssh://ops@box --compose`
- **THEN** the compose plan runs on box over ssh, a profile `box` → `http://127.0.0.1:8090` via `ssh://ops@box` is saved and made current, and the device login starts

#### Scenario: Dry run registers nothing
- **WHEN** `--dry-run` is given to `wfx profile create … --install …`
- **THEN** the plan is printed and no profile is written

### Requirement: Runners are provisioned outside profiles

The system SHALL provision a runner with `wfx worker install --to …`, and `wfx install server|runner`
SHALL fail with a message naming the new commands.

#### Scenario: The old spelling
- **WHEN** an operator runs `wfx install server --to local`
- **THEN** the command fails and names `wfx profile create … --install` and `wfx worker install`

### Requirement: The CLI installer installs the agent skills

install.sh and install.cmd SHALL run `wfx install --skills` after installing `wfx`, SHALL skip it for
wfx-server and wfx-runner or when `WFX_SKILLS=0` or `--no-skills` is given, SHALL print one line
naming the count and the locations, and SHALL warn but still succeed when the skills step fails.

#### Scenario: Opt out
- **WHEN** install.sh runs with `WFX_SKILLS=0`
- **THEN** no skill directory is written

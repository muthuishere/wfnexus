## ADDED Requirements

### Requirement: Tools, config and env defaults are managed through the server

The system SHALL let an authenticated operator add and remove tools, put/get/list/remove config files,
set/unset env defaults, list history and roll back, for the server's carry state or a named worker's,
through the wfx API, with the server running carry using its own credentials.

#### Scenario: Adding a tool on a worker

- **WHEN** an operator runs `wfx tool add jq@1.7.1 --on worker-1`
- **THEN** the server runs `carry tool add worker-1 jq@1.7.1` and reports the new version

#### Scenario: carry is not configured

- **WHEN** the server was started without `CARRY_REPO`
- **THEN** every carry route that needs carry answers 409 with a message naming `CARRY_REPO`

### Requirement: Env values are never shown

The system SHALL list env defaults by name only and SHALL pass values to carry on stdin, never argv.

#### Scenario: Listing env defaults

- **WHEN** an operator runs `wfx config env ls`
- **THEN** only names are printed

### Requirement: Configuration moves between places

The system SHALL copy config files and env defaults from a context or a local folder to another
context or local folder, additively, without moving env-store secrets.

#### Scenario: Server to server

- **WHEN** an operator runs `wfx sync --from dev --to prod`
- **THEN** every config file and env default of dev's state is written to prod's state, and nothing is deleted

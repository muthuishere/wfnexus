## ADDED Requirements

### Requirement: Skills and MCP servers are added to the current profile's server

The system SHALL provide `wfx skill add <name|git-url|path>|rm|ls` and
`wfx mcp add <name> [--set k=v] [--env NAME]|rm|ls` against the current profile's server, and SHALL
require `admin:write` for the writes.

#### Scenario: A local skill folder
- **WHEN** an operator runs `wfx skill add ./my-skill`
- **THEN** the folder (without hidden files) is uploaded under its SKILL.md name and the skill is in the server's registry when the command returns

#### Scenario: MCP credential by name
- **WHEN** an operator runs `wfx mcp add gh --set command="npx -y gh-mcp" --env GITHUB_TOKEN`
- **THEN** mcp.json holds `envFrom: ["GITHUB_TOKEN"]` and no value, and a step using `gh` gets the env-store value in the server's environment

#### Scenario: A credential-looking literal
- **WHEN** a `--set env.X=` or `--set header.X=` value looks like a key, or `--env NAME=value` is given
- **THEN** it is refused and the message does not contain the value

### Requirement: Package changes are versioned and roll back

The system SHALL record every skill or MCP change as one carry version of the server's state, and
`wfx config rollback <n>` on the server's state SHALL restore those skills and MCP servers on disk and
reload them.

#### Scenario: Carry is off
- **WHEN** carry is not configured
- **THEN** the change applies and the command says it was not versioned, and why

### Requirement: The catalog reloads without a restart

The server SHALL reload skills, MCP servers and providers when their files change on disk, keep the
previous catalog when a reload fails, and leave a step's already-built toolkit untouched.

#### Scenario: Edited by hand
- **WHEN** a skill directory or mcp.json changes on disk
- **THEN** within `WFX_CATALOG_POLL` seconds the registry reflects it, with no API call

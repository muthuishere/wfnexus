## ADDED Requirements

### Requirement: Every finished run leaves a memory record

The system SHALL, when a run reaches `done`, `failed` or `cancelled`, write one record for that run's
(project, workflow) containing the input, the outcome, each step's status with a truncated output or
error, and every human resolution with actor and reason, unless the workflow sets `memory: false`.
A failure to write memory SHALL NOT change the run's status.

#### Scenario: A failed run is remembered

- **WHEN** a run fails at step `fix` with an error
- **THEN** the workflow's memory holds a record for that run naming `fix` and the error

#### Scenario: Opt-out

- **WHEN** a workflow sets `memory: false`
- **THEN** no record is written and no memory is loaded into its steps

### Requirement: Memory is loaded into the next run by default

The system SHALL render the workflow's memory into a bounded section titled "What previous runs of this
workflow learned", expose it as `{{ .Memory }}`, and append it to every agent step's prompt that does
not reference `.Memory` itself. The section SHALL be newest-first and SHALL NOT exceed the budget
(default 1500 tokens, 4 characters per token).

#### Scenario: Run two sees run one

- **WHEN** a workflow runs twice
- **THEN** the second run's agent prompt contains the first run's record

#### Scenario: Budget truncation

- **WHEN** stored memory exceeds `budget`
- **THEN** the section is cut to the budget and says it was truncated

### Requirement: Other workflows' memory is opt-in

The system SHALL load another workflow's memory only when `memory.include` names it: `workflow:<name>`
(same project), `workflow:<project>/<name>`, `project`, or `global`. A step-level `memory:` SHALL narrow
or disable the workflow's setting and SHALL NOT enable memory a workflow disabled.

#### Scenario: Include a sibling workflow

- **WHEN** workflow B declares `memory: { include: [workflow:A] }`
- **THEN** B's steps see A's records under "From workflow A"

### Requirement: Memory never holds a secret

The system SHALL scrub every value the run redacts and every env-store value before writing a record or
a lesson.

#### Scenario: A secret in a step output

- **WHEN** a step's output contains an env-store value
- **THEN** the stored record contains the redaction marker, not the value

### Requirement: Memory is bounded and inspectable

The system SHALL keep at most `retain` raw run records per workflow (default 20), folding older ones into
one rolling digest, and SHALL let an operator list, show and forget memory via REST, `wfx memory`, and
the workflow page. A dry run SHALL show the memory a run would load.

#### Scenario: Retention folds old runs

- **WHEN** a workflow with `retain: 2` finishes a third run
- **THEN** two run records remain and the oldest is summarised in the digest

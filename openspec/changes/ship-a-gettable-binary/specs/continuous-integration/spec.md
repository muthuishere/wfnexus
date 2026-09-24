# continuous-integration

What runs on push and pull request, what merging is gated on, and the build invariants continuous
integration must not be able to route around. This repository has no continuous integration
today, so every requirement here is first contact.

## ADDED Requirements

### Requirement: Every push and pull request runs the full check suite

Continuous integration SHALL run, on every push and every pull request, the complete set of
checks the repository defines as `check`: the Go test suite, `go vet`, a formatting check, a
cross-compilation of every target claimed as supported, the UI type-check and production build,
and validation of every shipped workflow file against the live registries. It SHALL additionally
run the race-detector suite over the concurrent packages and the end-to-end UI suite against a
built binary.

#### Scenario: A pull request runs everything

- **WHEN** a pull request is opened or updated
- **THEN** the test suite, vet, the formatting check, the cross-compilation, the UI type-check
  and build, workflow validation, the race suite and the end-to-end suite all run

#### Scenario: A formatting violation fails the build

- **WHEN** a commit contains a Go file that is not gofmt-clean
- **THEN** the run fails

#### Scenario: An invalid shipped workflow fails the build

- **WHEN** a shipped workflow file does not validate against the registries
- **THEN** the run fails

#### Scenario: No external service is required

- **WHEN** the check suite runs on a clean runner
- **THEN** it completes with no database service, no object store, no model provider and no
  credential of any kind

### Requirement: Merging to the default branch is gated on a green run

The default branch SHALL be protected such that a pull request cannot merge while the required
run is failing or has not completed. The gate SHALL be a repository setting, not a convention.

#### Scenario: A red run blocks the merge

- **WHEN** the required check fails on a pull request
- **THEN** the pull request cannot be merged

#### Scenario: A tag does not bypass the gate

- **WHEN** a release tag is pushed
- **THEN** the full check suite runs again before any artifact is produced, and a failure stops
  the release

### Requirement: Checks run against the pinned dependency set, not the local workspace

The repository contains a Go workspace whose second module deliberately builds against an
unreleased upstream. Continuous integration SHALL build and test the server module against the
versions pinned in its own module file, with the workspace disabled, so that a green run proves
which code it ran against. The spike module SHALL NOT be part of continuous integration.

#### Scenario: The workspace does not influence the run

- **WHEN** the check suite runs
- **THEN** it resolves dependencies from the server module's pinned versions and not from the
  workspace

#### Scenario: Spikes are excluded

- **WHEN** the check suite runs
- **THEN** the spike module is neither built nor tested

### Requirement: The platforms the product claims are exercised, not only compiled

Because a step runs as a subprocess in a working directory, path and process behaviour differs by
operating system. The test suite SHALL run on Linux, macOS and Windows runners. The remaining
checks MAY run on Linux only.

#### Scenario: The suite runs on three operating systems

- **WHEN** the check suite runs
- **THEN** the Go test suite has executed on Linux, on macOS and on Windows

#### Scenario: A platform-specific failure is caught

- **WHEN** a change breaks path handling on Windows only
- **THEN** the Windows leg fails and the merge is blocked

### Requirement: Continuous integration cannot bypass the embedded-asset staging

Every server binary produced anywhere SHALL be produced through the asset staging step, which
builds the UI and copies the UI bundle, templates, skills and provider registry into the embedded
directory, and which fails when the built UI is absent. No job SHALL build a server binary by a
route that skips it.

#### Scenario: A missing UI build fails the job

- **WHEN** the UI build produces no index file and a server binary build is attempted
- **THEN** the job fails with the stated message rather than producing a binary that serves a
  missing page

#### Scenario: No job compiles the server directly

- **WHEN** the workflow definitions are inspected
- **THEN** every step that produces a server binary invokes a target that depends on asset
  staging, and none invokes the compiler against the server package directly

### Requirement: The paired-migration convention is enforced by the build

A migration in the primary dialect without its counterpart in the second dialect SHALL fail the
run, as SHALL a migration missing either its up or its down file. This convention is currently
enforced only by review.

#### Scenario: An unpaired migration fails

- **WHEN** a migration is added for one dialect only
- **THEN** the run fails and names the missing file

#### Scenario: A migration without a down file fails

- **WHEN** a migration is added with an up file and no down file
- **THEN** the run fails and names the missing file

# binary-versioning

A build knows and reports its own identity: semver tags, the version injected at build time,
`wfx version`, the rule for a build made outside a tag, and the version a running server reports.

## ADDED Requirements

### Requirement: Releases are semantic versions on git tags

A release SHALL be identified by a git tag of the form `vMAJOR.MINOR.PATCH` following Semantic
Versioning 2.0.0. The tag SHALL be the only trigger that produces a published release. A tag,
once pushed, SHALL NOT be moved or deleted; a defective release is superseded by a new patch tag.

While the HTTP API envelope and the workflow YAML schema are still expected to change
incompatibly, the major version SHALL remain `0`.

#### Scenario: A tag is the only release trigger

- **WHEN** a commit is pushed to any branch without a tag
- **THEN** no release is published and no artifact is uploaded anywhere

#### Scenario: A defective release is superseded, not moved

- **WHEN** a published release is found to be defective
- **THEN** a new tag with a higher patch version is pushed
- **AND** the defective tag is left in place, unmoved and undeleted

### Requirement: Every binary reports its own version

`wfx`, `wfx-server` and `wfx-runner` SHALL each report the version they were built from. `wfx`
SHALL expose it as a `version` subcommand listed in its usage output, and all three SHALL accept
a `--version` flag. The reported value SHALL include the version, the commit it was built from,
and the build date.

#### Scenario: The client reports its version

- **WHEN** `wfx version` is run
- **THEN** the output states the version, the commit and the build date
- **AND** the command exits zero without contacting any server

#### Scenario: The version verb is discoverable

- **WHEN** `wfx help` is run
- **THEN** the usage output lists the `version` verb

#### Scenario: All three binaries answer --version

- **WHEN** `--version` is passed to `wfx`, to `wfx-server` and to `wfx-runner`
- **THEN** each prints its version and exits zero

### Requirement: A build made from a tag reports that tag exactly

A binary built from a tagged commit SHALL report that tag verbatim. A binary built from any other
commit SHALL NOT report a bare release version; it SHALL report a value that identifies it as an
untagged build, including the commit, and SHALL mark a build made from a modified working tree.

#### Scenario: A tagged build reports the tag

- **WHEN** the release build runs on tag `v0.1.0`
- **THEN** the resulting `wfx version` output reports `v0.1.0`
- **AND** the release job fails if it does not

#### Scenario: A build from an untagged commit does not claim a release

- **WHEN** a binary is built from a commit that is not a tag
- **THEN** its reported version identifies the commit and is distinguishable from any released
  version

#### Scenario: A build from a modified tree says so

- **WHEN** a binary is built while the working tree has uncommitted changes
- **THEN** its reported version is marked as modified

### Requirement: A binary installed with the Go toolchain still reports a version

A binary obtained with `go install <module path>@<version>` is not built by this project's build
system and receives no injected version. Such a binary SHALL still report a version, derived from
the module version and revision recorded by the Go toolchain, rather than reporting an unknown or
empty value.

#### Scenario: go install at a version

- **WHEN** `wfx` is installed with `go install …/cmd/wfx@v0.1.0` and `wfx version` is run
- **THEN** the output reports `v0.1.0`

#### Scenario: go install at latest

- **WHEN** `wfx` is installed with `go install …/cmd/wfx@latest`
- **THEN** the output reports the resolved module version and the revision, and never an empty
  or unknown version

### Requirement: A running server and a joined worker expose their version remotely

The server's health endpoint SHALL include the server's version, so that an operator with no
shell access on the host can identify the build. A worker SHALL report its version when it joins,
so that a worker running an older build than the server is visible.

#### Scenario: The health endpoint carries the version

- **WHEN** the health endpoint is requested from a running server
- **THEN** the response includes the server's version
- **AND** existing fields of that response are unchanged

#### Scenario: A worker's version is visible after join

- **WHEN** a worker joins a server
- **THEN** the worker's reported version is stored and visible alongside the worker's labels

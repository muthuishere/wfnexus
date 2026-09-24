# remote-use

A `use:` entry that names a git repository and a ref, in the grammar GitHub Actions and Go
modules already use, and what it means when it cannot be resolved.

## ADDED Requirements

### Requirement: A local `use:` keeps working unchanged

A `use:` value that names no host and carries no ref SHALL be resolved against the local task
registry exactly as before. Resolving such a workflow SHALL make no git call and no network
request, and SHALL create no cache.

#### Scenario: A workflow with only local uses is unaffected

- **WHEN** a workflow whose every `use:` is a bare task name is loaded
- **THEN** each one expands from the local task registry, no git process is started, and no
  cache directory is created

#### Scenario: An unknown local task still fails at load time

- **WHEN** a workflow names a local task that does not exist
- **THEN** the load fails naming the unknown task and listing the known ones, as before, and the
  reference is not interpreted as a remote

### Requirement: A remote reference names a repository and a ref

A `use:` value SHALL be read as a remote reference when it contains a `@` separating a path from
a ref. The system SHALL resolve the path to a git remote by these rules and no others: a path
whose first segment contains no dot SHALL resolve to `github.com`; a path whose first segment
contains a dot SHALL treat that segment as the host; a value carrying a URL scheme SHALL be
passed to git verbatim as the remote. There SHALL be no configurable default host.

#### Scenario: A two-segment path resolves to GitHub

- **WHEN** a workflow declares `use: acme/bug-fix@v1.2.0`
- **THEN** the reference resolves to the repository `acme/bug-fix` on `github.com` at ref `v1.2.0`

#### Scenario: A dotted first segment names its own host

- **WHEN** a workflow declares `use: git.acme.internal/team/wf@v1.2.0`
- **THEN** the reference resolves to the repository `team/wf` on host `git.acme.internal`, and no
  request is made to github.com

#### Scenario: An explicit URL is used as given

- **WHEN** a workflow declares a reference carrying a scheme, such as an `ssh://` or `file://` URL
  followed by `@<ref>`
- **THEN** the URL is used as the git remote verbatim and the text after the final `@` is the ref

#### Scenario: There is no default-host setting

- **WHEN** the resolution of a reference is inspected for configuration inputs
- **THEN** no setting, environment variable or config file can change which host a given reference
  resolves to

### Requirement: A ref may be a tag, a branch or a commit

A reference's ref SHALL be accepted as a tag, a branch or a commit SHA, and SHALL be resolved by
git. The resolution SHALL record the commit SHA the ref resolved to.

#### Scenario: A tag resolves and records its commit

- **WHEN** a reference pinned to a tag is resolved
- **THEN** the resolution succeeds and records the reference text, the remote and the commit SHA
  the tag pointed at

#### Scenario: A commit SHA resolves to itself

- **WHEN** a reference pinned to a commit SHA is resolved
- **THEN** the recorded commit SHA is that SHA and no ref lookup changes it

### Requirement: A remote reference is resolved at load time

A remote `use:` SHALL be resolved, fetched and expanded before the workflow's steps are
validated, so that every step produced by a remote reference is checked exactly as a
hand-written one. A remote reference SHALL NOT be resolved during a run.

#### Scenario: Expanded steps are validated like hand-written ones

- **WHEN** a workflow with a remote `use:` naming a step with an invalid declaration is loaded
- **THEN** the load fails with the same validation error a hand-written step would produce

#### Scenario: No resolution happens mid-run

- **WHEN** a workflow containing a remote `use:` is run
- **THEN** every reference was resolved before the run started and no resolution is attempted
  while steps execute

### Requirement: An unresolvable remote reference fails loudly

When a remote reference cannot be resolved — the remote is unreachable, the ref does not exist,
or the repository holds no matching bundle — the load SHALL fail naming the reference and the
remote. The system SHALL NOT fall back to a local task of the same name, SHALL NOT skip the
reference, and SHALL NOT run the workflow in a degraded form.

#### Scenario: An unreachable remote is a load error

- **WHEN** a workflow with a remote `use:` is loaded with the cache cold and the remote
  unreachable
- **THEN** the load fails, the error names the reference and the remote, and no run starts

#### Scenario: A local task of the same name is not substituted

- **WHEN** a remote reference fails to resolve and a local task shares its trailing name
- **THEN** the local task is not used and the failure stands

### Requirement: A nested remote reference is refused

A bundle fetched by a remote `use:` whose own workflow declares a further remote `use:` SHALL be
refused at resolution with a message stating that transitive remote references are not supported.
The system SHALL NOT resolve such a reference partially.

#### Scenario: A transitive reference is named and refused

- **WHEN** a fetched bundle's workflow itself declares a remote `use:`
- **THEN** the load fails naming both references and stating that transitive remote references
  are not supported, and nothing from the nested reference is resolved or cached

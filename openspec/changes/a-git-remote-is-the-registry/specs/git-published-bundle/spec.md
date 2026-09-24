# git-published-bundle

A published bundle as files in a git repository: the layout, what is committed, what publishing
refuses, and the provenance a commit and a tag carry.

## ADDED Requirements

### Requirement: A published bundle is a readable tree in the repository

Publishing to a git remote SHALL write the bundle as a directory of files under a path naming the
bundle and its version, containing the manifest, the workflow, the MCP declarations it carries if
any, and each carried skill as a directory of its own files. The bundle SHALL NOT be committed as
an archive or any other opaque single file.

#### Scenario: A reviewer can read what changed

- **WHEN** a bundle is published to a git remote and the resulting commit is diffed
- **THEN** the diff shows the workflow's text, the manifest's fields, and each skill's files as
  ordinary readable changes, and contains no binary archive

#### Scenario: The tree carries every dependency

- **WHEN** a published bundle's directory is inspected
- **THEN** it contains the manifest, the workflow, each named skill's content, and the MCP
  declarations the steps name

#### Scenario: Two versions coexist

- **WHEN** a second version of the same bundle is published to the same repository
- **THEN** both version directories exist and the earlier version's files are unchanged

### Requirement: The committed tree and the packed archive describe the same bundle

The manifest SHALL describe the committed tree and the packed archive identically: one manifest,
one set of entry digests, one bundle digest. Packing the committed tree SHALL produce a bundle
whose digests equal those recorded in the committed manifest.

#### Scenario: Packing the tree reproduces the recorded digests

- **WHEN** a bundle's committed tree is packed
- **THEN** every entry digest and the bundle digest equal the values recorded in the committed
  manifest

#### Scenario: Ordering and timestamps do not change a digest

- **WHEN** the same content is packed with different file ordering, modification times or
  permissions
- **THEN** the computed digests are unchanged

### Requirement: Publishing resolves dependencies on the publishing machine

Publishing to a git remote SHALL resolve, on the publishing machine and before anything is
committed, every skill and MCP declaration the workflow's steps name, and SHALL carry their
content into the bundle with each one's digest. The published bundle SHALL NOT carry unresolved
references.

#### Scenario: Skills and MCP declarations travel with the workflow

- **WHEN** a workflow naming skills and MCP servers is published to a git remote
- **THEN** the committed tree contains each named skill's content and each MCP declaration, and
  the manifest records a digest for each

#### Scenario: An unresolvable reference fails the publish

- **WHEN** a workflow naming a skill absent from the publishing machine is published
- **THEN** the publish is refused, the error names the reference and the locations searched, and
  no commit is created and nothing is pushed

### Requirement: Publishing to a git remote refuses a literal credential

Publishing to a git remote SHALL apply the existing literal-credential rule unchanged to every
field meant to hold the name of an environment variable, and SHALL refuse a value longer than 64
characters or containing any byte outside `[A-Za-z0-9_]`. The refusal SHALL occur before any
commit is created, and the refusal message SHALL NOT contain the offending value.

#### Scenario: A pasted key never reaches a commit

- **WHEN** a workflow whose provider names a key-environment field holding a value with
  punctuation or over 64 characters is published to a git remote
- **THEN** the publish is refused, no commit is created, nothing is pushed, and the working clone
  contains no file holding that value

#### Scenario: The refusal does not echo the value

- **WHEN** a publish is refused for a literal credential
- **THEN** neither the error message nor any log line contains the refused value

#### Scenario: A legitimate variable name publishes

- **WHEN** a workflow naming an environment variable such as `OPENAI_API_KEY` is published
- **THEN** the publish succeeds and the committed bundle carries the name, not a value

### Requirement: A published version is immutable and immutability is enforced by the remote

Publishing a bundle name and version that the remote already carries SHALL be refused rather than
overwritten, including when the content is byte-identical. The refusal SHALL be produced by the
remote rejecting the push, not only by a local check. A moving tag MAY point at a version; a
version SHALL NOT move.

#### Scenario: Republishing an existing version is refused

- **WHEN** a bundle name and version already present on the remote is published again
- **THEN** the push is rejected, the error names the existing version, and the remote's content
  for that version is unchanged

#### Scenario: A new version is accepted

- **WHEN** a bundle already published at one version is published at a higher version
- **THEN** both versions exist on the remote, each addressed by its own digest

#### Scenario: A tag moves but a version does not

- **WHEN** a moving tag is repointed from one version to a newer one
- **THEN** the tag resolves to the newer version and each version directory's content is unchanged

### Requirement: Publishing uses the user's existing git credentials and stores none

Publishing to a git remote SHALL authenticate using git's own credential mechanisms. The system
SHALL NOT read, prompt for, store, log or transmit a git credential, and SHALL NOT require an
account on any wfnexus server.

#### Scenario: No wfnexus account is needed to publish to a remote

- **WHEN** a user with no wfnexus login and no configured host publishes to a git remote
- **THEN** the publish succeeds and no login is requested

#### Scenario: No credential is captured

- **WHEN** a publish to a git remote completes
- **THEN** no credential value appears in any file written by the system, in its output, in its
  logs, or in the arguments of any process it starts

### Requirement: Provenance is the commit, the tag and the signature

A published bundle's provenance SHALL be the commit that introduced it — its author, committer,
date and message — together with the tag naming the version and any signature the publisher's git
configuration applied. The system SHALL record what git reports and SHALL NOT claim
tamper-evidence for an unsigned bundle.

#### Scenario: Provenance is queryable from the repository alone

- **WHEN** a published bundle's provenance is asked for with no wfnexus server available
- **THEN** the commit's author, date and the version tag answer it

#### Scenario: A signature is reported, not enforced

- **WHEN** a bundle published with a signed tag and a bundle published without one are both
  resolved
- **THEN** the signature state of each is reported as git reports it, and neither resolution is
  refused on that basis

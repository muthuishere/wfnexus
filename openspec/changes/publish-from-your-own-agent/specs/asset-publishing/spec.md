# asset-publishing

Pushing a workflow and its dependencies to a host as an immutable, content-addressed
bundle, and what is refused (ADR 0018).

## ADDED Requirements

### Requirement: A published workflow is a bundle whose dependencies are resolved at publish time

Publishing a workflow SHALL resolve, at publish time and on the publishing machine, every
skill the workflow's steps name and every MCP server declaration it uses, and SHALL carry
them inside the published bundle. The bundle SHALL NOT carry unresolved references to be
resolved on the receiving machine. The bundle SHALL record the content digest of each
skill and each MCP declaration it carries.

#### Scenario: Skills and MCP declarations travel with the workflow

- **WHEN** a workflow whose steps name skills and MCP servers is published
- **THEN** the stored bundle contains the content of each named skill and each MCP
  declaration, plus the digest of each

#### Scenario: Nothing is resolved at pull time

- **WHEN** a bundle is pulled onto a machine that has never held any of the skills it names
- **THEN** the pull requires no lookup of those skills on that machine and the bundle is
  complete after the pull

#### Scenario: A carried skill differing from a local one is reported

- **WHEN** a bundle is pulled onto a machine that already holds a skill of the same name
  with a different digest
- **THEN** the client reports both digests and names the skill, rather than silently
  preferring either

### Requirement: A bundle is content-addressed and immutable

A bundle SHALL be addressed by the digest of its content. Publishing a name and version
that already exists SHALL be refused rather than overwritten. Republishing the same name
SHALL produce a new version; an existing version's content SHALL NOT change. A moving tag
MAY point at a version; a version SHALL NOT move.

#### Scenario: Republishing the same version is refused

- **WHEN** `name@version` is published a second time, with identical or different content
- **THEN** the publish is refused, the stored bundle for that version is unchanged, and the
  error names the existing version

#### Scenario: Republishing the same name yields a new version

- **WHEN** a workflow already published as `name@1.0.0` is published again as `name@1.1.0`
- **THEN** both versions exist, each addressed by its own digest, and `name@1.0.0` resolves
  to the same content as before

#### Scenario: The digest identifies the content

- **WHEN** the same bundle content is published under two names
- **THEN** both resolve to the same content digest

#### Scenario: A tag moves but a version does not

- **WHEN** a moving tag is repointed from one version to a newer one
- **THEN** the tag resolves to the newer version and each version continues to resolve to
  its original content

### Requirement: Publishing refuses a literal credential

Publishing SHALL apply the existing literal-credential rule unchanged: a field that is
meant to hold the NAME of an environment variable SHALL be refused when its value is
longer than 64 characters or contains any byte outside `[A-Za-z0-9_]`. This SHALL apply to
provider and classifier key-environment fields and to step environment declarations
carried in the bundle. The refusal SHALL happen at publish time, before anything is
stored, and the refusal message SHALL NOT contain the offending value.

#### Scenario: A pasted key is refused at publish time

- **WHEN** a workflow is published whose provider names an environment variable field
  holding a value with punctuation or over 64 characters
- **THEN** the publish is refused, no bundle is stored, and the error states that the field
  must hold the name of an environment variable

#### Scenario: The refusal does not echo the value

- **WHEN** a publish is refused for a literal credential
- **THEN** neither the error message nor any log line contains the refused value

#### Scenario: A legitimate variable name publishes

- **WHEN** a workflow naming an environment variable such as `OPENAI_API_KEY` is published
- **THEN** the publish succeeds and the bundle carries the name, not a value

### Requirement: Publishing refuses an unresolvable reference or a digest mismatch

Publishing SHALL fail when any skill or MCP declaration the workflow names cannot be
resolved on the publishing machine, and when the content of a resolved dependency does not
match the digest recorded for it. Every such failure SHALL occur at publish time; a bundle
that was accepted SHALL NOT fail for these reasons at run time on the receiving machine.

#### Scenario: An unknown skill fails the publish

- **WHEN** a workflow naming a skill that is not present on the publishing machine is
  published
- **THEN** the publish is refused, the error names the unresolvable skill, and no bundle is
  stored

#### Scenario: A digest mismatch fails the publish

- **WHEN** a dependency's content does not hash to the digest recorded for it
- **THEN** the publish is refused, the error names the dependency and both digests, and no
  bundle is stored

#### Scenario: A stored bundle does not fail for these reasons at run time

- **WHEN** a bundle that was accepted at publish time is pulled and run on another machine
- **THEN** the run does not fail for an unresolvable skill or MCP reference, nor for a
  digest mismatch on a carried dependency

### Requirement: A pulled bundle runs identically on another machine

A bundle pulled to a different machine SHALL run with the same step declarations, the same
resolved skills, the same MCP declarations, and the same output contracts as on the
machine it was published from. Differences in which skills that machine has installed
locally SHALL NOT change what the bundle runs. Where the bundle's declared execution
backend is present on both machines and the same, the run SHALL produce the same sequence
of step declarations submitted to that backend.

This is the portability claim; it SHALL be asserted by a test that publishes on one
machine, pulls on a second that holds none of the dependencies, and compares the runs.

#### Scenario: A bundle runs on a machine holding none of its skills

- **WHEN** a bundle is pulled onto a machine with an empty skill set and run
- **THEN** the run executes every step with the skills the bundle carries and completes

#### Scenario: A conflicting local skill does not change the run

- **WHEN** the receiving machine holds a different skill of the same name as one the bundle
  carries
- **THEN** the run uses the bundle's carried skill and its behaviour is unchanged

#### Scenario: The same bundle produces the same step declarations on both machines

- **WHEN** the same bundle is run on the publishing machine and on the receiving machine
  against the same execution backend
- **THEN** the prompts, scoped skills and tools, and output contracts submitted for each
  step are identical

### Requirement: Nothing is published without an authenticated subject, and the bundle records who published it

Publishing SHALL require an authenticated subject and SHALL record that subject and the
time of publication on the stored bundle as provenance. Provenance is a recorded publisher,
not a cryptographic signature.

Publishing SHALL be absent, not merely unused, at the one-person scale: a host with no
registry configured SHALL boot and run workflows from disk as before, and a publish
invocation with no configured host SHALL be an error rather than a degraded or local-only
mode.

#### Scenario: Publishing with no authenticated subject is refused

- **WHEN** a publish request arrives with no valid subject
- **THEN** it is refused and no bundle is stored

#### Scenario: The bundle records its publisher

- **WHEN** an authenticated subject publishes a bundle
- **THEN** the stored bundle records that subject and the publication time, and both are
  returned when the bundle is inspected

#### Scenario: Publishing is absent with no host configured

- **WHEN** `wfx publish` is run with no host context configured
- **THEN** the command exits with an error stating that no host is configured, and nothing
  is written locally

#### Scenario: A single-machine install runs unaffected

- **WHEN** a server boots with no registry configured
- **THEN** it loads and runs workflows from disk exactly as before, and exposes no
  publishing route

#### Scenario: Provenance survives in an air-gapped install

- **WHEN** an air-gapped host receives bundles from several subjects over time
- **THEN** each stored bundle can be queried for its publisher and publication time

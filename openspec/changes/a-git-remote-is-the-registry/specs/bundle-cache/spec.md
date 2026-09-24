# bundle-cache

Where a fetched bundle is kept, what is recorded so a run is reproducible, and how a host with no
network resolves a remote `use:`.

## ADDED Requirements

### Requirement: The cache is a cache, not a store

A fetched bundle SHALL be kept in the content-addressed local store under a key derived from its
digest. Deleting the whole cache SHALL NOT change what any workflow means: a subsequent
resolution of the same pinned reference SHALL yield the same digests and the same expanded steps.

#### Scenario: Deleting the cache changes nothing but timing

- **WHEN** the cache is deleted entirely and a workflow whose references resolved before is
  resolved again
- **THEN** the resolution succeeds and produces the same commit SHA, the same bundle digest and
  the same expanded steps as before

#### Scenario: A cached bundle is not refetched

- **WHEN** a reference pinned to a digest already present in the cache is resolved
- **THEN** the bundle is read from the cache and no network request is made

#### Scenario: A cache key cannot escape its directory

- **WHEN** a bundle is written to the cache
- **THEN** the key is derived from a hexadecimal digest and no written path lies outside the cache
  directory

### Requirement: A resolution is recorded so a rerun is exact

Each resolution of a remote reference SHALL record, against the run, the reference text as
authored, the remote, the resolved commit SHA and the bundle's manifest digest. A rerun of a
recorded run SHALL resolve from the recorded commit SHA, never from the reference's ref.

#### Scenario: The pin is recorded on the run

- **WHEN** a workflow with a remote `use:` is run
- **THEN** the run records the reference text, the remote, the commit SHA and the manifest digest
  for each remote reference

#### Scenario: A moved tag does not change a rerun

- **WHEN** a recorded run is rerun after the tag its reference named has been repointed to a
  different commit
- **THEN** the rerun resolves the originally recorded commit SHA and produces the same expanded
  steps

#### Scenario: A commit-pinned reference resolves identically on any host

- **WHEN** the same commit-pinned reference is resolved on two hosts with cold caches
- **THEN** both record the same commit SHA and the same manifest digest

### Requirement: A host with no network resolves from the cache or refuses

On a host with no route to the reference's remote, a remote `use:` SHALL resolve from the cache
when the pinned content is present, and SHALL otherwise fail naming the reference and the remote.
No part of publishing, resolving from cache, or running SHALL require a network request when the
cache holds the pinned content.

#### Scenario: An air-gapped run resolves from cache

- **WHEN** a workflow whose remote references are all present in the cache is run on a host with
  no network route to any remote
- **THEN** the run proceeds and no network request is attempted

#### Scenario: A cold cache with no network is an error, not a degraded run

- **WHEN** a remote reference absent from the cache is resolved on a host with no network
- **THEN** the load fails naming the reference and the remote, and no run starts

### Requirement: A bundle can be brought in without a network

The system SHALL provide a way to import a bundle from a local directory or archive into the
cache. An imported bundle SHALL be verified by recomputing every entry digest and the bundle
digest from the imported content, and SHALL be refused on any mismatch without being stored.

#### Scenario: A sideloaded bundle resolves a reference

- **WHEN** a bundle is imported from a local directory and a workflow pins that bundle's digest
- **THEN** the reference resolves from the cache with no network request

#### Scenario: Tampered content is refused on import

- **WHEN** a bundle whose content does not match its recorded digests is imported
- **THEN** the import is refused, the error names the mismatching entry and both digests, and
  nothing is written to the cache

### Requirement: Retargeting a remote is the operator's git configuration

Redirecting references from one host to a local mirror SHALL be achieved through git's own remote
rewriting configuration. The system SHALL NOT provide a separate mirror or default-host setting
of its own.

#### Scenario: A mirror retargets references without editing a workflow

- **WHEN** an operator configures git to rewrite a public host's URLs to an internal mirror and a
  workflow naming references on that public host is resolved
- **THEN** the references are fetched from the internal mirror and no workflow file is edited

#### Scenario: No wfnexus-side mirror setting exists

- **WHEN** the system's configuration surface is inspected
- **THEN** it exposes no mirror, proxy or default-host option for reference resolution

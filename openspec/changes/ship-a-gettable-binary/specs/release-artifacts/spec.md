# release-artifacts

What a tag publishes — the binaries, the checksums, the self-contained tarballs, the container
images — and the integrity rule that binds them.

## ADDED Requirements

### Requirement: A tag publishes every binary the build produces

Pushing a release tag SHALL publish, to the repository's releases, every binary the release build
produces: the client, the server and the worker, for Linux, Windows and macOS, on both the 64-bit
x86 and 64-bit ARM architectures. The binaries SHALL be published individually and named so that
operating system and architecture are readable from the file name.

#### Scenario: All targets are published

- **WHEN** a release tag is pushed and the release job completes
- **THEN** the release lists a client, a server and a worker binary for each supported operating
  system and architecture combination

#### Scenario: A target that fails to build stops the release

- **WHEN** any target fails to compile
- **THEN** no release is published and no partial set of artifacts is uploaded

### Requirement: Every published file is covered by a published checksum

The release SHALL publish a checksum file containing a SHA-256 digest for every other file in the
release, including the tarballs. A release in which any published file is absent from the
checksum file SHALL be treated as defective.

#### Scenario: The checksum file covers everything

- **WHEN** the release is inspected
- **THEN** every published file other than the checksum file itself has an entry in it

#### Scenario: A download can be verified offline

- **WHEN** a user downloads a binary and the checksum file
- **THEN** the digest can be verified with a standard command-line tool and no network access

### Requirement: The self-contained server tarball is published on every release

Each release SHALL publish the self-contained server tarball: the Linux binaries, the built UI,
the shipped workflows, the templates, the skills, the provider registry, the Kubernetes manifests,
an example configuration, and a container definition that copies the binaries already inside the
tarball rather than building them. A tarball SHALL be published for each supported Linux
architecture, differing only in the binaries it carries.

#### Scenario: The tarball is a release artifact, not a local build product

- **WHEN** a release is published
- **THEN** a server tarball for each supported Linux architecture is attached to it, each with a
  published checksum

#### Scenario: The tarball needs no registry and no toolchain

- **WHEN** the tarball is unpacked on a machine with a container runtime, no registry access
  beyond itself and no Go or Node toolchain
- **THEN** the container image builds from the binaries inside the tarball and the server starts

#### Scenario: The tarball's container definition names no remote registry

- **WHEN** the tarball's container and compose definitions are inspected
- **THEN** they obtain the application binaries from the tarball and not from any container
  registry

### Requirement: Container images are published to a registry

The release SHALL publish container images for the server and the worker to a public OCI
registry, built from the repository's own container definitions, for the 64-bit x86 and 64-bit
ARM Linux architectures. Each image SHALL be tagged with the exact release version and with a
moving tag for the newest release.

#### Scenario: Images are pullable at the release version

- **WHEN** a release tag is pushed and the release job completes
- **THEN** the server and worker images are pullable at that exact version

#### Scenario: Publishing images does not alter the offline path

- **WHEN** the container images are published
- **THEN** the tarball's own definitions are unchanged and continue to require no registry

### Requirement: A release is reproducible from the repository at that tag

The release build SHALL be the same build a contributor can run locally from the repository at
that tag, with no step that exists only in the release environment other than uploading. Build
flags that affect the output SHALL be identical.

#### Scenario: A contributor can reproduce the artifacts

- **WHEN** a contributor checks out the release tag and runs the repository's release target
- **THEN** the same set of artifacts is produced, with the same names and the same injected
  version

#### Scenario: No release-only build step

- **WHEN** the release job is inspected
- **THEN** the only steps it performs beyond the repository's own release target are checksum
  generation, upload and image push

### Requirement: The release states how to verify what was downloaded

Release notes SHALL state the command that verifies a downloaded file against the published
checksum file, and SHALL state that downloaded macOS binaries are unsigned and are quarantined by
the operating system, together with the command that clears the quarantine attribute.

#### Scenario: Verification is documented at the point of download

- **WHEN** a user reads the release notes
- **THEN** the checksum verification command is present

#### Scenario: The macOS consequence is stated, not discovered

- **WHEN** a user reads the release notes
- **THEN** the notes state that macOS binaries are unsigned and not notarised and give the
  command that allows a downloaded binary to run

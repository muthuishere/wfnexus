# install-paths

How someone who does not have this repository obtains the binaries, stated at all three scales —
one person, a small org, an air-gapped enterprise — and what each path does and does not verify.

## ADDED Requirements

### Requirement: One person installs the client with a single command

A person on macOS or Linux SHALL be able to install the client with one copy-and-paste command
that requires no Go toolchain, no Node toolchain, no container runtime and no clone of this
repository. The command SHALL detect the operating system and architecture, obtain the newest
release, and place the client on the user's own path without requiring administrative privileges.
An equivalent command SHALL exist for Windows.

#### Scenario: Install on macOS or Linux

- **WHEN** the documented install command is run on a supported macOS or Linux machine with no
  toolchain installed
- **THEN** the client is installed to a per-user directory and reports its version when run

#### Scenario: The install needs no privileges

- **WHEN** the install command is run as an unprivileged user
- **THEN** it completes without requesting administrative privileges and writes nothing outside
  the user's own directories

#### Scenario: The path is stated when it is not already usable

- **WHEN** the install directory is not on the user's path
- **THEN** the installer says so and prints the line that adds it

#### Scenario: Windows has an equivalent

- **WHEN** the documented Windows command is run
- **THEN** the client is installed to a per-user directory and reports its version

### Requirement: The single-command install verifies integrity before installing

The installer SHALL download the release's checksum file and verify the downloaded binary against
it before placing anything on the user's path. On a mismatch it SHALL install nothing and exit
non-zero.

#### Scenario: A corrupted download is refused

- **WHEN** the downloaded binary's digest does not match the published checksum
- **THEN** nothing is installed, any partial download is removed, and the command exits non-zero
  with a message naming the mismatch

#### Scenario: A missing checksum is a failure, not a warning

- **WHEN** the checksum file cannot be obtained
- **THEN** the installer refuses to install rather than proceeding unverified

### Requirement: An unsupported platform fails clearly

The installer SHALL refuse a platform for which no binary is published, naming the detected
operating system and architecture and the platforms that are supported.

#### Scenario: An unpublished architecture

- **WHEN** the installer runs on an operating system and architecture combination that has no
  published binary
- **THEN** it installs nothing and states the detected platform and the supported ones

### Requirement: The Go toolchain is a documented second path, with its limit stated

For users who already have a Go toolchain, the client and the worker SHALL be installable
directly from the module path at a version. The documentation SHALL state that the server is not
installable this way, because the UI and default assets are embedded from a staging directory
that is not committed, and SHALL direct the reader to the release binary or the tarball instead.

#### Scenario: The client installs from the module path

- **WHEN** the client is installed from its module path at a released version
- **THEN** the resulting binary runs and reports that version

#### Scenario: The server's exclusion is documented, not implied

- **WHEN** a reader follows the toolchain install documentation
- **THEN** it states that the server cannot be obtained this way and names the two paths that
  work

### Requirement: A small org pins a version

The install command SHALL accept an explicit version, so that an organisation can pin one version
in its own build file and have every developer machine and build runner obtain the identical
build, verified against that release's checksums.

#### Scenario: A pinned version is honoured

- **WHEN** the install command is run with an explicit released version
- **THEN** that version is installed rather than the newest, and it is verified against that
  release's checksums

#### Scenario: The pin is auditable on the machine

- **WHEN** an installed client is asked for its version
- **THEN** it reports the pinned version, so a machine can be checked against the pin

### Requirement: An air-gapped site installs from a single verified file

An organisation with no network access from the target machines SHALL be able to install the
complete server by transferring one published tarball and the checksum file, verifying the
tarball, and unpacking it on the target. The install SHALL require no container registry, no
package repository, no Go or Node toolchain, and no network access other than to the
organisation's own model provider.

#### Scenario: Download once, verify, carry in

- **WHEN** the tarball and checksum file are downloaded on a connected machine, verified, and
  transferred to a disconnected machine
- **THEN** the server, the client, the worker, the UI, the shipped workflows, the templates, the
  skills and the provider registry are all present on the disconnected machine

#### Scenario: The disconnected machine starts the platform

- **WHEN** the documented command is run on the disconnected machine
- **THEN** the platform starts and serves its UI without reaching any registry or package
  repository

#### Scenario: One file per machine architecture

- **WHEN** an operator selects a tarball for a target machine
- **THEN** one tarball corresponds to that machine's architecture and no architecture decision is
  deferred to build time on the disconnected machine

### Requirement: The offline path is documented before the registry path

Documentation that lists ways to obtain the platform SHALL present the self-contained tarball
ahead of the container-registry path, because the registry path is the one an air-gapped site
cannot use.

#### Scenario: Ordering in the documentation

- **WHEN** the install documentation is read
- **THEN** the self-contained tarball appears before any instruction that pulls an image from a
  registry

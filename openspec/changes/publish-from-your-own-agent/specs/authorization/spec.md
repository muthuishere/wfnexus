# authorization

What a subject may do. Roles, a default admin, project scoping, and the rule that
authorization is resolved by this system and never delegated (ADR 0017).

## ADDED Requirements

### Requirement: Authorization is resolved by this system and never delegated

Every authorization decision SHALL be made by this system against its own roles and
project scopes. The system SHALL NOT accept a permission, role, or entitlement asserted by
an external identity provider as the decision. When authentication is external, the
provider's assertion SHALL be used only to resolve which local subject is acting; the
permissions applied SHALL be the ones this system stores for that subject.

This holds at the enterprise scale unchanged: an external provider may authenticate, it
may never authorize.

#### Scenario: Provider-asserted roles are not honoured

- **WHEN** an external authentication provider is configured and its assertion for a
  subject carries role or group claims naming permissions the local subject does not have
- **THEN** the request is authorized against the locally stored roles only, and the
  claimed permissions have no effect

#### Scenario: A locally revoked role takes effect immediately

- **WHEN** a subject's role is removed locally while the external provider still
  authenticates them
- **THEN** the subject authenticates and their next request to an action that role granted
  is refused

### Requirement: A subject's permissions are scoped by project

Every subject SHALL carry a project scope. An action on a workflow, run, step, or
published asset SHALL be authorized only when that resource's project is within the
acting subject's scope. A request for a resource outside scope SHALL be refused and SHALL
NOT disclose whether the resource exists.

#### Scenario: A token cannot reach another project's workflows

- **WHEN** a subject scoped to project A requests a workflow belonging to project B
- **THEN** the request is refused and the response is indistinguishable from the response
  for a workflow that does not exist

#### Scenario: A token cannot reach another project's runs

- **WHEN** a subject scoped to project A requests, lists, cancels, or resolves a pause on a
  run belonging to project B
- **THEN** each request is refused and the run's state is unchanged

#### Scenario: Listing is filtered, not merely checked

- **WHEN** a subject scoped to project A lists workflows or runs while resources exist in
  projects A and B
- **THEN** only project A's resources appear in the listing

#### Scenario: Scope applies to publishing

- **WHEN** a subject scoped to project A publishes an asset naming project B
- **THEN** the publish is refused and nothing is stored

### Requirement: A default admin exists on first boot and users and roles are editable

On first boot of a host that requires authentication, the system SHALL create exactly one
default administrator subject and a default set of roles. The role set SHALL NOT be fixed
in the binary: roles SHALL be creatable, editable, and deletable afterwards, and users
SHALL be creatable, editable, and deletable afterwards. Creating the default admin SHALL
NOT generate a fixed or published default password; the initial credential SHALL be
obtained through the device grant or shown once at bootstrap.

At the one-person scale, where the listener is on loopback and no users are configured,
no default admin and no roles are created.

#### Scenario: First boot creates one admin and the default roles

- **WHEN** a host that requires authentication boots against an empty store
- **THEN** exactly one administrator subject and the default roles exist
- **AND** a second boot against the same store creates no further admin

#### Scenario: Roles are editable after bootstrap

- **WHEN** an administrator creates a new role, edits a default role's permissions, and
  deletes a role that no subject holds
- **THEN** each change is stored and takes effect on the next authorization decision

#### Scenario: No default admin on a loopback single-machine install

- **WHEN** the server is bound to loopback with no users configured
- **THEN** no admin subject and no roles are created

#### Scenario: The last administrator cannot be removed

- **WHEN** an administrator deletes or demotes the only remaining administrator subject
- **THEN** the change is refused and the host retains at least one administrator

### Requirement: Every state-changing action records who performed it

Every action that changes stored state SHALL record the acting subject as a queryable
field on the affected record, not only as a log line. The recorded actor SHALL follow the
shape already established for pause resolution: a principal identifier and the channel the
claim arrived through (`api`, `ui`, `cli`), stored alongside the time of the action and
what was decided. An empty actor SHALL be refused rather than defaulted.

Where authentication is present, the actor SHALL be filled from the authenticated subject
and SHALL NOT be taken from the request body. Where authentication is absent (loopback,
one-person scale) the actor is recorded as an unverified claim, and the recorded channel
SHALL make the two eras distinguishable to a later audit.

#### Scenario: An approval records the authenticated subject

- **WHEN** an authenticated subject approves a paused step
- **THEN** the step record stores that subject as the resolver, the resolution, the reason,
  and the resolution time
- **AND** an actor supplied in the request body is ignored in favour of the authenticated
  subject

#### Scenario: A reject and an answer record the same way

- **WHEN** an authenticated subject rejects a paused step, and separately answers a
  question step
- **THEN** each step record stores the resolver, the resolution, the reason, and the time
  in the same fields used by approval

#### Scenario: An empty actor is refused

- **WHEN** a resolve request arrives with no actor and no authenticated subject
- **THEN** the request is refused and the step's state is unchanged

#### Scenario: Publishing records its actor

- **WHEN** an authenticated subject publishes an asset
- **THEN** the stored asset records that subject and the time of publication as queryable
  fields

#### Scenario: The audit record survives at enterprise scale

- **WHEN** an air-gapped host with an external authentication provider serves approvals and
  publishes over a period
- **THEN** each affected record carries its actor and time, and the audit can be answered
  by query without reading logs

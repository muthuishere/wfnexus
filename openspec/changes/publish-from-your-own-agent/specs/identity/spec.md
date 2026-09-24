# identity

Who a request is from. Device-grant login, user tokens, logout, named host contexts,
and the rule that a subject is required off-loopback and absent on it (ADR 0017).

## ADDED Requirements

### Requirement: Login uses the OAuth 2.0 Device Authorization Grant

The client SHALL authenticate against a host using the OAuth 2.0 Device Authorization
Grant (RFC 8628). The flow SHALL complete with no callback listener on the client
machine and with no browser on the client machine: the client prints a verification
URI and a user code, then polls the host until the code is approved, denied, or expires.
No other interactive login flow SHALL be offered.

Because no port is bound and no browser is launched, the flow SHALL work unchanged when
the client runs over SSH, inside a container, and inside another agent's session.

#### Scenario: Login completes with no callback and no browser

- **WHEN** `wfx login --url <host>` is run on a machine with no browser and no display
- **THEN** the client prints a verification URI and a user code, binds no local port,
  launches no browser, and polls the host's token endpoint
- **AND** once the code is approved out of band the client receives a token and the
  command exits successfully

#### Scenario: Login works over SSH and inside a container

- **WHEN** `wfx login --url <host>` is run inside an SSH session and, separately, inside
  a container with no host networking
- **THEN** both invocations complete successfully with the same printed-code-and-poll flow

#### Scenario: The user code expires

- **WHEN** the printed user code is not approved before its expiry
- **THEN** the host refuses the pending authorization, the client stops polling, and the
  command exits with a non-zero status and an expiry message
- **AND** no token is written to the client's credential store

#### Scenario: Polling respects the grant's pacing

- **WHEN** the client polls before the interval the host returned, or the host answers
  `slow_down`
- **THEN** the client increases its polling interval rather than failing the login

### Requirement: A token value is shown once and stored only as a hash

The host SHALL store a user token only as its hash, using the same mint-and-hash path as
the existing worker token. The token value SHALL be returned to the client exactly once,
at issue time, and SHALL NOT be retrievable from the host afterwards. Listing or
inspecting tokens SHALL return metadata (identifier, subject, creation time, last use)
and never the value.

#### Scenario: The value is not recoverable from the host

- **WHEN** a user token has been issued and the operator then lists or reads that token
  through any host API
- **THEN** the response contains the token's metadata and does not contain the token value

#### Scenario: Presenting the token authenticates

- **WHEN** a request carries `Authorization: Bearer <issued value>`
- **THEN** the host hashes the presented value, matches the stored hash, and resolves the
  subject for that token

#### Scenario: Revocation is independent of worker tokens

- **WHEN** a user token is revoked
- **THEN** requests carrying it are refused
- **AND** worker tokens and other user tokens continue to authenticate

### Requirement: A credential value never leaves the client's credential store

A credential value SHALL NOT appear in any log line, any error message, any process
argument vector, any event or audit record, or any file other than the client's own
credential store. The store SHALL be created with permissions that deny access to other
users on the machine.

#### Scenario: No credential in logs or errors

- **WHEN** login succeeds, and separately when an authenticated request fails with an
  authentication error
- **THEN** no client or host log line and no error message contains the token value
- **AND** a search of the captured output for the issued value finds no occurrence

#### Scenario: No credential on the command line

- **WHEN** the client makes any authenticated request
- **THEN** the token value is not passed as a command-line argument to any process it
  spawns, and is not present in the client's own argv

#### Scenario: The credential store is not world-readable

- **WHEN** the client writes its credential store
- **THEN** the file's permissions grant read access to the owning user only

### Requirement: Hosts are named contexts, selected per invocation

The client SHALL keep several hosts configured simultaneously as named contexts, each
with its own host URL and credential. A context SHALL be selectable per invocation, and a
default context SHALL be used when none is named. Adding, selecting, or removing one
context SHALL NOT alter any other context's stored credential.

#### Scenario: Two hosts are logged in at once

- **WHEN** the user logs in to host A and then to host B
- **THEN** both contexts are listed as authenticated and each retains its own credential

#### Scenario: A request targets the named context

- **WHEN** a command names context B while the default context is A
- **THEN** the request is sent to host B's URL with host B's credential

#### Scenario: Logging out of one host leaves others intact

- **WHEN** `wfx logout` is run for host A
- **THEN** host A's credential is removed from the store and host A's context is no longer
  authenticated
- **AND** host B remains authenticated and its next request succeeds

#### Scenario: Logout revokes at the host

- **WHEN** `wfx logout` is run for a host that is reachable
- **THEN** the client asks the host to revoke that token and the token no longer
  authenticates
- **AND** the local credential is removed even if the host is unreachable

### Requirement: On loopback with no users configured there is no authentication

When the server's listener is bound to a loopback address and no users are configured,
authentication SHALL be absent rather than permissive. There SHALL be no identity on the
request, no anonymous or implicit user record, no default password, and the authorization
check SHALL NOT be reached. The server SHALL decide this from its own bind address; there
SHALL NOT be a setting or flag that disables authentication on a non-loopback bind.

This is the one-person scale: zero config, one binary, nothing to log into.

#### Scenario: An unauthenticated request on loopback succeeds

- **WHEN** the server is bound to `127.0.0.1` with no users configured and a request
  arrives on any route with no `Authorization` header
- **THEN** the request is served normally

#### Scenario: No implicit user is created

- **WHEN** the server has run on loopback with no users configured and requests have been
  served
- **THEN** no user record exists in the store and no default credential has been generated

#### Scenario: Authentication cannot be switched off off-loopback

- **WHEN** the server is configured to bind a non-loopback address and any setting or flag
  is used that would disable authentication
- **THEN** no such setting exists; the server binds with authentication required

### Requirement: Off loopback every request requires a valid subject

When the server's listener is bound to any address that is not loopback, every API route
SHALL resolve a subject before handling the request. The subject SHALL be either a machine
identity (a worker token) or a person (a user token). A request with no credential, an
unparseable credential, an unknown credential, or a revoked credential SHALL be refused
with an authentication failure and SHALL NOT be handled.

#### Scenario: No credential off loopback is refused

- **WHEN** the server is bound to a non-loopback address and a request arrives with no
  `Authorization` header
- **THEN** the request is refused with an authentication failure and the handler is not
  reached

#### Scenario: A revoked or unknown credential is refused

- **WHEN** a request presents a token that was revoked, or a value matching no stored hash
- **THEN** the request is refused with an authentication failure

#### Scenario: Both subject kinds resolve on the same middleware

- **WHEN** a worker presents its worker token and a person presents their user token, each
  on a route they are entitled to call
- **THEN** both requests resolve a subject through the same server-wide middleware and are
  handled

#### Scenario: Enterprise authentication is pluggable without changing this rule

- **WHEN** the host is configured with an external authentication provider instead of
  local users
- **THEN** the same rule holds: off loopback a subject is resolved for every request, and
  a request with no valid subject is refused

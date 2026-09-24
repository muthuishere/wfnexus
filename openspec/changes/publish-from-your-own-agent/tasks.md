# Tasks — publish from your own agent

Ordering is by dependency: groups 1–2 are prerequisites for everything; groups 3–6 are the
**identity half** and ship on their own (`wfx login` is useful with no publishing at all);
groups 7–10 are the **publishing half** and depend on group 5 (a publish needs a subject);
groups 11–13 close the change. Tasks marked **[SHIP]** are an independently releasable
boundary. Tasks marked **[SEC-TEST]** MUST land with the named test — this repo's rule is
that a claim without observed output is not done.

**Not a task, but a real prerequisite:** distribution (tags, CI, goreleaser, install path)
is a separate change. Until it lands nobody can obtain the client these verbs live in;
verification here is therefore `go run ./apps/api/cmd/wfx` or a locally built binary.

## 1. Move the credential path to a neutral package (blocks everything)

- [x] 1.1 Create `apps/api/internal/auth` and move `newToken()` and `HashToken()` verbatim
      out of `apps/api/internal/engine/workers.go:82-93`, keeping the `wfx_` prefix, the
      SHA-256 storage rule and the comment stating the value is never written to a
      database, a log or an event. Export `NewToken`. Verify: `go build ./...` and
      `go test ./apps/api/...` pass with no behaviour change.
- [x] 1.2 Point `engine` at `auth.NewToken`/`auth.HashToken` (registration token,
      `AuthWorker`) and delete the local copies. Verify: existing worker tests still pass;
      `grep -rn "func newToken\|func HashToken" apps/api` returns only the new package.
- [x] 1.3 **[SEC-TEST]** `auth` unit test: a minted token has the `wfx_` prefix and 48 hex
      characters; `HashToken` is stable and never returns the input; two mints differ.
      Verify: `go test ./apps/api/internal/auth/`.

## 2. Let the handler see how it is reachable (the load-bearing edit)

- [x] 2.1 Add an `addr string` parameter to `api.New` (`apps/api/internal/api/api.go:51`)
      and pass `cfg.Addr` from the single call site `apps/api/main.go:212`. Nothing uses it
      yet. Verify: `go build ./...`; the server still starts and `GET /api/health` answers.
- [x] 2.2 Add `isLoopback(addr)` in `api`: true only for `127.0.0.0/8`, `::1` and
      `localhost`; an empty host (`:8090`) is **not** loopback. Compute `loopbackOnly` once
      in `New`. Verify: table test over `127.0.0.1:8090`, `localhost:8090`, `[::1]:8090`,
      `:8090`, `0.0.0.0:8090`, `10.0.0.5:8090`.
- [x] 2.3 Do **not** change the bind default — `config.go:190` is already
      `127.0.0.1:8090`. Verify: `grep -n '127.0.0.1:8090' apps/api/internal/config/config.go`
      confirms it, and note it in the deployment notes rather than editing it.

## 3. Identity storage (paired migrations)

- [x] 3.1 Read the existing pair `apps/api/migrations/000009_step_resolution.{up,down}.sql`
      and `apps/api/migrations/sqlite/000003_step_resolution.{up,down}.sql` before writing
      anything; the sqlite tree is squashed and numbered independently. Verify: state the
      two next numbers (`000010_identity` and `sqlite/000004_identity`) in the PR body.
- [x] 3.2 Write the paired identity migration — `users`, `roles`, `user_tokens`,
      `device_codes` per design §3 — as `000010_identity.{up,down}.sql` and
      `sqlite/000004_identity.{up,down}.sql`, each in its own dialect (no `uuid`/`jsonb`/
      `text[]`/`timestamptz` assumptions in the sqlite copy). `workers` is untouched.
      Verify: boot against a fresh SQLite file and a fresh Postgres; both migrate up and
      down cleanly.
- [x] 3.3 Add `store` types and queries: users, roles, user tokens (lookup by
      `HashToken(bearer)`, `last_used` touch), device codes. Verify: store test creating a
      user, issuing a token, resolving it by hash, revoking it.
- [x] 3.4 **[SEC-TEST]** Token hashing at the storage boundary: after issuing a token,
      no row, column, log line or API response contains the token value — assert by
      scanning the dump and the captured output for the issued string. Covers identity
      spec "A token value is shown once and stored only as a hash".

## 4. The device grant (server side)

- [x] 4.1 `POST /api/device/code` (RFC 8628 §3.1–3.2): accepts `client_id=wfx-cli`, returns
      `device_code`, `user_code`, `verification_uri`, `verification_uri_complete`,
      `expires_in=900`, `interval=5`. `user_code` is 8 chars of the §6.1 base-20 alphabet
      `BCDFGHJKLMNPQRSTVWXZ`, displayed `XXXX-XXXX`; `device_code` is stored hashed.
      Verify: curl the endpoint and show the response shape.
- [x] 4.2 `POST /api/device/token` (§3.4–3.5): answers `authorization_pending`,
      `slow_down`, `expired_token`, `access_denied` or the token. The row is **deleted** on
      success, denial or expiry (§3.5 single use). Verify: test walking pending → approved →
      token, then a second poll with the same device code returning an error.
- [x] 4.3 **[SEC-TEST]** Rate limit the verification endpoint (§5.2): 5 attempts per minute
      per source, and 5 total failures burns the code. Test asserts the 6th attempt is
      refused and the code is unusable thereafter.
- [x] 4.4 Resolve the open question in design §1 first: whether the SPA fallback in
      `api.go` permits a non-`/api` `/device` route. Read the fallback, then either add
      `GET /device` or place the page under a path that works. Verify: `curl /device`
      returns the page and the SPA routes still resolve.
- [~] 4.5 The approval page — served by the API at `GET /device` (`internal/api/device.go`),
      NOT from `apps/ui/src/`: another agent owns that tree, and a login must work on a host
      where the UI bundle was never built. The page shows the client name, takes the code and
      approves or denies. The UI-side version, if wanted, is still open.
      ORIGINAL: The approval page (`apps/ui/src/`): enter the code, show the client name
      (`wfx-cli`), the invoking hostname, the requested scope and the signed-in user,
      Approve / Deny. Approving binds that browser session's user to the device code.
      Verify: approve a real pending code in a browser and watch the CLI poll succeed.
- [x] 4.6 Resolve design's second open question — whether the UI needs its own session
      cookie to approve, or the first admin approves via the bootstrap code. Implement
      exactly one; record which in the PR body. Verify: the chosen path completes a login
      end to end.

## 5. Subject resolution middleware and the loopback exemption

- [x] 5.1 Add `requireSubject` middleware and install it on the `/api` router with
      `r.Use(...)` **unconditionally** — no branch omits it. It resolves the bearer to a
      user or a worker subject and puts it on the request context. Verify: a handler reads
      the subject from context in a test.
- [x] 5.2 Keep `bearer()` (`apps/api/internal/api/workers.go:25`) and `authWorker` (`:30`)
      as the narrower, second check on the four worker routes — the middleware **precedes**
      them, it does not replace them. Verify: existing worker route tests pass untouched.
- [x] 5.3 **[SEC-TEST]** The loopback exemption: with `loopbackOnly` true, a request with
      no `Authorization` header gets a nil subject and is served; no user row is created and
      no default credential is generated. With `loopbackOnly` false, the same request is
      401 and the handler is never reached. Covers identity spec scenarios "An
      unauthenticated request on loopback succeeds", "No implicit user is created", "No
      credential off loopback is refused".
- [x] 5.4 **[SEC-TEST]** There is no off switch: assert by test and by
      `grep -rn "WFX_AUTH\|no-auth\|DisableAuth" apps/api` that no setting or flag can
      disable authentication on a non-loopback bind. Covers "Authentication cannot be
      switched off off-loopback".
- [x] 5.5 **[SEC-TEST]** Boot refusal: a non-loopback bind with zero rows in `users` prints
      the one-time bootstrap admin credential to stdout and **refuses to serve** until it is
      claimed. Test asserts the server does not accept connections in that state and that
      the credential value appears only on stdout, never in a stored row.
- [x] 5.6 Both subject kinds resolve through the same middleware: a worker token and a user
      token each authenticate on a route they are entitled to. Verify: one test, two
      requests, both 200.
- [x] 5.7 One boot log line stating whether auth is active, and a deployment note recording
      the two honest gaps from design §4 (a reverse proxy in front of a loopback bind; the
      machine as the trust boundary). Verify: read the line out of a real boot on each bind.

## 6. The client: contexts, login, logout **[SHIP — the identity half ends here]**

- [x] 6.1 Contexts file `~/.config/wfx/contexts.json` (design §2 shape), written with the
      temp-file + `os.Rename` pattern already in `blob/folder.go:65-80`, mode `0600` and
      parent `0700`. Verify: `stat` the written file.
- [x] 6.2 **[SEC-TEST]** The client **refuses** to read a contexts file that is group- or
      world-readable and says so. Test chmods to `0644` and asserts a non-zero exit with no
      request sent. Covers identity spec "The credential store is not world-readable".
- [x] 6.3 Resolution order in `base()` (`apps/api/cmd/wfx/main.go:156-162`), most specific
      first: `--url` → `WFX_API` (unchanged) → `contexts.current` → `http://127.0.0.1:8090`.
      A `--url` with no context is an error telling the user to `wfx login --url` first.
      Verify: four-case table test.
- [x] 6.4 `call()` (`:163-193`) sets `Authorization: Bearer <token>` when the resolved
      context has one, and nothing else changes in the transport. Verify: a test server
      asserts the header; the `WFX_API`-only path sends none.
- [x] 6.5 `wfx login --url <host>`: print the verification URI and user code, bind no port,
      launch no browser, poll honouring `interval` and adding 5s on `slow_down`, stop on
      expiry with a non-zero exit and no token written. Verify: run it over SSH and inside a
      container and paste both transcripts. Covers the first four identity scenarios.
- [x] 6.6 `wfx logout [--url]`: `DELETE /api/tokens/self` to revoke server-side, then remove
      the local context; the local entry is removed **even if the host is unreachable**.
      Verify: log out of host A and show host B's next request still succeeds.
- [x] 6.7 `wfx context list` / select, and the multi-host invariant: adding, selecting or
      removing one context does not alter another's credential. Verify: log in to two local
      servers on different ports and exercise all four scenarios of "Hosts are named
      contexts".
- [x] 6.8 **[SEC-TEST]** No credential in logs, errors or argv: run login and a failing
      authenticated request, capture all client and server output, and assert a search for
      the issued value finds nothing; assert the value is in no spawned process's argv.
      Covers "A credential value never leaves the client's credential store".
- [ ] 6.9 (NOT DONE — `apps/ui/` is owned by another agent this pass.) UI login state in `apps/ui/src/` (React+TS+Vite): `api.ts` already carries an
      actor claim; add a signed-in user, the bearer on requests, and sign-out. Verify: load
      the UI against a non-loopback server and observe 401 before login, 200 after.

## 7. Authorization: roles, admin, project scope

- [x] 7.1 Seed roles `admin`, `publisher`, `viewer` as **rows** on first boot of a host that
      requires authentication, plus exactly one admin subject; a second boot against the
      same store creates no further admin. No role set is baked into the binary (ADR 0017).
      Verify: boot twice against one store and count rows.
- [x] 7.2 Name the permission vocabulary from the routes that exist (design's third open
      question) and store it in `roles.permissions`. Verify: every `/api` route maps to a
      named permission in a table test that fails when a route has none.
- [x] 7.3 Admin CRUD for users and roles: create, edit, delete a role no subject holds; the
      change takes effect on the next authorization decision. Verify: edit a role and show
      the next request's outcome flipping.
- [x] 7.4 **[SEC-TEST]** The last administrator cannot be deleted or demoted. Test asserts
      the refusal and that an admin remains.
- [x] 7.5 **[SEC-TEST]** Project scoping, hooked to `model.ScopeProject`
      (`apps/api/internal/model/model.go:129`): a subject scoped to project A is refused
      workflows, runs, cancels and pause resolutions in project B, and the refusal is
      **indistinguishable from not-found**. Test asserts identical status and body.
- [x] 7.6 **[SEC-TEST]** Listing is filtered, not merely checked: with resources in A and B,
      a subject scoped to A sees only A's in workflow and run listings.
- [x] 7.7 Authorization is never delegated: when external authentication is configured, a
      provider assertion carrying role or group claims has no effect — permissions come from
      the local `roles` table, and a locally revoked role takes effect on the next request.
      Verify: test with a stub provider asserting `admin` for a `viewer` subject.

## 8. Actor recording (extends the ADR 0021 shape already in `000009_step_resolution`)

- [x] 8.1 Fill `resolved_by` from the authenticated subject and **ignore** an actor supplied
      in the request body. Verify: post a resolve with a forged body actor and assert the
      stored resolver is the authenticated subject.
- [x] 8.2 Apply the same to reject and to answering a question step, using the same four
      columns. Verify: one test per resolution kind.
- [x] 8.3 An empty actor with no authenticated subject is refused and the step's state is
      unchanged. Verify: assert the 4xx and re-read the row.
- [x] 8.4 Record the channel (`api`, `ui`, `cli`) so the unauthenticated loopback era and
      the authenticated era are distinguishable to a later audit. Verify: rows from both
      eras queried side by side.
- [x] 8.5 **[SEC-TEST]** A notification or link never carries a capability: the
      `notify.Notification.URL` (`apps/api/internal/notify/notify.go:42-43`) is a pointer
      to the approval, never a token or a signed resolve-on-click link. Test asserts no
      emitted notification field contains a token, a signature or a resolving query
      parameter, and that following the URL unauthenticated resolves nothing.

## 9. The bundle format

- [x] 9.1 Manifest and digest rules (design §5): entry digest = `sha256:` of a file's bytes,
      or for a skill directory the canonical sorted `"<digest>  <path>\n"` listing; bundle
      digest = `sha256:` of `manifest.json`'s canonical bytes (sorted keys, no insignificant
      whitespace). Verify: a test recomputes the same digest from a reordered tar and from
      changed mtimes/permissions.
- [x] 9.2 Pack/unpack a gzipped tar with `manifest.json`, `workflow.yaml`,
      `skills/<name>/…`, `mcp.json`. Verify: round-trip test — pack, unpack, compare trees
      and digests.
- [x] 9.3 Store bytes in the **existing** `apps/api/internal/blob` store under
      `bundles/<sha256>/<mediatype>`; no new storage layer. Verify: works under the `Folder`
      driver and (if available) MinIO; `folder.go:47-56` already refuses escaping keys and
      hex digests cannot escape — assert with a test.
- [x] 9.4 Paired migration for `published_bundles` (+ `bundle_tags`) per design §5, as
      `000011_published_bundles` and `sqlite/000005_published_bundles`, each in its own
      dialect. Verify: migrate up and down on both engines.
- [~] 9.5 (PARTIAL — see note.) Write order is blobs first, then the index row, in one transaction — a refused
      publish stores nothing.
      NOTE: every refusal this change defines (credential, unresolvable reference, digest
      mismatch, immutability, scope, no subject) fires BEFORE the blob write, and
      `assertNothingStored` proves nothing is stored for each. A true single transaction
      spanning a blob store and a database does not exist: design §5 itself settles for
      blobs-first and an orphan blob that a deferred GC collects. A crash between the two
      writes therefore leaves an orphan, never a broken index row — which is the direction
      the design chose. The literal fault-injection test was not written. Verify: fault-inject a failure after the blob write and assert
      no index row and no partially indexed bundle.

## 10. `wfx publish` and its refusals **[SHIP — the publishing half]**

- [x] 10.1 Resolve every skill and MCP declaration the workflow's steps name against the
      publisher's roots at publish time and carry the content into the bundle with its
      digest. Verify: publish a workflow and inspect the stored bundle for each skill's
      files and digest.
- [x] 10.2 **[SEC-TEST]** Refuse a literal credential, reusing `catalog.looksLikeSecret`
      (`apps/api/internal/catalog/save.go:77`, called at `:31` and `:52`) **unchanged** —
      do not reimplement. Applied to provider and classifier `apiKeyEnv` and to step env
      keys, **client-side before upload and server-side on receipt**. Test asserts the
      refusal, that nothing is stored, and that neither the message nor any log line
      contains the offending value; and that `OPENAI_API_KEY` publishes fine.
- [x] 10.3 Refuse an unresolvable skill or MCP reference, naming the reference and the roots
      searched. Verify: publish a workflow naming a missing skill and show the error and an
      empty bundle table.
- [x] 10.4 Refuse a digest mismatch: the server recomputes every entry digest and the
      manifest digest from the received bytes; a mismatch is a 400, nothing is indexed and
      nothing is written under the claimed key. Verify: upload tampered bytes in a test.
- [x] 10.5 Immutability: `unique (project, kind, name, version)` makes `name@version` twice
      a **refusal, not an overwrite**, including for byte-identical content — the error says
      the version exists and shows that the digest matches. Verify: the four scenarios of
      "A bundle is content-addressed and immutable", including a tag moving while versions
      do not.
- [x] 10.6 **[SEC-TEST]** Publishing requires an authenticated subject and records
      `published_by` + `published_at` as queryable fields returned on inspect. A publish
      with no valid subject is refused and stores nothing. A subject scoped to project A
      publishing into project B is refused and stores nothing (ties to 7.5).
- [x] 10.7 `wfx publish` with no configured host is an **error** — "no host: run
      `wfx login --url …`" — not a degraded or local-only mode, and writes nothing locally.
      Verify: run it with an empty contexts file and assert exit code and an unchanged
      working tree.
- [x] 10.8 A server with no registry configured exposes **no publishing route** and loads
      and runs workflows from disk exactly as before. Verify: `curl` the publish path on a
      default install and get a 404.

## 11. Resolving a bundle back into a runnable workflow (portability proof)

- [x] 11.1 Materialise a pulled bundle into a **bundle-scoped skill root** and **prepend**
      it to `skills.DefaultRoots` (`apps/api/internal/skills/registry.go:45-53`), so
      first-root-wins resolves every step's `skills:` to the carried copy and a same-named
      machine skill is recorded in `Shadowed` rather than silently winning. Do not change
      the resolution rule. Verify: a test asserting the carried skill wins and the local one
      appears in `Shadowed`.
- [x] 11.2 Hold the platform boundary: materialisation is done by the server on a
      CLI-initiated publish/pull, never by a tool handed to a step; no new platform tool is
      added (`apps/api/internal/skills/platform.go:18`). Verify:
      `git diff apps/api/internal/skills/platform.go` is empty and the platform tool list is
      unchanged.
- [x] 11.3 Decide and record whether `wfx pull` lands in this change (design's fourth open
      question — the proposal claims only `publish`). If it does, implement it and report a
      conflicting local skill by naming it and printing **both** digests rather than
      silently preferring either. Verify: pull onto a machine holding a different
      same-named skill and read the report.
- [x] 11.4 The portability test the spec demands: publish on one machine, pull on a second
      holding none of the dependencies, run both against the same backend, and assert the
      prompts, scoped skills and tools, and output contracts submitted for each step are
      identical. Verify: the test passes and its output is recorded in
      `docs/spikes/FINDINGS.md` — real code and observed output, per the repo's spike rule.

## 12. The three-scale check (the change is not done until all three are observed)

- [ ] 12.1 **Solo**: a fresh install with nothing configured — zero config, no auth, no
      login, publishing absent. Boot on the default `127.0.0.1:8090`, run a workflow from
      disk, confirm no user rows, no roles, no default password, `WFX_API` still works
      untouched, and `wfx publish` errors rather than degrading. Verify: paste the transcript.
- [ ] 12.2 **Small org**: one shared server with `WFX_ADDR=:8090`, an admin from first boot,
      `wfx login --url` from a second machine (and once from inside a container), roles and
      projects as rows, a publish and a run by a scoped user. Verify: paste the transcript.
- [ ] 12.3 **Enterprise**: an air-gapped host — copy the blob store and bundles offline and
      restore them elsewhere, confirm nothing in publish reaches the internet, query
      provenance by `published_by`/`published_at`, and show a stub external authentication
      provider resolving a subject while **authorization stays local**. Verify: paste the
      transcript.

## 13. Documentation

- [ ] 13.1 Fix the stale README: `README.md:187-188` documents `wfx skill list` and
      `wfx skill install`, but the real verb is `wfx install --skills`
      (`apps/api/cmd/wfx/skillinstall.go`). Verify: every command block in the README is
      run and its output pasted; nothing documented that does not exist.
- [ ] 13.2 Add the missing sections to the README: `wfx login` / `wfx logout`, named host
      contexts, and `wfx publish` — none are mentioned today. Verify: a reader who has only
      the README can log in and publish.
- [ ] 13.3 Deployment notes: the loopback exemption and its two honest gaps, the boot
      refusal on a non-loopback bind with zero users, and the fact that the default bind is
      already `127.0.0.1:8090` and the container/k8s manifests state `WFX_ADDR=:8090`
      themselves. Verify: an operator following the notes reproduces both boot behaviours.
- [ ] 13.4 State the deferrals in the docs so they are not assumed: OIDC/LDAP, token refresh
      and rotation, bundle signing (provenance is a recorded publisher, **not**
      tamper-evidence), dependency resolution between bundles, a public registry, blob GC,
      and the audit-log schema. Add any that gained a trigger to `docs/not-now.md`. Verify:
      the deferral list matches the proposal's "NOT in this change" line for line.

Dependency order, top to bottom. Groups 1–4 are the reading half (`use:` resolves a remote)
and end at a `[SHIP]` boundary: a workflow can consume a published bundle before anything can
write one. Groups 5–7 are the writing half. Group 8 is the requirement checking, which needs the
manifest from 5. `[SEC-TEST]` marks a rule that must land with its test.

Prerequisite, not a task: `publish-from-your-own-agent` is implemented — `wfx publish`/`pull`,
`internal/bundle`, `api/bundles.go`, `store/bundles.go`, migration `000011_published_bundles`.
This change extends that working code; it does not replace it.

## 1. The reference grammar

- [ ] 1.1 Add remote parsing to the `Use` struct in `apps/api/internal/workflow/task.go` (~line 31):
      a value containing `@` is a remote reference, everything else stays a local task name.
- [ ] 1.2 Implement the host rules from design §2: a first segment with no dot resolves to
      `github.com`; a first segment containing a dot is the host itself. Table test both, plus an
      SSH-style URL and a bare path.
- [ ] 1.3 [SEC-TEST] A reference must not be able to name a local path or traverse out of a
      checkout. Assert `../`, absolute paths and `file://` are refused with a reason.
- [ ] 1.4 Verification: `go test ./internal/workflow/...` and a parse table covering every form in
      the `remote-use` spec, including the two that must stay LOCAL.

## 2. Fetch and cache

- [ ] 2.1 Fetch a reference into the existing `apps/api/internal/blob` store — read it first; do not
      add a second storage mechanism. Key by resolved commit, never by tag.
- [ ] 2.2 Record the resolved COMMIT for every run, so a moved tag cannot change what a past run
      did. This is the reproducibility rule from design §3.
- [ ] 2.3 A cache hit makes no network call. Assert it by fetching twice with the remote made
      unreachable on the second attempt.
- [ ] 2.4 The offline path: a host with no network and a warm cache resolves; a host with no network
      and a cold cache fails naming the reference and the cache it looked in.
- [ ] 2.5 Verification: the three scenarios above, observed, plus `wfx dryrun` on a workflow with a
      remote `use:` reporting whether it would resolve — WITHOUT fetching if the cache is warm.

## 3. Expansion

- [ ] 3.1 Expand a fetched bundle's task through the SAME path a local task takes
      (`task.go:123`), so `with:`, `as:`, `consumes:`/`produces:` and the ADD-only `Override`
      semantics are unchanged. A remote task that behaves differently from a local one is a bug.
- [ ] 3.2 [SEC-TEST] A remote task may not loosen policy. `Override` is ADD-only and
      first-deny-wins for guardrails; assert a fetched task cannot remove a guardrail or widen a
      tool allowlist.
- [ ] 3.3 Skills carried by a fetched bundle resolve by prepending a bundle-scoped root, exactly as
      `wfx pull` already does — reuse `PrependSkillRoot`, do not add a second resolution order.
- [ ] 3.4 Verification: a workflow using a remote task runs, and its step loads a skill that exists
      ONLY inside the fetched bundle.

## 4. `[SHIP]` — consuming a published bundle works

- [ ] 4.1 End to end, observed: publish a bundle to a local bare repo, `use:` it from a second
      workflow on a host holding none of its skills, run it, paste the output.
- [ ] 4.2 `wfx dryrun` reports an unresolvable remote reference as a dry-run failure, not a runtime
      one — the repo's whole static-pass claim depends on this.
- [ ] 4.3 Update `skills/workflow-author/` so the authoring skill knows the remote form. It teaches
      the local `use:` today and nothing else.

## 5. The published tree, and the manifest

- [ ] 5.1 Add a tree writer beside the tar writer in `internal/bundle/pack.go`, same manifest
      (design §1). The committed tree is what a reviewer reads in a PR; the tarball is derived.
- [ ] 5.2 Extend the manifest with the execution requirements from design §7: each step's
      `provider:` and its KIND, every `runs-on:` label, every MCP server named.
- [ ] 5.3 [SEC-TEST] The manifest records a requirement and NEVER a credential. Assert an `http`
      provider contributes only its `apiKeyEnv` NAME, and that no env value appears anywhere in a
      written manifest.
- [ ] 5.4 Verification: publish a workflow with a `cli` provider, an `http` provider and a
      `runs-on:` label; read the manifest back and assert all three recorded, no values.

## 6. `wfx publish --to <git remote>`

- [ ] 6.1 Add the `--to` sink. Without it, behaviour is exactly as today (the host path).
- [ ] 6.2 Run every existing refusal BEFORE a commit is made: `looksLikeSecret`
      (`catalog/save.go:85`), unresolvable reference, digest mismatch. A refused publish must leave
      no commit, no tag and no push.
- [ ] 6.3 Write under `workflows/<name>/<version>/`, commit, tag `<name>/v<version>`, push.
- [ ] 6.4 Immutability is git's: a tag that already exists is a push that fails. Assert the failure
      is reported as the same refusal the server path gives for a duplicate version — one
      vocabulary, two mechanisms.
- [ ] 6.5 Narrow the no-token refusal at `publish.go:41-42` to the HOST sink only. With `--to`, no
      token is needed and none is requested — auth is git's (design §5).
- [ ] 6.6 Verification: publish to a local bare repo, `git log`/`git tag` the result, and show the
      refused cases leaving the repo untouched.

## 7. The server index becomes a mirror

- [ ] 7.1 Keep `api/bundles.go`, `store/bundles.go` and `000011_published_bundles`, reframed: the
      index answers "what is running here", git answers "where does this live and who approved it".
      No migration; no working feature is removed.
- [ ] 7.2 Record the remote and commit on an indexed bundle that came from git, so the two views
      join.
- [ ] 7.3 Demote `published_bundles.published_by` per design §5 — git's commit author is the
      provenance now. Keep the column; stop treating it as the source of truth.

## 8. Requirement checking on pull

- [ ] 8.1 Check a pulled bundle's recorded requirements against the receiving host, reusing
      `engine/doctor.go` `checkProvider` rather than writing a second readiness path.
- [ ] 8.2 [SEC-TEST] Refuse naming every unmet requirement and what would satisfy it, in the same
      vocabulary as the publish-time refusals. A refused pull installs nothing.
- [ ] 8.3 A `runs-on:` label no worker in the pool holds is an unmet requirement — the check that
      `wfx dryrun` already does locally, done at the receiving end.
- [ ] 8.4 [SEC-TEST] The platform never accepts, stores or forwards a `cli`/`acp` provider's
      credential. Assert the refusal offers a command the operator runs themselves and nothing else.
- [ ] 8.5 Presence is NOT authentication: a `cli` requirement satisfied by a PATH lookup must not be
      reported in a way that implies the run will succeed. `doctor.go`'s own comment says a cli
      check is a PATH lookup only; make the wording honest at the pull site too.
- [ ] 8.6 A workflow with no providers, labels or MCP servers records nothing and triggers no check
      — absent, not empty.

## 9. Three-scale check, before this is done

- [ ] 9.1 One person: a workflow with only local `use:` entries behaves exactly as today. No git
      call, no cache, no remote, no account. Verify on a fresh install with nothing configured.
- [ ] 9.2 A small org: publish to a repo they already have, with the access control they already
      run, and consume it from another machine.
- [ ] 9.3 An enterprise: a non-GitHub remote, pinned by commit, resolving with no network from a
      warm cache. Verify against a bare repo over SSH, not github.com.

## 10. Documentation

- [ ] 10.1 Supersede the storage half of ADR 0018 — write the superseding note IN 0018 rather than
      editing its decision, so the reasoning stays readable. The bundle decision survives intact.
- [ ] 10.2 README: `use: acme/bug-fix@v1.2.0` beside the existing `uses:` explanation, and the
      offline path stated before the GitHub one, as the install section already does.
- [ ] 10.3 Record in `docs/not-now.md`: transitive `use:`, signing policy, and present-vs-
      authenticated for a CLI provider — each with the trigger that would change our mind.

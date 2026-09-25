Dependency order, top to bottom. Groups 1–4 are the reading half (`use:` resolves a remote)
and end at a `[SHIP]` boundary: a workflow can consume a published bundle before anything can
write one. Groups 5–7 are the writing half. Group 8 is the requirement checking, which needs the
manifest from 5. `[SEC-TEST]` marks a rule that must land with its test.

Prerequisite, not a task: `publish-from-your-own-agent` is implemented — `wfx publish`/`pull`,
`internal/bundle`, `api/bundles.go`, `store/bundles.go`, migration `000011_published_bundles`.
This change extends that working code; it does not replace it.

## 1. The reference grammar

- [x] 1.1 Add remote parsing to the `Use` struct in `apps/api/internal/workflow/task.go` (~line 31):
      a value containing `@` is a remote reference, everything else stays a local task name.
- [x] 1.2 Implement the host rules from design §2: a first segment with no dot resolves to
      `github.com`; a first segment containing a dot is the host itself. Table test both, plus an
      SSH-style URL and a bare path.
- [x] 1.3 [SEC-TEST] A reference must not be able to name a local path or traverse out of a
      checkout. Assert `../`, absolute paths, `~` and a drive letter are refused with a reason.
      **DECIDED 2026-09-25, reversing this task's original wording**, which said `file://` was
      refused and contradicted `specs/remote-use`'s "an explicit URL is used as given":
      `file://` IS ALLOWED. A bare repo on a mounted path is how an air-gapped site works and
      task 9.3 requires a non-GitHub remote; an explicit URL is an operator naming a remote, not a
      path smuggled in. The danger this task was reaching for is a BARE path or a traversal being
      read as a remote, and that is still refused.
- [x] 1.3a [SEC-TEST] **Added after the fact, and it is why the escape list had to go.** The rule
      now ENUMERATES ALLOWED SCHEMES (`https`, `http`, `ssh`, `git`, `file`, plus scp-style)
      instead of listing bad shapes, because the escape list missed `ext::` — and
      `git clone 'ext::sh -c <command>'` RUNS that command. A `use:` comes from a workflow
      somebody else published, so that is the untrusted input path. An allowlist refuses `ext::`
      and every transport helper git gains later without us learning their names. This is
      docs/not-now.md's own rule: a mechanism that works enumerates inclusions.
- [x] 1.4 Verification: `go test ./internal/workflow/...` and a parse table covering every form in
      the `remote-use` spec, including the two that must stay LOCAL.

## 2. Fetch and cache

- [x] 2.1 Fetch a reference into the existing `apps/api/internal/blob` store — read it first; do not
      add a second storage mechanism. Key by resolved commit, never by tag.
- [x] 2.2 Record the resolved COMMIT for every run, so a moved tag cannot change what a past run
      did. This is the reproducibility rule from design §3.
- [x] 2.3 A cache hit makes no network call. Assert it by fetching twice with the remote made
      unreachable on the second attempt.
- [x] 2.4 The offline path: a host with no network and a warm cache resolves; a host with no network
      and a cold cache fails naming the reference and the cache it looked in.
- [x] 2.5 Verification: the three scenarios above, observed, plus `wfx dryrun` on a workflow with a
      remote `use:` reporting whether it would resolve — WITHOUT fetching if the cache is warm.

## 3. Expansion

- [x] 3.1 Expand a fetched bundle's task through the SAME path a local task takes
      (`task.go:123`), so `with:`, `as:`, `consumes:`/`produces:` and the ADD-only `Override`
      semantics are unchanged. A remote task that behaves differently from a local one is a bug.
- [x] 3.2 [SEC-TEST] A remote task may not loosen policy. `Override` is ADD-only and
      first-deny-wins for guardrails; assert a fetched task cannot remove a guardrail or widen a
      tool allowlist.
- [x] 3.3 Skills carried by a fetched bundle resolve by prepending a bundle-scoped root, exactly as
      `wfx pull` already does — reuse `PrependSkillRoot`, do not add a second resolution order.
- [x] 3.4 Verification: a workflow using a remote task runs, and its step loads a skill that exists
      ONLY inside the fetched bundle.

## 4. `[SHIP]` — consuming a published bundle works

- [x] 4.1 End to end, observed: publish a bundle to a local bare repo, `use:` it from a second
      workflow on a host holding none of its skills, run it, paste the output.
- [x] 4.2 `wfx dryrun` reports an unresolvable remote reference as a dry-run failure, not a runtime
      one — the repo's whole static-pass claim depends on this.
- [x] 4.3 Update `skills/workflow-author/` so the authoring skill knows the remote form. It teaches
      the local `use:` today and nothing else.

## 5. The published tree, and the manifest

- [x] 5.1 Add a tree writer beside the tar writer in `internal/bundle/pack.go`, same manifest
      (design §1). The committed tree is what a reviewer reads in a PR; the tarball is derived.
- [x] 5.2 Extend the manifest with the execution requirements from design §7: each step's
      `provider:` and its KIND, every `runs-on:` label, every MCP server named.
- [x] 5.3 [SEC-TEST] The manifest records a requirement and NEVER a credential. Assert an `http`
      provider contributes only its `apiKeyEnv` NAME, and that no env value appears anywhere in a
      written manifest.
      **The NAME half was missed and is now done (2026-09-25).** The `Requirement` type was written
      from design §7's table, which lists the provider and its KIND and not the variable — so the
      first implementation could record no name and said so rather than guessing. `apiKeyEnv` is a
      NAME, the same thing `catalog.Provider` already carries and `LooksLikeSecret` already refuses
      a value in, so it travels. It is a HINT and not a rule: the receiving host reads its OWN
      variable for its own provider entry. Naming the publisher's variable only answers "a key is
      needed, and over there it was called this", which is what makes the refusal actionable
      instead of merely true.
- [x] 5.4 Verification: publish a workflow with a `cli` provider, an `http` provider and a
      `runs-on:` label; read the manifest back and assert all three recorded, no values.

## 6. `wfx publish --to <git remote>`

- [x] 6.1 Add the `--to` sink. Without it, behaviour is exactly as today (the host path).
- [x] 6.2 Run every existing refusal BEFORE a commit is made: `looksLikeSecret`
      (`catalog/save.go:85`), unresolvable reference, digest mismatch. A refused publish must leave
      no commit, no tag and no push.
- [x] 6.3 Write under `workflows/<name>/<version>/`, commit, tag `<name>/v<version>`, push.
- [x] 6.4 Immutability is git's: a tag that already exists is a push that fails. Assert the failure
      is reported as the same refusal the server path gives for a duplicate version — one
      vocabulary, two mechanisms.
      **This task's mechanism is only half the story, and the difference is worth writing down.**
      For an ordinary duplicate publish the version DIRECTORY is already in the clone, so the tree
      writer refuses before a commit exists and the remote never gets asked. The push rejection is
      what fires in the other case: the tag taken but the tree absent. Both paths are tested and
      both give the duplicate-version wording, so the task's INTENT holds — one vocabulary — but
      "immutability is git's" is the backstop, not the usual path. That is the right way round: a
      local refusal is cheaper and the remote still cannot be talked out of it.
- [x] 6.5 Narrow the no-token refusal at `publish.go:41-42` to the HOST sink only. With `--to`, no
      token is needed and none is requested — auth is git's (design §5).
- [x] 6.6 Verification: publish to a local bare repo, `git log`/`git tag` the result, and show the
      refused cases leaving the repo untouched.

## 7. The server index becomes a mirror

- [x] 7.1 Keep `api/bundles.go`, `store/bundles.go` and `000011_published_bundles`, reframed: the
      index answers "what is running here", git answers "where does this live and who approved it".
      No migration; no working feature is removed.
- [x] 7.2 Record the remote and commit on an indexed bundle that came from git, so the two views
      join.
- [x] 7.3 Demote `published_bundles.published_by` per design §5 — git's commit author is the
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

## 9a. `wfx bundle import` — the verb that populates the join

- [ ] 9a.1 **Added 2026-09-25, from two independent signals.** `wfx bundle import` appears in
      `specs/bundle-cache` and in no task, so it was never built; and task 7.2's `git_remote` /
      `git_commit` columns now exist, are carried through the API, and are set by NOBODY — because
      `publish --to` bypasses the host entirely by design. The columns are the receipt that a host
      has SEEN a git-published bundle, and importing is the act that writes one. Until this exists
      the join is possible and never populated, which is a schema that documents an intention
      rather than a fact.
- [ ] 9a.2 Import a bundle from a git remote into this host's index: resolve the reference the way
      `use:` already does (reusing the resolver, not a second fetch path), verify the digest, and
      record the remote and the resolved COMMIT. A moved tag must not change what a past import
      recorded.
- [ ] 9a.3 Run the group 8 requirement check as part of the import, so a bundle this host cannot
      run is refused at import rather than at run time.

## 10. Documentation

- [x] 10.1 Supersede the storage half of ADR 0018 — write the superseding note IN 0018 rather than
      editing its decision, so the reasoning stays readable. The bundle decision survives intact.
- [x] 10.2 README: `use: acme/bug-fix@v1.2.0` beside the existing `uses:` explanation, and the
      offline path stated before the GitHub one, as the install section already does.
- [x] 10.3 Record in `docs/not-now.md`: transitive `use:`, signing policy, and present-vs-
      authenticated for a CLI provider — each with the trigger that would change our mind.

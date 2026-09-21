# ADR 0007 — Pin toolnexus; a live working tree is not a dependency

- **Status:** accepted
- **Date:** 2026-09-22

## Context

While seven upstream issues were being fixed, `apps/api` used a `replace`
directive onto a local toolnexus checkout so fixes could be verified as they
landed. That was right for verification and wrong for building a product.

A second session working in this repository repointed that `replace` at a third
checkout. A full green test run then proved nothing about which branch it ran
against — and it took `go list -m -f '{{.Dir}}'` to notice.

## Decision

`apps/api` pins the **published** release and carries no `replace`. The platform
uses no unreleased API, so a release is sufficient.

`spikes/` keeps its `replace` onto the working tree: that module exists to
verify unreleased behaviour, which is exactly what a moving target is for.

Upgrades are deliberate events, recorded in `docs/toolnexus-upgrade.md` with the
assertions to add when they happen.

## Consequences

- Our verification is pinned one revision behind by choice. Acceptable: the
  alternative is a build whose behaviour depends on someone else's editor.
- Any file needing an unreleased symbol is parked behind a build tag rather than
  deleted (see `internal/devinadapter` and the `toolnexus_inprocess` tag).
- When several agents share a repository, `GOWORK` is how you build against a
  different checkout — never by editing a shared `go.mod`.

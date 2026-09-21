# ADR 0011 — One registry type for everything a step names

- **Status:** accepted
- **Date:** 2026-09-22

## Context

A step needs skills, built-in tools, an LLM provider, a judge backend and MCP
servers. Each arrived separately, and each grew its own shape: skills had a real
registry, MCP was re-read from `mcp.json` inside `buildToolkit`, the provider was
global config plus a bare `model:` string on a step, and the classifier was
config only.

The costs showed up immediately. `mcp: [githb]` was a run-time failure rather
than a load-time one. A step could not use a different model family from its
neighbour without editing Go. A workflow carried an endpoint in its YAML, so it
did not move between machines.

## Decision

**One generic registry type, four registries, and a step names an entry.**

`internal/registry.Registry[T]` provides the whole surface once: `Get`, `Has`,
`List`, `Names`, `Missing`, `Require`, and a `Skips` record so a rejected entry
is visible instead of silently absent. First name wins; later duplicates are
recorded rather than overwriting.

`internal/catalog` defines the entries — `Provider`, `Classifier`, `McpServer` —
loaded from `registries.json`, with `mcp.json` merged so the existing file keeps
working. Skills keep their filesystem discovery and expose the same verbs.

A step now says `provider: sonnet`, `classifier: jev`, `mcp: [github]`, and
every name is validated at **load** time against the registry that owns it.

A provider is not only an HTTP endpoint. `kind` is `http`, `cli` or `acp`, so a
coding agent — devin, opencode, the local `claude` CLI, copilot — is a provider
like any other, and a step chooses one by name.

## Consequences

- A typo is a boot error naming the registry and listing what it knows.
- Workflows are portable: names travel, endpoints and credentials do not.
  `apiKeyEnv` is the NAME of an environment variable; a key is never a value in
  a registry file.
- A new capability is a new entry, not a new field: adding a provider is a JSON
  object, not a Go change.
- `cli` and `acp` providers depend on the devin adapter, which is parked behind
  the `toolnexus_inprocess` build tag until upstream exports the transport
  (issue #95). They register and validate today; resolving one in a build
  without the tag fails loudly rather than silently falling back to the default.

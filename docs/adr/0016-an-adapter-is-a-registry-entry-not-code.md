# ADR 0016 — An adapter is a registry entry, not code

- **Status:** proposed
- **Date:** 2026-09-24

## Context

The 2026-09-24 competitor pass killed most of what we claimed. Typed step
schemas are Mastra's too. Per-step tool scoping is Mastra's and GitHub's, and
Mastra's is better. "Nobody ships a static validation pass" is false —
`mastra lint` exists. One claim survived: **the execution backend is a swappable
detail.** Observed in a real boot on this machine, one step declaration over
`claude-cli` (cli), `copilot-cli` (cli), `devin` (acp), `opencode` (cli) and
`gpt`/`haiku`/`sonnet` (http). toolnexus (ADR 0001) is what makes those
interchangeable, which is why the Go foundation is the reason the claim exists
rather than an implementation note.

Everyone else ships a runtime you must live inside: Mastra binds you to their TS
runtime, gh-aw to GitHub plus Copilot, Devin and Jules are their own model. We
ship a unit that travels. That only stays true if adding a backend never
requires us.

## Decision

**Adding support for a model or agent backend is a registry entry. It is never a
Go change.** Three kinds, as ADR 0011 already named them:

- `http` — `baseUrl` + `style` + `model` + `apiKeyEnv`.
- `cli` — an explicit `command` argv template plus `modelFlag`.
- `acp` — an agent process speaking the Agent Client Protocol.

The presets `devin`, `claude` and `copilot` are **conveniences only**. The
generic `command` template is the real mechanism, and it is what makes any agent
CLI usable without the engine learning its name — `provider.go` gives it
precedence over a preset for exactly that reason. `opencode` proves it end to
end: no Go knows the word, and it runs.

**We refuse an unknown preset rather than guessing it.** That rule is already in
the code, and the comment states why: a wrong argv template fails as a parse
error twenty turns in, which is the worst place to learn the name was unknown.
Generalise it — **we do not ship an adapter whose non-interactive invocation we
have not verified by running it.** The `{{prompt}}`/`{{file}}` placeholder check
in `validateProvider` came from making that mistake against a live `opencode`.

## The legal boundary

This must be precise, because the whole decision rests on it.

**We redistribute nothing.** We exec a binary the user installed and
authenticated with their own account, using their own credential, on their own
machine. That is the bargain a CI runner or a Jenkins node makes, and it is the
same bargain here.

An `http` provider is a public documented endpoint plus the user's own key.
`apiKeyEnv` is the **name** of an environment variable; a key is never a value
in a registry file.

What we must not do: bundle or redistribute a vendor's binary; proxy or resell
access to one; present a subscription seat as an API.

Several agent CLIs' terms restrict using a subscription seat as an automated
backend. That is the operator's call with the operator's own account — but the
registry entry must **say so in its `description`** rather than quietly
encourage it. `claude-cli` already says "uses your own subscription rather than
an API key". Honesty in the description is the mechanism; there is no other one,
and we should not pretend to enforce a vendor's terms we cannot read.

## The three-scale test

Every decision in this repo now has to hold at all three.

- **One person.** Zero config. Whatever CLI is already on their PATH, and no key
  at all if they use `claude-cli`.
- **A small org.** A shared registry of named providers, committed; credentials
  per machine. This is ADR 0011's portability claim, used.
- **An enterprise.** Their **own** models — self-hosted vLLM or Ollama behind an
  `http` entry, Bedrock or Azure, or their licensed CLI pinned to a worker label
  (ADR 0015). Data never leaves, and there is no vendor lock-in. Neither Mastra
  nor gh-aw can make this argument: both terminate at a runtime someone else
  owns.

## Consequences

- A new backend is a JSON object and a verification run, not a release.
- Portability is only **true** if a workflow that passes on `sonnet` also passes
  on `opencode`. The contract gate helps — `submit_output` validates identically
  on every backend — but capability does not. A CLI agent with no tool loop and
  a frontier model over HTTP are not the same thing behind one name.
- So this ADR **creates an obligation it does not discharge**: evals must prove
  portability per backend. That is a separate decision and is deliberately not
  made here. It is the reason evals moved from "a gap we skipped" to the proof
  of the core claim.
- `cli` and `acp` still depend on the devin adapter parked behind the
  `toolnexus_inprocess` build tag (ADR 0011, issue #95). This ADR does not
  change that.

# ADR 0017 — Egress is deny-by-default

- **Status:** accepted
- **Date:** 2026-09-22

## Context

A step runs an agent with `bash`, a git checkout of somebody's repository, and
whatever secrets the machine's environment holds. It has unrestricted network
access. Nothing in the platform stops a step from POSTing the working tree, the
environment, or a scraped credential to an arbitrary host — and no guardrail can,
because a guardrail inspects tool *calls* and `curl` is one call among a thousand
shapes (`python -c`, `nc`, a git remote, a DNS query).

ADR 0004 argues that scoping is the security model, and it is right about tools.
Egress is the one part of the attack surface that scoping cannot reach: the
tools a step legitimately needs are exactly the tools that can exfiltrate.

This is also the axis where the threat is not hypothetical. A workflow step
reads a bug report written by a stranger and feeds it to a model that can run
commands. Prompt injection is the normal case, not the edge case.

## Decision

**A step gets no network unless its egress allowlist says otherwise, and a
policy that fails to apply fails the step.**

- The sandbox (ADR 0016) starts with `--network=none` — measured at 0.04 ms, so
  the default costs nothing.
- Network is reintroduced through a **CONNECT proxy** the runner owns, with a
  per-step allowlist of hosts. The typical allowlist is two entries: the model
  endpoint and the git host. Nothing else.
- The allowlist is **hosts, not CIDRs**. A step names `openrouter.ai`, not an
  address range, because the address is not what the author knows and DNS moves.
- **No allowlist means no network at all** — not "inherit the default". A step
  that needs the network says so, in the same file that says which tools it
  needs, reviewed the same way.
- **Failure to apply the policy fails the step**, with kind `unavailable`
  (ADR 0016a). The alternative — running with the network open because the proxy
  did not start — is the failure mode ADR 0016 refuses for Seatbelt, for the
  same reason: a protection that degrades silently is worse than none, because
  the documentation goes on claiming it.

## Why a proxy rather than firewall rules

Rules need root and are per-host; the proxy is a process the runner already
starts, works identically on macOS and Linux, and can *log* what a step tried
to reach. The log is worth as much as the block: "this step attempted
`api.attacker.example`" is the signal that an injection succeeded, and a dropped
packet is silent.

## Consequences

- **This is the only real defence against exfiltration we will have.** Stated
  plainly so it is not traded away for convenience later.
- Steps that fetch dependencies (`go mod download`, `npm install`) need the
  registry on their allowlist, or the sandbox needs a warm module cache. The
  second is better and is where the container image earns its keep.
- MCP servers that talk to the network are subject to the same allowlist. A
  step naming an MCP server implicitly needs that server's hosts, and the
  registry entry (ADR 0011) is where that belongs — so an MCP entry gains an
  egress field rather than each step repeating it.
- Until ADR 0015 and 0016 ship, this is unimplementable. It is recorded now so
  that nothing built in between assumes open network as a property.

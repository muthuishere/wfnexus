# 0029: Modules are global singletons that own shared state

Status: proposed · 2026-11-08 · issue #109 · amends 0028

## Context

Workflows are many and short-lived, and they run concurrently. Some state must exist exactly once, however many workflows touch it:
- a WhatsApp connection and its sender (one writer per number);
- read cursors and dedupe;
- the echo ledger of what we sent;
- per-site write rate limits (for example, 1 post every 5 minutes);
- logged-in browser sessions;
- queues and debounce.

The 14-day retro shows what goes wrong without that, for example an alert storm of 612 rows with empty keys. The CEO's asks for a per-site rate limit and a Mac browser lane are both about state shared across workflows.

## Decision

1. **A module exists to own shared state.** It is a global singleton per server and account (#107 supervision: one copy, heartbeat, takeover). The rule: if two workflows could clash over something, a module owns it, and workflows go through the module.
2. **A module wraps a CLI.** Core modules are `apl` (people: WhatsApp, mail, Teams, calendar, Drive), `gh` (code) and `aos` (machine, on opted-in workers only, with writes gated by approval).
   - A **command** is one call with typed JSON in and out.
   - A **service** is a long-running loop that keeps the state above.
   - AI is allowed but not required. Usually the AI lives in the calling step, and a skill drives the module through its CLI.
3. **Global, with profiles.** A core module is installed once per server and is available in every project. A **profile** is a named login plus defaults, for example `whatsappbiz` for apl with the WhatsApp Business handle. Each module has a default profile, so `uses: apl.whatsapp-send` needs nothing more. A profile holds handle names, never secrets.
4. **Everything calls everything through the core:**
   - workflow → workflow (`start:`);
   - workflow → module (`uses:`);
   - module service → workflow (hook);
   - CLI, HTTP or MCP → either.

   The same approval and audit apply on every path. A send defaults to `draft`.

## Deliberately not yet (MVP first; add when a real workflow needs it)

- per-project or per-workflow narrowing;
- composite modules (modules built from other modules);
- MCP exposure of module commands;
- `permissions:` blocks;
- signing and trust tiers (#85);
- module → workflow `start` from a command;
- `binary: carry` (#102).

## Consequences

- A workflow never holds a connection, cursor or rate counter itself. If it needs one, that is a missing module command.
- Getting a single owner for this state depends on #107: when a copy goes silent, another worker takes it over.
- This ADR and the second `0028` keep their numbers. The `0028` collision (modules / auth) stays as history.

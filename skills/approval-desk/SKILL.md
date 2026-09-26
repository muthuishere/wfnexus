---
name: approval-desk
description: See what wfnexus runs are waiting on a human and resolve them — approve, reject, or answer a question. Trigger: what is waiting on me, approve that run, answer the agent's question.
---
# The approval desk

A paused run is a run that stopped ON PURPOSE, because a step needs a human before
it goes further — merging, deploying, anything outward-facing. There are three
surfaces for resolving one: the web UI, `wfx` on the command line, and this skill.
This is the third, for when the human would rather tell their agent than open a tab.

## The one rule that makes this safe

**Record yourself, never the human.** The actor on a resolution is an audit record
(ADR 0021), and a pause satisfied by an agent that signed the human's name is not a
human in the loop — it is a human in the logs. So:

```sh
wfx approve <run-id> --as "agent:claude (for muthu)"
```

Never `--as muthu`. If the human wants their own name on it, they type it
themselves; that is the whole point of the pause.

**`--as` is not optional for you.** Left out, the CLI fills it in from the
environment — `$USER@hostname` (`cmd/wfx/main.go` `actorOf`) — which is the logged-in
human. So an agent that simply runs `wfx approve <run-id>` signs THEIR name to it,
silently, and the audit record then says a person approved something no person saw.
That default is right for a human at their own terminal and wrong for everything
else. The platform now RECORDS that it could not tell: a resolution whose actor the
client filled in from the environment is written as
`muthu@laptop (via cli) [inferred from the environment; nobody asserted it]`, so an
audit line never presents a guess as a decision. That makes your omission visible
rather than invisible — it does not make it correct. Pass `--as` naming yourself.

**Approve only what the human told you to approve, in this conversation.** A list of
things waiting is not permission to clear the list.

## What is waiting

```sh
wfx runs --json                 # every recent run, newest first
wfx show <run-id>               # a run, step by step
wfx logs <run-id>               # the activity log, including why it paused
```

A run waiting on a person is `awaiting_approval` (stopped BEFORE a step runs) or
`needs_input` (a step asked a question and is holding). They resolve differently, so
read which one it is before acting.

## Resolving

```sh
wfx approve <run-id> --as "<who>"            # awaiting_approval → the step runs
wfx reject  <run-id> -m "why"  --as "<who>"  # awaiting_approval → the run is cancelled
wfx answer  <run-id> -m "text" --as "<who>"  # needs_input → the answer goes back to the step
```

A rejection **cancels the run**, so the reason is the only thing a person reading it
later will have. "no" is not a reason; "the migration is missing its down file" is.

## Before you approve anything, read what you are approving

The pause exists because the next thing is expensive or hard to undo. So report to
the human what the step will actually do, from the run — not from the workflow's
name:

1. `wfx show <run-id>` — which step is waiting, and what the previous steps produced.
2. `wfx logs <run-id>` — the step's own output and any `ask_human` question verbatim.
3. Say what happens on approve in one line, then ask, then act.

A question you cannot answer from the run is a question for the human. Say you do
not know rather than guessing on their behalf: a wrong approval is not recoverable
by a later message.

## When nothing arrives

If nobody is being told a run paused, the platform has no notifier configured — the
mechanism exists (`kind: webhook` in the notifiers registry, with `secretEnv`
naming the token and never holding it) and a fresh install ships none, because a
URL belongs to an install and not to a default. `wfx registry notifiers` shows what
is configured. That is a setup gap, not a bug, and it is worth saying out loud when
somebody asks why a run sat overnight.

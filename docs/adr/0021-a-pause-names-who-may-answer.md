# ADR 0021 — A pause names who may answer

- **Status:** proposed
- **Date:** 2026-09-24

## Context

The engine can already stop and wait, in three unrelated ways.

- A gate with `action: needs_input` parks the run (`engine.go:605`,
  `schedule.go:265`, `:332`).
- `requires_approval` on a step halts **before** the model is called
  (`workflow/task.go:62`, `engine.go:557`, `schedule.go:161`).
- `ask_human: true` grants a raw `ask_human` tool that returns
  `tn.Pending(tn.Request{Kind:"input"})`; with no `WaitFor` configured the run
  halts durably and the engine stores the `Request` on the step
  (`step.go:187-218`, `schedule.go:209-213`, `model.go:58`).

Four endpoints resolve them — `approve`, `reject`, `input`, `answer`
(`api.go:134-137`). Two things are missing, and both are the same omission.

**Nobody is told.** `needs_input` and `awaiting_approval` are strings the UI can
render. A run that pauses at 02:00 waits until somebody happens to look.

**Nobody is named.** `Engine.Approve` (`engine.go:374`) takes `runID` and
`stepID`. It takes no actor, checks no permission, and writes
`Status: "approved"` and nothing else. There is no record of who approved, and
under ADR 0017 there is now a subject that could have been recorded.

### What toolnexus already gives us, and what we use of it

Verified in `toolnexus/golang@v0.19.0`. `Request` (`types.go:64`) carries `ID`,
`Kind` — an **open** vocabulary, `"authorization" | "approval" | "input"` —
`Prompt`, `Data`, `URL` "present when the action happens at a link", and
`ExpiresAt`. `Answer` (`types.go:73`) carries `Ok`, `Data`, and a `Reason` of
`"declined" | "cancelled" | "expired"`. `ClientOptions.WaitFor`
(`client.go:98`) resolves a suspension inline; absent, `Run` halts with
`RunStatusPending` (`statusvocab.go:30`) — the two postures `pending_test.go`
names A and B.

We take path B only, and we use roughly a third of the surface:

- **`WaitFor` is never set** anywhere in `apps/api`. Deliberate and right for
  durability, but it means there is no inline posture at all.
- **A hook can raise a suspension.** `suspension_pathb_test.go` covers exactly
  the case a policy gate ships on: `BeforeToolCall` returns a `Request`, no
  `WaitFor`, and the run halts carrying the *hook's* Request. We build `Hooks`
  (`step.go:263`) for guardrails and never suspend from one — so a dangerous
  tool call can only be denied, never escalated.
- **`Request.URL` and `ExpiresAt` are unread.** `URL` is the field an approval
  link belongs in; `ExpiresAt` is a timeout the wire format already has.
- **`Answer.Ok` / `Reason` are discarded.** `AnswerQuestion(…, answer string)`
  (`engine.go:404`) carries a string, so "declined" and "timed out" are the
  same event to us.
- **`StreamEvent{Type:"pending"}` is emitted before `WaitFor`** with the comment
  "so a channel can push the link" (`client.go:1750`, `:2023`). toolnexus
  anticipated notification; we do not consume the event.
- **`MetricEvent.Pending`** (`client.go:197`) labels a suspension as a metric;
  unused, like the cost fields the 2026-09-24 research found discarded.

## Decision

**A pause is one object: a question, a channel to announce it on, and a set of
principals entitled to answer.** The three existing pauses become three `Kind`s
of one `tn.Request` rather than three mechanisms.

### 1. Notify

**One `notifier` interface, one method — deliver a pause — with per-channel
adapters declared in the registry, exactly as ADR 0016 rules for providers and
MCP servers.** A notifier is a registry entry, not a Go change; Slack, Telegram,
email and a generic webhook are entries somebody writes, not packages we ship.

We are **not** building a catalogue of first-party integrations. That is the
mistake `docs/research/competitors-2026-09.md` records against gh-aw: 40+
`safe-outputs` handlers where "every capability needs a compiler change"
(§competitors, and the adopt-list entry "ship **one** generic validated-effect
interface"). The same rule that made `opencode` run with no Go knowing its name
must make a webhook run with no Go knowing the operator's tooling.

The delivery payload is `Request` as it stands: `Prompt` is the question, `URL`
is where to answer it, `ExpiresAt` is how long it stands.

### 2. Ask

`ask_human` already exists and works. The change is **how it is granted**: today
it is a step-level boolean, `ask_human: true` (`workflow.go:230`), which makes
it the one capability in the system outside the per-step allowlist. That is
wrong under ADR 0004 — scoping is the security model, and a capability reached
by a second door is not scoped.

**`ask_human` becomes an ordinary tool name in `tools:`.** A step that may
interrupt a person declares it the way it declares `bash`, is validated at load
time like every other tool, and the boolean is dropped. It is a tool rather
than a workflow construct because a workflow construct can only pause at a
declared boundary, and the whole point is that the agent discovers mid-turn
that it cannot proceed.

The symmetry is worth stating: **a gate pausing and a step asking are one
mechanism pointed in opposite directions.** A gate is the workflow suspending on
the human; `ask_human` is the human's turn requested by the step. Both should
produce a `Request`, both resolve with an `Answer`, and both should reach the
same notifier. Today only the third one does, which is why `Approve` records
nothing and `AnswerQuestion` records a string.

### 3. Who may answer

**A pause names its approvers, resolved against ADR 0017's authorization model
— ours, permanently, and project-scoped.** Borrow the vocabulary rather than
coin it: GitHub Actions **environments** with **required reviewers**, which may
be a user, a team or a role, and its **"prevent self-review"** setting; and the
standard **four-eyes principle** / **segregation of duties** from control
frameworks.

Three rules, in order of how badly they are usually got wrong.

- **The approver is identified and recorded.** Who approved, when, and which
  `Request.ID` — an **audit fact on the step**, queryable, not a log line.
  `Approve` grows an actor and refuses without one wherever auth is present.
- **Segregation of duties.** Where policy says so, the principal who triggered
  the run may not approve it. This is a policy setting, not a constant, because
  at one scale the author is the only person there.
- **A notification is not an authorization path.** Clicking a button in Slack
  must resolve to an identity in *our* model before the approval is accepted.
  **Possession of the channel must never be sufficient.** If it is, the approval
  control is worth exactly as much as the weakest membership list of a chat
  workspace we do not administer — and every person who can post a link into
  that room becomes an approver. A notifier **delivers a pointer and nothing
  more**; the answer is authenticated on our API, by the same token ADR 0017
  mints, whatever channel carried the pointer.

## The three-scale test

- **One person.** No notifier, no auth, no roles. The run pauses, and the
  terminal or the UI says so — which is what it does today. Zero config stays
  zero config, and a pause must **never** require a channel to be configured;
  a notifier that is absent is not an error.
- **A small org.** One Slack or Telegram entry in the registry, a couple of
  named approvers, or the coarse rule "anyone in this project may approve" —
  which ADR 0017's project scope already expresses.
- **An enterprise.** Approvers by role, segregation of duties on, a recorded
  trail of who approved what and when, and a webhook notifier pointed at their
  own system because run data does not go to a third-party chat. The notifier
  being a registry entry is what makes that last one possible without us.

## Consequences

- One representation for a pause replaces three. `Approve`, `ProvideInput` and
  `AnswerQuestion` converge on one resolve path carrying an `Answer`, so
  `Ok`/`Reason` stop being thrown away and a decline stops looking like a
  timeout.
- The engine grows an outbound dependency it did not have. A pause that cannot
  be delivered must still be a pause — the run parks either way.

**Deliberately not decided here.**

- **Timeout on an unanswered pause.** `Request.ExpiresAt` exists and we ignore
  it. Whether an expired pause fails the run, escalates to another approver, or
  waits forever is open, and the honest default is probably "waits forever".
- **Delivery guarantees for a notifier.** Retry, ordering, and what a run should
  do when every channel is down.
- **Delegation.** Whether an approver can hand their authority to someone else,
  and whether that survives segregation of duties.
- **Templating.** What a notification actually says. It will be asked for on
  first contact with a real Slack channel, and it is not this decision.

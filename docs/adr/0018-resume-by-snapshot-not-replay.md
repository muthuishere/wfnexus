# ADR 0018 — Resume by snapshot, not by replay

- **Status:** accepted
- **Date:** 2026-09-22

## Context

ADR 0005 makes the step the durability boundary and, having done so, accepts a
sharp consequence: an answered `needs_input` **re-runs the step from its
prompt**. The transcript is not resumed — the step starts again, reads the
workspace as the previous attempt left it, and works forward.

That choice was right. The alternative at the time was `Runtime.Resume`, which
replays a whole turn and which ADR 0005 explicitly refuses. But it is the
constraint that costs us the most everywhere else:

- **Every step must be idempotent in effect.** ADR 0012's retry adds a second
  reason, so the constraint now has two sources and no escape.
- **Work is re-paid.** A step that spent thirty turns investigating, then asked
  a question, spends thirty more turns re-investigating after the answer. The
  human's answer arrives hours later and the model's context does not survive
  the wait.
- **A long step cannot be interrupted at all.** Cancellation and redeploy throw
  away everything the step learned, because there is nothing to resume from.

We already store the pieces. `saveArtifacts` writes the transcript and the
workspace diff against a persisted `base_ref` on every step.

## Decision

**A suspended or interrupted step is resumed from a snapshot, not replayed from
its prompt.** The snapshot is two things we already persist plus one we do not:

1. the **workspace diff** against `base_ref` — already written per step;
2. the **transcript** — already written per step;
3. the **pending request** and the tool-call id it belongs to, which is what
   lets the answer be delivered as a tool result rather than as new prose in a
   fresh conversation.

On resume the runner restores the worktree from `base_ref` plus the diff,
rehydrates the conversation through toolnexus's `ConversationStore` (present in
the pinned version and unused today), and delivers the answer as the result of
the `ask_human` call that suspended. The step continues on its next turn.

## What this does not change

ADR 0005 stands: **we still never call `Runtime.Resume`.** Replaying a turn and
restoring a conversation are different operations, and the objection in 0005 was
to replay. The step remains the durability boundary; this ADR only changes what
is stored at that boundary — state instead of a starting point.

## Consequences

- **The idempotency requirement relaxes from "must" to "should".** It is still
  worth having for retry after a genuine failure, where the snapshot may be the
  thing that is wrong. But a step is no longer forced to be re-runnable to
  support a human question, which is what forced it in practice.
- The turn budget becomes meaningful across a suspension: the resumed step
  continues its count rather than restarting it, so a step cannot get unlimited
  turns by asking a question. That is a behaviour change and a desirable one.
- A snapshot can be stale. If the repository's base moved while the question
  was open, the restore is against the recorded `base_ref` — which is the reason
  ADR 0005's fix persisted `base_ref` in the first place. The run finishes
  against the world it started in, and rebasing is an explicit step, never
  implicit.
- Storage grows with the diff, not with the repository, because the base is a
  ref and not a copy.
- This needs ADR 0015's runner to have a defined quiescent moment at which the
  filesystem can be snapshotted. Without it, a snapshot races the step's own
  children.

---
name: leader-brief
description: "Draft a leader's cited brief (daily brief, meeting brief, meeting actions, weekly digest, decision log, 1:1 prep) from a collected evidence file — every line cites refs, nothing invented, draft only. Trigger: draft the brief, write draft.json from evidence, leadership brief."
---

# leader-brief — how to draft from evidence

You are drafting for a busy leader who will act on what you write without
re-reading the sources. A wrong line costs more than a missing one. The
evidence was collected read-only by the workflow's `run:` step through
**apl-skill** recipes (mail, calendar, Teams chat, Drive/OneDrive, contacts,
GitHub); you only read files and write `draft.json`. A verifier
(`verify.py` + a calibrated JEV classifier) checks every line you write
against the evidence afterwards and removes what the source does not say —
write so nothing needs removing.

## The contract

Read `evidence.md` (one line per item, `[ref] {…}`) and, when a line needs
more, the item in `evidence.json` or the text file its `path` names. Write
`draft.json` in the working directory AND submit the same object:

```json
{
  "title": "Daily brief — Mon 28 Sep",
  "sections": [
    {"id": "meetings", "heading": "Today's meetings", "empty": "No meetings today.",
     "items": [
       {"text": "10:00 Dev team meeting — 6 people, all internal; the agenda doc is attached.",
        "cites": ["event:google:work:abc123", "file:google:work:1xYz"],
        "why": "prep: read the attached agenda before 10:00",
        "rank": 1}
     ]}
  ],
  "appendix": []
}
```

Rules for every item:

1. **`cites` holds refs copied exactly** from the evidence (`mail:…`, `event:…`,
   `chat:…`, `pr:owner/repo#12`, `issue:…`, `file:…`, `task:…`, `person:…`,
   `notes:…`, `transcript:…`, `meetchat:…`, `comment:…`, `action:…`). A ref
   you did not see in the evidence gets the line removed.
2. **One fact per line.** "PR #12 waits on your review since Tuesday" — not a
   paragraph. The classifier checks the whole sentence; one unsupported clause
   sinks it.
3. **Numbers, names, dates only as the source states them.** Relative time
   ("2 days") only when you can compute it from a timestamp in the item and
   the window in `evidence.md`.
4. **`why` is the reason it is in the brief** (needs a reply because they asked
   a direct question; blocks a release; you promised it Friday). It is checked
   too, so ground it.
5. **`rank`** orders a section, 1 = act first. Rank by: someone is blocked on
   me > external person waiting > deadline today > internal ask > FYI.
6. **`quote`** (optional, with `quote_ref`): a verbatim span from the cited
   text. Use it for decisions and action items from meeting notes — the
   verifier checks the quote is really there.
7. **Never paste secrets** (tokens, passwords, OTPs, card numbers) even if an
   email contains one. Say "a one-time code arrived" at most.
8. **Automated mail** (`automated: true`, newsletters, receipts, GitHub
   notification mail) is not a "needs a reply" item. It can be FYI only when
   it is an alert or a bill that needs action.
9. **Replied already** (`replied_by_me: true`) is not "needs a reply".
10. **Empty is a valid answer.** An empty section prints its `empty` text; do
    not pad it.

## Sources you could not read

`evidence.md` lists every source with `ok`, `partial` or `unreadable` and its
`FIX`. You do not need to repeat them — the verifier prints them at the top
of the brief — but never write "no mail today" when the mail source was
unreadable: write nothing for that section and let its `empty` text say
"Mail could not be read".

## Per template

- **daily-brief** — sections: `meetings` (what each needs: attendees, orgs,
  docs, last thread), `decide` (mail/chat needing a decision or reply, ranked,
  with why), `waiting` (PRs/issues waiting on me), `commitments` (promises in
  my sent mail and open meeting actions, not yet seen done), `monitoring`
  (open alert issues), `fyi` (≤3 lines).
- **meeting-brief** — per meeting: `who` (attendee, org, role), `history`
  (last threads), `open` (open items / promises either way), `material`
  (docs, PRs), `agenda` (suggested agenda and questions — each must cite what
  prompted it), `owe` (what I owe them).
- **meeting-actions** — `decisions`, `actions` (text "Owner — action — due"),
  each with `quote` + `quote_ref` from the notes; plus `followup` in the
  appendix: the follow-up email body (a draft, never sent by you).
- **weekly-leadership-digest** — `shipped`, `broke`, `metrics`, `risks`,
  `decisions_needed`, `waiting_on_me`.
- **decision-log** — `decisions`: text "Decided: …", with `who`, `when`,
  `rationale` in `why`, `quote` + `quote_ref`; `conflicts` for a decision that
  contradicts an earlier logged one (cite both).
- **one-on-one-prep** — `work` (their recent PRs/issues/messages), `wins`,
  `blockers`, `they_owe`, `i_owe`, `ask` (questions to ask). Facts only — no
  judgement of the person ("struggling", "great attitude"); a classifier
  removes judgements.

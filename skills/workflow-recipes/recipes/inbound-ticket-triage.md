# Recipe: inbound-ticket-triage — route every ticket, draft grounded replies

New support tickets (helpdesk or a support inbox) are fetched with the
customer's account facts; a classifier assigns category and priority and routes
confident ones (internal, reversible); one agent drafts a reply grounded ONLY in
the account record and the KB; a classifier checks every factual claim in the
draft; a support agent sends or edits — nothing is ever sent automatically.

## Use when

- "Tickets sit unrouted for hours", "we write the same reply 30 times a day".

## Not when

- The ask is auto-reply without a person — refuse; Air Canada was held liable
  for its chatbot's advice (catalogue `support` §2).
- Issues in a code tracker → `issue-triage-labelling`.

## Evidence

Catalogue `support` §1 `ticket-triage-route` (ServiceNow: "AI Agents are
automating 37% of our customer support case workflow") and §2
`grounded-reply-draft` (Octopus: about a third of drafts need zero or minimal
edits; Fyxer "53% Of AI-generated drafts accepted as written").

## Questions — one at a time, offer the default

1. **Where do tickets live?** → `{{FETCH_TICKETS_CMD}}`, printing JSON lines
   `{"id","subject","body","customer_email"}` for new tickets *(default: Zendesk
   `curl -s -u "$ZENDESK_USER/token:$ZENDESK_TOKEN" https://<sub>.zendesk.com/api/v2/search.json?query=status:new`
   reshaped with `jq`; Gmail via `apl mail search 'label:support is:unread'`)*
   Toy check: a spreadsheet of old tickets is a test set, not the trigger — use it
   to calibrate, then point at the live queue.
2. **Where are the customer's facts?** → `{{ACCOUNT_LOOKUP_CMD}} <email>` *(default: a
   read-only SQL query for plan, orders, last payment, open incidents)*
3. **Categories and priorities, with what each means?** → `{{CATEGORIES_YAML}}`
   *(default: billing, bug, how-to, account-access, feature-request; P1–P4 by customer impact)*
4. **How do we write fields and drafts back?** → `{{APPLY_CMD}}` (sets category,
   priority, assignee, internal note with the draft) *(default: the helpdesk API; the
   draft goes in as an INTERNAL note or a Gmail draft, never a sent reply)*
5. **How often?** → `{{CRON}}` *(default: every 5 minutes)*
6. **Success number?** *(default: drafts sent with zero/minimal edits ≥ 33%;
   routing corrections by agents < 10% on a weekly sample)*

## Skeleton — `<name>/workflow.yaml`

Sidecars: `sidecars/route.py`, `questions.yaml`, `claims.yaml` (the second
questions file, below).

```yaml
name: {{NAME}}
description: >-
  Every new ticket is categorised, prioritised and routed (confident ones only),
  and gets a reply draft grounded in the customer's record with every claim
  checked. A support agent sends — nothing is sent automatically.
  Success: {{SUCCESS_NUMBER}}.

on:
  workflow_dispatch:
  schedule:
    - cron: "{{CRON}}"

steps:
  - id: fetch
    name: New tickets and each customer's record
    run: |
      {{FETCH_TICKETS_CMD}} > tickets.jsonl
      test -s tickets.jsonl || { echo "no new tickets"; exit 0; }
      python3 - <<'PY'
      import json, subprocess
      out = open("items.jsonl", "w")
      for line in open("tickets.jsonl"):
          t = json.loads(line)
          acct = subprocess.run("{{ACCOUNT_LOOKUP_CMD}} " + json.dumps(t["customer_email"]),
                                shell=True, capture_output=True, text=True).stdout[:3000]
          out.write(json.dumps({"id": str(t["id"]), "subject": t["subject"], "account": acct,
                                "state": f"Subject: {t['subject']}\n\n{t['body'][:3000]}"}) + "\n")
      PY
      exit 3
    timeout_sec: 180
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: classify
    name: Category, priority, confidence (JEV)
    needs: [fetch]
    when:
      - path: fetch.exitCode
        equals: 3
    run: |
      wfx judge -q ./questions.yaml --items items.jsonl > verdicts.jsonl || echo "judge failed"
      python3 route.py items.jsonl verdicts.jsonl route_confident
    timeout_sec: 300
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: draft
    name: Draft grounded replies
    needs: [classify]
    when:
      - path: fetch.exitCode
        equals: 3
    tools: [read, write]
    max_turns: 30
    system: >-
      You draft support replies using ONLY facts in the customer's record and the KB
      text provided. If a fact is not there, you say you will check — you never guess.
    prompt: |
      For each line of ./items.jsonl (ticket + account), draft a reply. Write
      claims.jsonl — one line per factual claim in a draft:
      {"id": "<ticket id>#<n>", "ticket": "<ticket id>", "state": "<the claim, then the
      account/KB text it relies on>"} — and drafts.json {ticket id: draft text}.
    output_schema:
      type: object
      required: [drafted]
      properties:
        drafted: { type: integer }

  - id: check
    name: Every claim supported? (JEV)
    needs: [draft]
    when:
      - path: fetch.exitCode
        equals: 3
    run: |
      wfx judge -q ./claims.yaml --items claims.jsonl > claim_verdicts.jsonl || echo "judge failed"
      python3 route.py claims.jsonl claim_verdicts.jsonl supported claim_
    timeout_sec: 300
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }

  - id: apply
    name: Route confident tickets; attach drafts as internal notes
    needs: [check]
    when:
      - path: fetch.exitCode
        equals: 3
    run: |
      # yes.jsonl = confidently routed; the rest go to the triage queue.
      # claim_uncertain/claim_no.jsonl = claims highlighted as UNVERIFIED in the note.
      {{APPLY_CMD}}
    timeout_sec: 300
    output_schema:
      type: object
      properties:
        ok:       { type: boolean }
        exitCode: { type: integer }
        stdout:   { type: string }
        stderr:   { type: string }
```

## `questions.yaml`

```yaml
category:
  type: choice
  instructions: Which category is this ticket?
  options:
    {{CATEGORIES_YAML}}
priority:
  type: choice
  instructions: How urgent is it for the customer?
  options:
    P1: the customer cannot use the product or is losing money now
    P2: a core feature is broken with no workaround
    P3: broken with a workaround, or a billing question
    P4: a question or a request
route_confident:
  type: noul
  instructions: Is the ticket clear enough to route without a person reading it first?
  "true": one clear problem matching one category
  "false": several issues, angry/legal/cancellation language, or unclear
```

## `claims.yaml`

```yaml
supported:
  type: noul
  instructions: Is this claim fully supported by the account record or KB text that follows it?
  "true": the record/KB states it explicitly
  "false": not stated, contradicted, or inferred
```

## Gates

No new tickets → nothing runs. Routing applies in the yes band only; uncertain →
triage queue. Unsupported claims are highlighted in the note. **Sending is never
automated** — the outward act is the support agent's click in their own tool, so
this workflow has no send step to gate.

## Success number

Drafts sent with zero/minimal edits (≥ 33%, Octopus baseline); routing
corrections < 10% on a weekly sample of 20.

## Bar check

trigger: 5-min schedule · record: helpdesk + account DB · output: fields +
internal draft · verify: JEV per claim · gate: a person sends every reply · no
silent failure: missing verdict → triage queue / unverified · number: above.

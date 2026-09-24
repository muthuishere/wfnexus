# What to ask, and what each answer changes

Ask what you cannot infer. Four or five questions, not twenty — and state your
assumption for everything you did not ask. A person who is interrogated stops
answering; a person handed a draft full of silent guesses stops trusting.

Skip any question the request already answers. Never ask for something the
catalogue can tell you.

## The five that change the shape

**1. What must be true for this to have worked?**
→ the last step's `output_schema`.
If the answer is "it ran to the end", there is no workflow here — it is a shell
script. Say so rather than wrapping a command in five agents.

**2. What does it act on, and where does that come from?**
→ `input_schema`, and whether you need `repo_url` (cloned per run) or
`repo_path` (a working copy).
"A bug report" and "a repository at a commit" are different workflows.

**3. What is irreversible, and who approves it?**
→ one final step with `requires_approval: true`, guardrails on every step
before it denying the same thing.
If nothing is irreversible, say so — no approval gate, and the workflow is
cheaper and more useful for it.

**4. What should happen when a step says no?**
→ `gates`. Ask for the actual failure cases: cannot reproduce, tests still red,
reviewer rejects. Each is `needs_input`, `fail` or `skip_to` — and "just carry
on" is a valid answer worth writing down.

**5. Where does it run, and what does it need to get there?**
→ `runs-on:` and `env:`.
Only ask when the request implies a machine that is not this one (Windows, a
GPU box, a licensed toolchain) or a credential. A token is named
(`${GITHUB_PAT}`), never written into the file.

## Worth asking when it is not obvious

- **How often, and started by what?** → `on:` — a person, a cron, or another
  system POSTing. Default to `workflow_dispatch` only, and say that you did.
- **How long is too long?** → budgets and `timeout_sec`. If they do not know,
  propose a ceiling and say it is a guess.
- **Is there existing expertise for this?** → `skills:`. Check the catalogue
  and name what you found rather than asking them to remember the names.

## Do not ask

- Anything `wf_catalog` answers — which skills, tools, providers exist.
- Which model. Default unless they raise it.
- Schema field types. Propose them; they will correct you if it matters.
- Permission to use a cheap step instead of an agent. Just do it and say why.

## How to close

Before drafting, say back in three lines what you are about to build: the
shape, the gates, and what you assumed. One round of that catches more than
another five questions. Then draft, dry run, and hand over with the assumptions
still attached.

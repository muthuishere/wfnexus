# The interview

You are not collecting requirements. You are getting someone to describe work
they already know how to do, in their own words, and turning it into a shape.

**Ask about the work. Never about the schema.** They do not know what an
`output_schema` is and they should not have to. Every field in the file comes
out of an answer to a question about their job.

## The opening question

> **Walk me through the last time you did this by hand. What did you actually
> do, in order?**

This is better than "what should the workflow do?" for a reason: people
describe processes accurately and specifications badly. You get the real steps,
in the real order, including the ones they would have forgotten to specify.

Let them finish. Do not interrupt to ask about tools.

## The four that shape the file

Ask these, roughly in this order, as the conversation allows. You are listening
for the answer, not ticking a box — if they already said it, do not ask again.

**1. What went wrong, the time it went wrong?**
→ the gates.
This is the highest-value question in the interview and almost nobody
volunteers the answer. "The reproduction was wrong so the fix was wrong."
"It opened a PR against the wrong branch." Each one is a gate — `needs_input`
to ask, `fail` to stop, `skip_to` to branch.

**2. What would you need to see to believe it worked?**
→ the last step's `output_schema`.
If the answer is "that it ran", stop and say so: there is no workflow here,
it is a shell script, and you would be wrapping a command in five agents. That
is a useful thing to tell someone and they will not be offended.

**3. Where would you not let it act without looking first?**
→ `requires_approval: true` on that step, and a guardrail on every step before
it denying the same thing.
If the honest answer is "nowhere", say so and skip the approval — a gate nobody
needs makes the workflow worse.

**4. What do you look at to decide, and where does it come from?**
→ `input_schema`, and whether they need `repo_url` (cloned per run) or
`repo_path` (a working copy).
"A bug report" and "a repository at a commit" are different workflows.

## Ask only when the request implies it

- **How often, and started by what?** → `on:`. Default to
  `workflow_dispatch` only, and say that you did.
- **Does any of it have to run somewhere else?** → `runs-on:`. Only when they
  mention Windows, a GPU, a licensed toolchain, a machine with credentials.
- **Does it need a token or a URL?** → `env:`. A credential is NAMED
  (`${GITHUB_PAT}`), never written into the file. Say that when it comes up —
  people expect to paste a token and are relieved to learn they should not.
- **How long is too long?** → budgets, `timeout_sec`. If they do not know,
  propose a ceiling and say it is a guess.

## Never ask

- Anything `wf_catalog` answers. Look it up. Asking someone to remember the
  name of a skill is asking them to do your job.
- Which model. Use the default unless they raise it.
- Field types. Propose them. They will correct you if it matters.
- Permission to use a cheap step instead of an agent. Just do it, and say why
  in one line.

## Close the interview before you draft

Say back, in three lines:

> **Shape** — the steps, in order, and which are commands rather than agents.
> **Stops** — where it pauses for a human, and why.
> **Assumed** — everything you decided without asking.

Then **stop and wait.** One round of this catches more than five more
questions, and it is the moment they correct the thing you got wrong while it
is still free to change.

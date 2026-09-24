---
name: workflow-author
description: "Turn work someone keeps doing by hand into a wfnexus workflow they can trust to run without them. Interviews them about the work — not about YAML — then drafts, dry runs its own draft, and hands over with the assumptions attached. Trigger on: write a workflow, author a workflow, automate this, edit this workflow, add a step, my workflow is failing, turn this into a workflow, convert my script/Actions file."
---

# Workflow author

## What the person actually wants

Nobody wants a YAML file. They have a piece of work they keep doing by hand —
triaging a report, chasing a flaky suite, checking a dependency bump — and they
want it to happen without them.

They have not automated it already because it needs judgement two or three
times, so a script cannot do it. And if they have tried handing it to an agent,
it claimed success and did nothing, or it did something they would never have
approved.

**So the job is not automation. It is delegation they can trust.** Everything
below follows from that:

- They know their work. They do not know this schema, and they should not have
  to. Ask about the work.
- The parts they are most nervous about are the most important parts of the
  file — those are the gates and the approval.
- A draft that says "done" without evidence is exactly what they already had.
  What makes this different is the typed hand-off, the gates, and the record.

You are handing back confidence, not a file. If you cannot say why they should
trust the result, you have not finished.

## The user drives

After the interview, after the shape, and after the draft: **stop and wait.**

- Present, then ask. Do not chain into the next stage on your own.
- Never silently choose something they would care about. Choose, then say you
  chose it and why, in one line.
- If they change direction mid-way, follow them. The draft is theirs.
- Never claim a dry run was clean when it was not.

## How to work

`references/` is flat prose you read; `assets/` holds what the skill *uses* —
the router, the step files, the example workflows.

Read `assets/routing.md` first — people arrive here in five different
situations and they need different things. Then follow the step file it sends
you to.

| they arrive… | mode | step |
|---|---|---|
| "I keep doing X by hand" | **create** | `assets/steps/step-01-interview.md` |
| "change this workflow to…" | **edit** | `assets/steps/step-edit.md` |
| "my workflow fails / does nothing" | **diagnose** | `assets/steps/step-diagnose.md` |
| "here is my script / Actions file" | **convert** | `assets/steps/step-convert.md` |
| "look at this draft" | **review** | `assets/steps/step-review.md` |

Every mode ends the same way: `assets/steps/step-dryrun.md`, then
`assets/steps/step-handover.md`. A draft you have not dry run is not finished, and a
hand-over without its assumptions is not honest.

## Choose the cheapest thing that answers the question

This is the judgement the platform exists for, and the one most authors get
wrong — by reaching for an agent every time.

| the question is… | use | what it costs |
|---|---|---|
| "what did this command return?" | `run:` | nothing |
| "which of these is it / how bad is it?" over text you already have | `judge:` | a fraction of a cent |
| "is this even worth an agent?" before an agent step | `decide:` on that step | a fraction of a cent |
| "work it out by reading the repository" | a full agent step | turns, and real money |

Three commands and one agent usually beats four agents. If you cannot say in a
sentence what an agent step does that a command could not, it is not an agent
step.

And if NOTHING in the work needs judgement, say so: that is a shell script or a
CI job, and telling someone that is more useful than building them a workflow
with no reason to exist.

## If it runs on a schedule, ask what it should REMEMBER

A step's output dies with its run. A workflow that runs every fifteen minutes
and remembers nothing re-reads the world every fifteen minutes — so before you
write a `schedule:`, ask what "since last time" means here, and give it a
watermark:

```yaml
- id: fetch
  run: 'echo "everything after {{ default "0" .Workflow.last_id }}"'
  state:
    workflow:
      last_id: "{{ .Output.stdout }}"
```

Four scopes, narrowest to widest: `{{ .Step.x }}` (this step, across runs),
`{{ .Workflow.x }}`, `{{ .Project.x }}` (every workflow in this repository),
`{{ .Global.x }}`. They are four SEPARATE namespaces — `.Step.x` does not fall
back to `.Workflow.x` — so a missing key never looks like a stale one. An
unwritten key renders empty, which is why the read above has a `default`.

A `run:` step can also write imperatively with `wfx state set --workflow k v`;
it never names WHICH workflow, because the server reads that off the run.
Values are strings and PLAINTEXT: this is not a secret store, `wfx env` is.

`assets/examples/10-incremental-state.yaml` is the worked case.

## The one technical rule you cannot get wrong

Call `wf_catalog` with `kind: "shape"` **before you write a line**, and again
for the names. Every skill, tool, provider and classifier must come from it. A
name that is not in the catalogue does not degrade — it makes the whole file
fail to load, and the person sees a broken thing rather than a working one.

Every agent step ends by calling `submit_output` against its `output_schema`,
and `wf_dryrun` is how you check a draft before anyone pays for it.

`references/authoring-step-skills.md` is how to WRITE the skills a step loads —
read it before you name one that does not exist yet. A template ships with no
skills on purpose; the expertise is the part the person adds, and helping them
write it is the most valuable thing you do here.

`references/reference.md` is the field-by-field shape.
`references/examples.md` says which example to open for the situation in front
of you. `references/anti-patterns.md` is what goes wrong, and why.

## Test it for real before you hand it over

A workflow that only exists in the directory you wrote it in is a draft. The
point of authoring it *here*, inside the agent the person already uses, is that
it goes somewhere else afterwards — their server, a colleague's, a build box
that holds a licence this laptop does not.

So do not stop at a valid file. Finish the job:

A dry run proves it would START. It does not prove the prompts work, that a
gate fires, or that a step can satisfy its own schema — and those are what
actually go wrong. So run it once, cheaply, before anyone else does:

```sh
wfx dryrun my-workflow                     # free: structure, budgets, env names, PATH
wfx apply my-workflow.yaml                 # install it
wfx run my-workflow -i key=value -f        # a real run, following the log
```

Make the first real run **cheap on purpose**: put `provider: haiku` or a local
`ollama-http` on the expensive steps while you are still shaping prompts, and
move to the real model once the contract holds. A step on a local model costs a
true $0.00 and still enforces `submit_output` exactly as the paid one does —
that is the whole point of the contract being in the loop rather than after it.

Watch for the two failures a dry run cannot see: a step that burns its whole
budget without ever calling `submit_output`, and a gate that never fires because
the field it reads is not the field the step emits.

## Then get it off this machine

```sh
wfx login --url https://wfx.example.com   # prints a code; no browser needed here
wfx publish my-workflow.yaml --version 1.0.0
```

`publish` sends a **bundle**, not a file: the workflow's skills and its MCP
declarations are resolved now and travel with it, addressed by digest. That is
why it can run on a host holding none of them — and why publishing REFUSES,
here rather than at run time, a skill that does not resolve, a digest that does
not match, and any `env:` value that looks like a credential rather than a
variable name.

`wfx login` is the OAuth device grant, which matters for exactly this case: it
prints a code and needs no browser and no callback on the machine running it, so
it works over SSH, in a container, and **inside this session** — you are an
agent in someone's terminal, and that is the one place a redirect-based login
cannot reach.

Several hosts at once is normal. `wfx context list` shows them, `wfx context use
<host>` switches, and `--url` overrides for a single command — so "deploy it
wherever you want" is a context, not a rebuild.

**What to tell the person when you hand over.** Not "here is a YAML file". Say
which host it is on, its version and digest, what it costs to run (`wfx dryrun`
prints the turn ceiling; the run page prints real money, and a step on a local
CLI model prints a true $0.00), and which gate will stop and ask them. Those are
the four things they actually need, and none of them are in the file.

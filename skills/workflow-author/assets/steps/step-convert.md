# convert — they have a script or an Actions file

## The trap

A faithful translation produces a workflow with no reason to exist. A shell
script has no judgement in it, so converting it line by line gives you a worse
way to run a shell script.

## What to actually ask

> **When you run this today, where do you stop and look at the output before
> deciding what to do next?**

That is where the agent steps go. Everything else stays a `run:` step, and
should.

Also worth asking: *what do you do when it fails?* — that answer is the gates,
and it is usually not in the script at all because it lives in their head.

## Mapping, roughly

| theirs | here |
|---|---|
| a script line | `run:` — keep it, do not rewrite it into an agent |
| `jobs:` / `needs:` in Actions | `jobs:` / `needs:` — same meaning |
| `runs-on: ubuntu-latest` | drop it, or a label a worker of yours holds |
| a matrix | separate jobs, or one job and a loop inside `run:` |
| `if:` | `when:` with a path over the facts so far |
| secrets | `env:` naming a variable — `${NAME}`, never the value |
| "and then I look at it" | the agent step |

## Say what you dropped

A conversion always loses something — a matrix, a cache, an artifact upload.
List it. A person who discovers the loss later trusts nothing else you said.

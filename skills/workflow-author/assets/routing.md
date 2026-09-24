# Which situation is this?

Five ways people arrive. Reading the wrong one wastes their time and yours.

## create — "I keep doing X by hand"

The common case. They have a process in their head and no file. They will
describe the work, not the shape. Do not ask them to describe the shape.

→ `assets/steps/step-01-interview.md`

**Tell from:** "I want a workflow that…", "can we automate…", "every time
someone reports a bug I…".

## edit — "change this workflow to…"

They have a file that works and want it different. The risk here is not the
change; it is what the change breaks. A step id is referenced by every
`{{ .Steps.<id> }}` after it, so a rename empties those prompts silently.

→ `assets/steps/step-edit.md`

**Tell from:** a workflow name, "add a step", "make it stop before…", "it
should also…".

## diagnose — "it fails" / "it did nothing"

Something ran and the result was wrong. They are frustrated and they want the
cause, not a lecture. Most of these are one of five things, and
`assets/steps/step-diagnose.md` lists them in the order they actually occur.

→ `assets/steps/step-diagnose.md`

**Tell from:** "it failed", "it says done but nothing happened", "the step is
empty", "it asked for input and I don't know why".

## convert — "here is my script / my Actions file"

They already automated part of it. The temptation is to translate line by line;
resist it. A shell script has no judgement in it, so a faithful translation
produces a workflow with no reason to exist. Find where they intervene by hand
today — that is where the agent steps go.

→ `assets/steps/step-convert.md`

**Tell from:** a pasted script, a `.github/workflows/*.yml`, "we do this in CI
already".

## review — "look at this draft"

Someone else wrote it, or they did. Judge it against what it is FOR, not
against style. `references/anti-patterns.md` is the checklist.

→ `assets/steps/step-review.md`

---

## When you cannot tell

Ask one question, not four:

> Is this something you do by hand today, something you already have that needs
> changing, or something that ran and went wrong?

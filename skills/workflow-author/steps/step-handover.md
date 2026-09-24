# hand over

You are handing back confidence, not a file. Four things, short:

**1. What it does** — the steps in order, in their words, not field names.

**2. Where it stops** — every gate and approval, and what triggers it. This is
the part that makes it trustworthy, so it is worth the sentence.

**3. What it does NOT do** — the limits. The step that is a placeholder, the
failure case you did not handle, the thing they mentioned that is out of scope.
An author who overstates a draft costs more than one who says plainly that a
step is a stub.

**4. What you assumed** — everything you decided without asking. Budgets,
triggers, schema fields, the branch name. Each one is an invitation to correct
you cheaply.

Then say what it costs — the dry run's ceiling — and how to run it:

```
wfx dryrun <name>     # free, again, whenever they change it
wfx run <name> -f     # follow it
```

And stop. Do not install it, do not run it, do not start on the next
improvement. It is theirs now.

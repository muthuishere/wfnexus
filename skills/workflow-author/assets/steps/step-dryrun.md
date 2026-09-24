# dry run — the check that costs nothing

`wf_dryrun` on the draft. It reports what would actually happen on this
machine: the derived order, the prompts as rendered, the turn ceiling, the
variables each step reads, and anything unresolvable.

## Read it properly

- **Fatal problems** — fix them. There is no judgement call here.
- **Warnings** — a label nobody serves, a variable not set locally. Decide and
  SAY which you decided to accept and why.
- **The rendered prompts** — read them as the agent will. This is where an
  empty `{{ .Steps.a.b }}` shows up as a sentence with a hole in it.
- **The ceiling** — turns across all agent steps. If it is higher than the
  person would be comfortable paying, that is a finding, not a detail.

## Then do it again

Fix, dry run, repeat until clean. Then dry run **the exact definition you are
about to hand over** — not the one you fixed two edits ago. Submitting an
unverified draft after verifying a different one is the most common way to get
this wrong.

## Never claim it was clean when it was not

If it still has problems and the person wants it anyway, that is their call to
make with the facts in front of them. Say what is outstanding.

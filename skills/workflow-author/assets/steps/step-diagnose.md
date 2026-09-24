# diagnose — it ran and the result was wrong

They are frustrated. Find the cause, say it in one line, then fix it. No
lecture.

## Look at the run first

The run record has what happened: which step, what it submitted, what the tool
calls were. `wfx show <run-id>` and `wfx logs <run-id>`. Do not theorise from
the YAML when the evidence exists.

## The five things it actually is, in order

**1. A prompt referenced a field that does not exist.**
The commonest by a distance. `{{ .Steps.a.b }}` where `b` is not in `a`'s
output_schema renders EMPTY — the agent was told less than the author thinks,
and behaved accordingly.
*Symptom:* an agent that ignored context it was "given".
*Check:* every `.Steps.<id>.<field>` against that step's schema.

**2. The step ran out of turns.**
It did the work and never called `submit_output`, so nothing was recorded.
*Symptom:* status failed, turns equal to the budget, a transcript full of
useful work.
*Fix:* raise the budget, or split the step — but first ask whether the step is
doing two jobs.

**3. A name is not in the catalogue.**
A skill, tool, provider or classifier that does not exist makes the whole file
fail to load. The workflow does not appear at all rather than failing at that
step.
*Symptom:* "workflow not found", or it is missing from `wfx workflows`.

**4. A gate fired and it looks like a failure.**
`needs_input` parks the run deliberately. It is doing its job.
*Symptom:* status `needs_input` and a message nobody read.

**5. It ran somewhere without what it needed.**
A `runs-on:` label nobody serves waits. A `${VAR}` nobody set fails naming the
variable. A `cli` provider whose binary is not on THAT machine's PATH fails at
the first turn with an exec error wrapped in an HTTP error.
*Check:* `wfx dryrun <name>` says all three before anything is spent.

## Then

Fix one thing, dry run, and say plainly which of the five it was. If it was
none of them, say that too rather than guessing.

# review — judge a draft

Judge it against what it is FOR, not against style. Ask what the workflow is
supposed to achieve before reading a line; a draft can be internally perfect
and still be the wrong thing.

## The pass

Work `references/anti-patterns.md` top to bottom. The four that matter most:

1. **Is every agent step earning its turns?** What does it do that a command
   could not?
2. **Is every output schema a contract?** Could you write a gate against it?
3. **Does every `.Steps.<id>.<field>` exist?** Silent empties are the most
   common real defect.
4. **Is anything irreversible ungated?**

## Then dry run it

Do not review from reading alone. `wf_dryrun` reports the derived order, the
rendered prompts and the ceiling — three things you cannot see by eye.

## Say it plainly

Severity honestly: what breaks, what will bite later, what is taste. Do not
inflate to look thorough, and say when it is fine — a review that never
approves anything stops being read.

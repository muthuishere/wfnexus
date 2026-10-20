-- WHY a step was skipped, in the engine's own words: which `when:` did not
-- hold and what the value was, a branch the planner did not take, a gate's
-- skip_to. The UI used to re-derive this from the definition and got it wrong
-- wherever its rules and the engine's differed (input guards, planned mode,
-- null vs absent); now the one thing that decided says why.
ALTER TABLE step_runs ADD COLUMN skip_reason text NOT NULL DEFAULT '';

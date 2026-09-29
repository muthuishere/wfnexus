-- WHO started a run, as a fact on the run (ADR 0021).
--
-- Segregation of duties ("the principal who triggered the run may not approve
-- it") needs the trigger's identity at approval time, and a log line is not
-- something a policy can be checked against. An authenticated subject's name,
-- a claimed actor on the loopback, or `trigger:<kind>` for a schedule or an
-- inbound event. Empty for runs from before this column: they have no record,
-- and inventing one would be worse than admitting it.
ALTER TABLE workflow_runs ADD COLUMN triggered_by text NOT NULL DEFAULT '';

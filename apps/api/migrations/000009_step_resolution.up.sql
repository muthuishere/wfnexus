-- WHO resolved a pause, and WHAT they decided (ADR 0021).
--
-- An approval used to leave `status='approved'` and nothing else: no actor, no
-- time, and a decline indistinguishable from a timeout. These four columns are
-- the audit fact on the step — queryable, not a log line.
--
-- resolution mirrors toolnexus's Answer: 'approved' | 'answered' for Ok, and
-- 'declined' | 'cancelled' | 'expired' for the three not-Ok Reasons
-- (types.go:76-81), plus 'rejected' for the approval gate's own no.
ALTER TABLE step_runs ADD COLUMN resolved_by       text NOT NULL DEFAULT '';
ALTER TABLE step_runs ADD COLUMN resolved_at       timestamptz;
ALTER TABLE step_runs ADD COLUMN resolution        text NOT NULL DEFAULT '';
ALTER TABLE step_runs ADD COLUMN resolution_reason text NOT NULL DEFAULT '';

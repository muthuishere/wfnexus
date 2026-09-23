-- A machine is its NAME. Re-running the join command on a box that already
-- joined should update it, not add a second row — otherwise every reinstall,
-- every service restart that re-joins, leaves a ghost, and the Workers page
-- fills with offline machines that do not exist.
--
-- Observed for real: three rows for one laptop after an afternoon of testing.

-- Existing duplicates keep the most recently seen row; the rest are dropped,
-- and their jobs come with them (worker_id is ON DELETE SET NULL, so a job
-- attributed to a retired row is simply unattributed, never lost).
DELETE FROM workers a
USING workers b
WHERE a.name = b.name
  AND (a.last_seen, a.id) < (b.last_seen, b.id);

CREATE UNIQUE INDEX IF NOT EXISTS workers_name_key ON workers (name);

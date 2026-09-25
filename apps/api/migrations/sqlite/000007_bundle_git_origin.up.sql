-- The SQLite half of the Postgres 000013 migration: where an indexed bundle
-- came from, when it came from git.
--
-- NULL, not '': a bundle uploaded straight to this host has neither a remote
-- nor a commit, and absent must stay distinguishable from empty.
--
-- SQLite's ALTER TABLE cannot add a table CHECK to an existing table, so the
-- both-or-neither rule Postgres states as a constraint is enforced by the store
-- before the insert (internal/store/bundles.go) on both dialects. Stated here
-- so a reader of this file is not misled into thinking the dialects differ in
-- what they accept.
ALTER TABLE published_bundles ADD COLUMN git_remote text;
ALTER TABLE published_bundles ADD COLUMN git_commit text;

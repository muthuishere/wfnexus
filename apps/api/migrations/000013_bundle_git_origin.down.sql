ALTER TABLE published_bundles DROP CONSTRAINT IF EXISTS published_bundles_git_origin_whole;
ALTER TABLE published_bundles DROP COLUMN git_commit;
ALTER TABLE published_bundles DROP COLUMN git_remote;

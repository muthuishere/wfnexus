-- Where an indexed bundle actually CAME FROM, when it came from git.
--
-- The change "a git remote is the registry" (design §4) makes this table a
-- local index of what a host has seen, not the registry itself. Two views then
-- exist — git says where a bundle lives and who approved it, this table says
-- what is present here — and they only reconcile if a row can name the remote
-- and the commit it was read from.
--
-- The commit and not the tag, because a tag moves and a commit cannot: that is
-- the pin from design §3, recorded here for the same reason it is recorded on a
-- run.
--
-- NULL, not '': a bundle uploaded straight to this host has no remote and no
-- commit, and an empty string pretending to be a value is how "absent" becomes
-- indistinguishable from "the empty remote". Nothing backfills these; every
-- existing row predates the git sink and legitimately has neither.
ALTER TABLE published_bundles ADD COLUMN git_remote text;
ALTER TABLE published_bundles ADD COLUMN git_commit text;

-- Both or neither. A remote with no commit is an unreconcilable half-record:
-- it names a place without naming what was read from it.
ALTER TABLE published_bundles ADD CONSTRAINT published_bundles_git_origin_whole
    CHECK ((git_remote IS NULL) = (git_commit IS NULL));

-- published_bundles.published_by is DEMOTED here, per design §5, without losing
-- a byte of it. Git's commit author is the record of who published: it has an
-- author, a committer, a date, a message and a parent, and a signed tag is the
-- tamper-evidence ADR 0018 explicitly could not claim. This column becomes a
-- CACHED COPY of that for a row that came from git, and stays the only answer
-- available for a row uploaded directly to this host. It is not dropped,
-- because deleting an audit trail to make a point about provenance is worse
-- than an audit trail that says where it came from.
COMMENT ON COLUMN published_bundles.published_by IS
    'Who this host was told published it. Not authoritative: for a bundle with a git_commit, the commit author is the record (design §5).';
COMMENT ON COLUMN published_bundles.git_remote IS
    'The git remote this bundle was read from. NULL for a direct upload.';
COMMENT ON COLUMN published_bundles.git_commit IS
    'The resolved commit — the pin. A tag moves; this does not. NULL for a direct upload.';

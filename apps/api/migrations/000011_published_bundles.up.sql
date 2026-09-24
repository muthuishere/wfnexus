-- The published-bundle INDEX (ADR 0018, design §5).
--
-- The bytes live in the blob store under bundles/<sha256>/…; this table holds
-- only what has to be queried: identity, provenance and the manifest.
--
-- `unique (project, kind, name, version)` is the immutability rule, spelled
-- where it cannot be forgotten: publishing name@version twice is a REFUSAL, not
-- an overwrite — npm's rule, for npm's reason. A tag may move; a version may not.
create table if not exists published_bundles (
    id            uuid primary key,
    kind          text not null,        -- skill|mcp|workflow|template|provider
    project       text not null default '',
    name          text not null,
    version       text not null,
    digest        text not null,        -- sha256:… of the canonical manifest
    manifest      jsonb not null,
    -- Provenance is a RECORDED PUBLISHER, not a signature: it says who this
    -- host was told published it, in git's sense of an author. Signing is
    -- deferred, so nothing here is tamper-evidence.
    published_by   uuid references users(id) on delete set null,
    published_by_name text not null default '',
    published_at   timestamptz not null default now(),
    unique (project, kind, name, version)
);
create index if not exists published_bundles_digest_idx on published_bundles (digest);

-- A tag is a MOVING pointer at a version. Separate row, so moving it cannot
-- touch the version it used to point at.
create table if not exists bundle_tags (
    project    text not null default '',
    kind       text not null,
    name       text not null,
    tag        text not null,
    version    text not null,
    updated_at timestamptz not null default now(),
    primary key (project, kind, name, tag)
);

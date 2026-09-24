-- The SQLite half of the Postgres 000011 published-bundle migration.
-- Same columns, this dialect: text for uuid, timestamp for timestamptz, text
-- for jsonb.
CREATE TABLE published_bundles (
    id            text PRIMARY KEY,
    kind          text NOT NULL,
    project       text NOT NULL DEFAULT '',
    name          text NOT NULL,
    version       text NOT NULL,
    digest        text NOT NULL,
    manifest      text NOT NULL,
    published_by  text REFERENCES users(id) ON DELETE SET NULL,
    published_by_name text NOT NULL DEFAULT '',
    published_at  timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
CREATE UNIQUE INDEX published_bundles_nv_key ON published_bundles (project, kind, name, version);
CREATE INDEX published_bundles_digest_idx ON published_bundles (digest);

CREATE TABLE bundle_tags (
    project    text NOT NULL DEFAULT '',
    kind       text NOT NULL,
    name       text NOT NULL,
    tag        text NOT NULL,
    version    text NOT NULL,
    updated_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    PRIMARY KEY (project, kind, name, tag)
);

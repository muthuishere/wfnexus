-- See migrations/000008_state_vars.up.sql for what this table is, and why the
-- four scopes are four namespaces rather than a cascade.
CREATE TABLE IF NOT EXISTS state_vars (
    scope      text NOT NULL,
    scope_name text NOT NULL DEFAULT '',
    key        text NOT NULL,
    value      text NOT NULL,
    updated_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    PRIMARY KEY (scope, scope_name, key)
);

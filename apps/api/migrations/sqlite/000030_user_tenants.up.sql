-- USER TENANTS (see the Postgres 000036 for the rules).
CREATE TABLE user_tenants (
    user_id    text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    tenant_id  text NOT NULL REFERENCES tenants(id) ON DELETE RESTRICT,
    role       text NOT NULL CHECK (role IN ('org-admin', 'member')),
    created_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now'))
);
CREATE INDEX user_tenants_tenant_idx ON user_tenants (tenant_id);

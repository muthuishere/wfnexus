-- See the Postgres 000033.
CREATE TABLE tenant_issuer_links (
    issuer          text NOT NULL,
    external_tenant text NOT NULL,
    tenant_id       text NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    created_at      timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    PRIMARY KEY (issuer, external_tenant)
);

CREATE TABLE issuer_revocations (
    issuer          text NOT NULL,
    external_tenant text NOT NULL,
    subject         text NOT NULL,
    revoked_before  timestamp NOT NULL,
    PRIMARY KEY (issuer, external_tenant, subject)
);

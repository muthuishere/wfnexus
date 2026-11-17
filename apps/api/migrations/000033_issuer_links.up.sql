-- Accounts signed in at an EXTERNAL token issuer (internal/issuer). The
-- issuer's tenant id (the token's tid) maps to exactly one local tenant; a
-- tid with no row is refused. The mapping is set by an operator, never taken
-- from a token.
create table if not exists tenant_issuer_links (
    issuer          text not null,
    external_tenant text not null,
    tenant_id       text not null references tenants(id) on delete cascade,
    created_at      timestamptz not null default now(),
    primary key (issuer, external_tenant)
);

-- A token whose auth_time is before revoked_before is refused (revocation
-- compares the sign-in time, never iat).
create table if not exists issuer_revocations (
    issuer          text not null,
    external_tenant text not null,
    subject         text not null,
    revoked_before  timestamptz not null,
    primary key (issuer, external_tenant, subject)
);

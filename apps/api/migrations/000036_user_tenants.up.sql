-- USER TENANTS: the one organization (tenant) a person signed in through an
-- OIDC provider with orgClaim belongs to, and their role in it (ADR 0035).
-- A person with no row is unscoped, as every user was before this table.
--
-- One row per user, written at sign-in and never moved: a later sign-in
-- naming another organization is refused, not re-bound. role is "org-admin"
-- or "member", refreshed from the operator's roleMap at every sign-in.
create table if not exists user_tenants (
    user_id    uuid primary key references users(id) on delete cascade,
    tenant_id  text not null references tenants(id) on delete restrict,
    role       text not null check (role in ('org-admin', 'member')),
    created_at timestamptz not null default now()
);
create index if not exists user_tenants_tenant_idx on user_tenants (tenant_id);

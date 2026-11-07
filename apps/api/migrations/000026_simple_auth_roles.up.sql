-- FOUR ROLES (ADR 0028). superadmin is users.role; the other three are grants
-- at a scope:
--   project_admin   on a project
--   workflow_admin  on a workflow
--   workflow_runner on a workflow
create table if not exists members (
    scope      text not null check (scope in ('project', 'workflow')),
    name       text not null,
    user_id    uuid not null references users(id) on delete cascade,
    role       text not null,
    created_at timestamptz not null default now(),
    primary key (scope, name, user_id),
    check ((scope = 'project'  and role = 'project_admin') or
           (scope = 'workflow' and role in ('workflow_admin', 'workflow_runner')))
);
create index if not exists members_by_user on members (user_id);

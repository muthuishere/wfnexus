-- ONE LOGIN, ONE JWT, FOUR ROLES (ADR 0028).
--
-- The only GLOBAL role left is superadmin (users.role = 'superadmin'; '' is
-- none). The other three are grants at a scope, in `members`:
--   project_admin   on a project
--   workflow_admin  on a workflow
--   workflow_runner on a workflow
--
-- Existing users: admin -> superadmin. publisher and viewer are MARKED here
-- ('legacy:publisher', 'legacy:viewer') and converted at the next boot, when
-- the server knows which workflows exist (workflows are files, not rows):
-- publisher -> workflow_admin on every workflow of its project (all
-- workflows when unscoped), viewer -> workflow_runner on the same set. Any
-- other custom role -> none.
update users set role = case role
    when 'admin'     then 'superadmin'
    when 'publisher' then 'legacy:publisher'
    when 'viewer'    then 'legacy:viewer'
    else '' end;

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

-- ONE LOGIN, ONE JWT, FOUR ROLES (ADR 0028).
--
-- Global roles are renamed: admin -> superadmin, publisher -> editor,
-- viewer -> user. A renamed row keeps its permissions; `user` also gains
-- runs:write (it may run what it can see), and `editor` gains runs:all (it
-- sees every run, where a `user` sees only its own).
update roles set name = 'superadmin' where name = 'admin'
   and not exists (select 1 from roles r where r.name = 'superadmin');
update roles set name = 'editor' where name = 'publisher'
   and not exists (select 1 from roles r where r.name = 'editor');
update roles set name = 'user' where name = 'viewer'
   and not exists (select 1 from roles r where r.name = 'user');
update roles set permissions = array_append(permissions, 'runs:write')
 where name = 'user' and not ('runs:write' = any(permissions));
update roles set permissions = array_append(permissions, 'runs:all')
 where name = 'editor' and not ('runs:all' = any(permissions));

update users set role = 'superadmin' where role = 'admin';
update users set role = 'editor'     where role = 'publisher';
update users set role = 'user'       where role = 'viewer';

-- Per-workflow roles: owner (edit, run, approve, expose, choose editors) and
-- editor (edit, run, approve). A grant ADDS to the user's global role for
-- this one workflow.
create table if not exists workflow_members (
    workflow   text not null,
    user_id    uuid not null references users(id) on delete cascade,
    role       text not null check (role in ('owner', 'editor')),
    created_at timestamptz not null default now(),
    primary key (workflow, user_id)
);
create index if not exists workflow_members_by_user on workflow_members (user_id);

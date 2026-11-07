drop table if exists workflow_members;
update users set role = 'admin'     where role = 'superadmin';
update users set role = 'publisher' where role = 'editor';
update users set role = 'viewer'    where role = 'user';
update roles set permissions = array_remove(permissions, 'runs:all') where name = 'editor';
update roles set permissions = array_remove(permissions, 'runs:write') where name = 'user';
update roles set name = 'admin'     where name = 'superadmin';
update roles set name = 'publisher' where name = 'editor';
update roles set name = 'viewer'    where name = 'user';

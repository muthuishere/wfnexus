DROP TABLE IF EXISTS workflow_members;
UPDATE users SET role = 'admin'     WHERE role = 'superadmin';
UPDATE users SET role = 'publisher' WHERE role = 'editor';
UPDATE users SET role = 'viewer'    WHERE role = 'user';
UPDATE roles SET permissions = (SELECT json_group_array(value) FROM json_each(roles.permissions) WHERE value <> 'runs:all') WHERE name = 'editor';
UPDATE roles SET permissions = (SELECT json_group_array(value) FROM json_each(roles.permissions) WHERE value <> 'runs:write') WHERE name = 'user';
UPDATE roles SET name = 'admin'     WHERE name = 'superadmin';
UPDATE roles SET name = 'publisher' WHERE name = 'editor';
UPDATE roles SET name = 'viewer'    WHERE name = 'user';

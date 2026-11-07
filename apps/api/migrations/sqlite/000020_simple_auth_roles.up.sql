-- The SQLite half of the Postgres 000026 migration (ADR 0028). Same renames;
-- roles.permissions is a JSON array here where Postgres has text[].
UPDATE roles SET name = 'superadmin' WHERE name = 'admin'
   AND NOT EXISTS (SELECT 1 FROM roles r WHERE r.name = 'superadmin');
UPDATE roles SET name = 'editor' WHERE name = 'publisher'
   AND NOT EXISTS (SELECT 1 FROM roles r WHERE r.name = 'editor');
UPDATE roles SET name = 'user' WHERE name = 'viewer'
   AND NOT EXISTS (SELECT 1 FROM roles r WHERE r.name = 'user');
UPDATE roles SET permissions = json_insert(permissions, '$[#]', 'runs:write')
 WHERE name = 'user' AND NOT EXISTS (SELECT 1 FROM json_each(roles.permissions) WHERE value = 'runs:write');
UPDATE roles SET permissions = json_insert(permissions, '$[#]', 'runs:all')
 WHERE name = 'editor' AND NOT EXISTS (SELECT 1 FROM json_each(roles.permissions) WHERE value = 'runs:all');

UPDATE users SET role = 'superadmin' WHERE role = 'admin';
UPDATE users SET role = 'editor'     WHERE role = 'publisher';
UPDATE users SET role = 'user'       WHERE role = 'viewer';

CREATE TABLE IF NOT EXISTS workflow_members (
    workflow   text NOT NULL,
    user_id    text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       text NOT NULL CHECK (role IN ('owner', 'editor')),
    created_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    PRIMARY KEY (workflow, user_id)
);
CREATE INDEX IF NOT EXISTS workflow_members_by_user ON workflow_members (user_id);

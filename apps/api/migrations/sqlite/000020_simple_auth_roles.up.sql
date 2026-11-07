-- The SQLite half of the Postgres 000026 migration (ADR 0028).
UPDATE users SET role = CASE role
    WHEN 'admin'     THEN 'superadmin'
    WHEN 'publisher' THEN 'legacy:publisher'
    WHEN 'viewer'    THEN 'legacy:viewer'
    ELSE '' END;

CREATE TABLE IF NOT EXISTS members (
    scope      text NOT NULL CHECK (scope IN ('project', 'workflow')),
    name       text NOT NULL,
    user_id    text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       text NOT NULL,
    created_at timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    PRIMARY KEY (scope, name, user_id),
    CHECK ((scope = 'project'  AND role = 'project_admin') OR
           (scope = 'workflow' AND role IN ('workflow_admin', 'workflow_runner')))
);
CREATE INDEX IF NOT EXISTS members_by_user ON members (user_id);

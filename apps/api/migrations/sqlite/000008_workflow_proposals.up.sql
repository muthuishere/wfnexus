-- The SQLite half of the Postgres 000014 workflow-proposal migration.
-- Same columns, this dialect: text for uuid, timestamp for timestamptz.
CREATE TABLE workflow_proposals (
    id              text PRIMARY KEY,
    project         text NOT NULL,
    workflow        text NOT NULL,
    kind            text NOT NULL,
    branch          text NOT NULL,
    base_branch     text NOT NULL DEFAULT '',
    commit_sha      text NOT NULL DEFAULT '',
    pr_url          text NOT NULL DEFAULT '',
    status          text NOT NULL DEFAULT 'pending',
    review_verdict  text NOT NULL DEFAULT 'unreviewed',
    review_reason   text NOT NULL DEFAULT '',
    review_score    real,
    note            text NOT NULL DEFAULT '',
    reason          text NOT NULL DEFAULT '',
    created_by      text NOT NULL DEFAULT '',
    decided_by      text NOT NULL DEFAULT '',
    created_at      timestamp NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    decided_at      timestamp
);
CREATE INDEX workflow_proposals_project_idx ON workflow_proposals (project, workflow, created_at);

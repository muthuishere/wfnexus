-- Workflow PROPOSALS: a save or delete on a git-backed project opens a branch
-- (and a PR when there is a remote) instead of writing the tracked checkout.
-- Git holds the change; this row holds what git does not: who asked, what the
-- classifier advised, and who decided.
create table if not exists workflow_proposals (
    id              uuid primary key,
    project         text not null,
    workflow        text not null,
    kind            text not null,                   -- create|edit|delete
    branch          text not null,
    base_branch     text not null default '',
    commit_sha      text not null default '',
    pr_url          text not null default '',
    status          text not null default 'pending', -- pending|approved|rejected|merged
    -- The classifier's ADVICE. The human decides; this never does unless an
    -- operator configured an auto-approve threshold.
    review_verdict  text not null default 'unreviewed', -- approve|reject|unreviewed
    review_reason   text not null default '',
    review_score    double precision,
    note            text not null default '',
    reason          text not null default '',        -- a rejection's reason
    created_by      text not null default '',
    decided_by      text not null default '',
    created_at      timestamptz not null default now(),
    decided_at      timestamptz
);
create index if not exists workflow_proposals_project_idx on workflow_proposals (project, workflow, created_at desc);

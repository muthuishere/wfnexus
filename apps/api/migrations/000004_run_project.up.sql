-- A run belongs to a PROJECT, the way a GitHub Actions run belongs to a repo:
-- project → workflow → runs. Without this a run list is one flat global stream
-- that stops making sense the moment a second repository is imported.
--
-- Backfilled to 'local' — the platform's own workflows directory — because that
-- is where every run so far came from, and a nullable column would push the
-- same defaulting into every query.
ALTER TABLE workflow_runs ADD COLUMN project text NOT NULL DEFAULT 'local';

-- The listing is always "this project, newest first".
CREATE INDEX workflow_runs_project_created_idx ON workflow_runs (project, created_at DESC);
-- And "this workflow within this project, newest first".
CREATE INDEX workflow_runs_project_workflow_idx ON workflow_runs (project, workflow, created_at DESC);

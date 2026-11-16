ALTER TABLE published_bundles DROP COLUMN tenant_id;
DROP INDEX IF EXISTS workflow_runs_tenant_created_idx;
ALTER TABLE workflow_runs DROP COLUMN tenant_id;
DROP TABLE IF EXISTS service_principal_tools;
DROP TABLE IF EXISTS service_principals;
DROP TABLE IF EXISTS tenant_projects;
DROP TABLE IF EXISTS tenants;

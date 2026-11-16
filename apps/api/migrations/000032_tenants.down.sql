alter table published_bundles drop column if exists tenant_id;
drop index if exists workflow_runs_tenant_created_idx;
alter table workflow_runs drop column if exists tenant_id;
drop table if exists service_principal_tools;
drop table if exists service_principals;
drop table if exists tenant_projects;
drop table if exists tenants;

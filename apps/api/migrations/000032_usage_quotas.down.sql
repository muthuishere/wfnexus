alter table projects drop column if exists max_cost_per_day;
alter table projects drop column if exists max_runs_per_day;
drop table if exists usage_runs;
drop table if exists project_usage;

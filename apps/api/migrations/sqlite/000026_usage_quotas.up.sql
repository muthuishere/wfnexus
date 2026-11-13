-- USAGE and QUOTAS per project (plan 18, W4).
--
-- project_usage: one row per project per UTC day. A run is accounted when it
-- ends (done / failed / cancelled); a run that is retried and ends again adds
-- only what it spent since (usage_runs holds what each run already counted).
create table if not exists project_usage (
    project  text    not null,
    day      text    not null, -- YYYY-MM-DD, UTC
    runs     integer not null default 0,
    steps    integer not null default 0,
    tokens   bigint  not null default 0,
    cost_usd real    not null default 0,
    primary key (project, day)
);

create table if not exists usage_runs (
    run_id   text    primary key,
    project  text    not null,
    steps    integer not null default 0,
    tokens   bigint  not null default 0,
    cost_usd real    not null default 0
);

-- 0 = no limit.
alter table projects add column max_runs_per_day integer not null default 0;
alter table projects add column max_cost_per_day real not null default 0;

-- What a workflow REMEMBERS between runs.
--
-- Every step output today dies with its run, so a scheduled workflow cannot say
-- "I last processed id 4120, start from 4121". This table is that memory.
--
-- FOUR SCOPES, narrowest to widest — the same (scope, scope_name) shape
-- env_vars uses, so the vocabulary is learned once:
--
--   step      scope_name = '<workflow>/<step id>'  one step, across runs
--   workflow  scope_name = '<workflow>'            one workflow, across runs
--   project   scope_name = '<project>'             every workflow in one repo
--   global    scope_name = ''                      everything
--
-- They are four NAMESPACES, not a cascade. A read of the step scope never
-- falls back to the workflow scope: env cascades because it is one environment
-- assembled from layers, while state is four separate places to put things, and
-- a silent fallback would make a missing key look like a stale one.
--
-- PLAINTEXT, deliberately. This is not a secret store — env_vars is, and it is
-- sealed. Nothing here is encrypted and nothing here should be a credential.
create table if not exists state_vars (
    scope      text        not null,            -- 'step'|'workflow'|'project'|'global'
    scope_name text        not null default '', -- '' for global
    key        text        not null,
    value      text        not null,
    updated_at timestamptz not null default now(),
    primary key (scope, scope_name, key)
);

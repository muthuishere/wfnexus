-- A job row keeps only env NAMES: values from the env store are delivered to
-- the worker when it claims the job, never stored. Rows written before that
-- held the values; blank them, keeping every name.
--
-- An old row cannot say which of its values came from the env store and which
-- the workflow wrote itself (it has no deliver list), so a job still waiting
-- for a worker cannot be run correctly after its values are blanked. It fails
-- instead, with a reason that says to re-run it. A job already leased keeps
-- running on its worker, which holds the values in memory.
update worker_jobs
   set status = 'done', finished_at = now(),
       result = '{"ok":false,"error":"this job was queued before an upgrade that changed how job settings are delivered; re-run the step"}'::jsonb
 where status = 'queued';
update worker_jobs
   set payload = jsonb_set(payload, '{env}',
       (select coalesce(jsonb_object_agg(k, ''), '{}'::jsonb) from jsonb_object_keys(payload->'env') k))
 where jsonb_typeof(payload->'env') = 'object';
update worker_jobs
   set payload = jsonb_set(payload, '{agent,step,env}',
       (select coalesce(jsonb_object_agg(k, ''), '{}'::jsonb) from jsonb_object_keys(payload->'agent'->'step'->'env') k))
 where jsonb_typeof(payload->'agent'->'step'->'env') = 'object';

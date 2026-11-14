-- A job row keeps only env NAMES: values from the env store are delivered to
-- the worker when it claims the job, never stored. Rows written before that
-- held the values; blank them, keeping every name.
--
-- An old row cannot say which of its values came from the env store and which
-- the workflow wrote itself (it has no deliver list), so a job still waiting
-- for a worker cannot be run correctly after its values are blanked. It fails
-- instead, with a reason that says to re-run it. A job already leased keeps
-- running on its worker, which holds the values in memory.
UPDATE worker_jobs
   SET status = 'done', finished_at = strftime('%Y-%m-%d %H:%M:%f', 'now'),
       result = '{"ok":false,"error":"this job was queued before an upgrade that changed how job settings are delivered; re-run the step"}'
 WHERE status = 'queued';
UPDATE worker_jobs
   SET payload = json_set(payload, '$.env',
       json((SELECT coalesce(json_group_object(key, ''), '{}') FROM json_each(worker_jobs.payload, '$.env'))))
 WHERE json_type(payload, '$.env') = 'object';
UPDATE worker_jobs
   SET payload = json_set(payload, '$.agent.step.env',
       json((SELECT coalesce(json_group_object(key, ''), '{}') FROM json_each(worker_jobs.payload, '$.agent.step.env'))))
 WHERE json_type(payload, '$.agent.step.env') = 'object';

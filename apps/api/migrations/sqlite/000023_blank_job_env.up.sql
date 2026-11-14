-- A job row keeps only env NAMES: values from the env store are delivered to
-- the worker when it claims the job, never stored. Rows written before that
-- held the values; blank them, keeping every name.
UPDATE worker_jobs
   SET payload = json_set(payload, '$.env',
       json((SELECT coalesce(json_group_object(key, ''), '{}') FROM json_each(worker_jobs.payload, '$.env'))))
 WHERE json_type(payload, '$.env') = 'object';
UPDATE worker_jobs
   SET payload = json_set(payload, '$.agent.step.env',
       json((SELECT coalesce(json_group_object(key, ''), '{}') FROM json_each(worker_jobs.payload, '$.agent.step.env'))))
 WHERE json_type(payload, '$.agent.step.env') = 'object';

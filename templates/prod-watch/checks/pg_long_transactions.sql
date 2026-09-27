-- title: Transactions open over 15 minutes in {datname}
-- severity: warning
-- component: database
-- key: datname
-- note: a long open transaction holds locks and blocks vacuum; the query text is deliberately not selected.
SELECT datname, state, count(*) AS n, max(now() - xact_start)::text AS oldest
FROM pg_stat_activity
WHERE xact_start < now() - interval '15 minutes' AND backend_type = 'client backend' AND pid <> pg_backend_pid()
GROUP BY 1, 2

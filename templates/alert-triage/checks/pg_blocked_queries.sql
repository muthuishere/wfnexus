-- title: Queries blocked on locks in {datname}
-- severity: warning
-- component: database
-- key: datname
SELECT datname, count(*) AS blocked, max(now() - query_start)::text AS longest
FROM pg_stat_activity
WHERE wait_event_type = 'Lock' AND query_start < now() - interval '1 minute'
GROUP BY 1

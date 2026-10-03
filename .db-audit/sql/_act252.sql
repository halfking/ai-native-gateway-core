SELECT state || ' | ' || coalesce(wait_event_type,'-') || ' | ' || coalesce(left(query,70),'-')
FROM pg_stat_activity
WHERE datname='llm_gateway' AND pid <> pg_backend_pid()
ORDER BY backend_start LIMIT 12;
SELECT 'total_backends=' || count(*) FROM pg_stat_activity WHERE datname='llm_gateway';
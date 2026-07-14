BEGIN;

DROP FUNCTION IF EXISTS analyze_llm_gateway_table_stats(integer);
DROP FUNCTION IF EXISTS apply_llm_gateway_autovacuum_settings();

COMMIT;

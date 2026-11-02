-- Down for 929: 只撤销登记（表可能承载 211 手工建的历史，不删）。
BEGIN;
DELETE FROM public.schema_migrations WHERE version = '929';
COMMIT;

-- 432_route_incident_events_evidence_jsonb.down.sql
-- 回滚 evidence 列 JSONB 转换（不执行实际回滚，保持 JSONB）

BEGIN;

-- 注意: 实际不回滚为 TEXT，因为会丢失数据类型优势
-- 此文件仅为满足 migration 规范要求，实际不建议执行

COMMIT;

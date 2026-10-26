-- dbinit:no-transaction —— 本文件含 DROP INDEX CONCURRENTLY，
-- PostgreSQL 不允许在事务块内执行它（见 installer/internal/dbinit/runner.go
-- noTransactionMarker）。用 CONCURRENTLY 而不是普通 DROP 是因为
-- route_incident_events 被网关持续写入，普通 DROP 的 ACCESS EXCLUSIVE 锁
-- 会阻塞写入；索引有 36 MB，drop 是元数据操作但锁的代价与大小无关。
--
-- Rollback for migration 871_route_incident_events_created_at_idx.sql.
-- Drops the index only; no data is touched.
--
-- ⚠️ 回滚是**倒退**：拿掉索引后，「按 created_at 删」会重新退化为全索引扫描，
--    在 statement_timeout=30s 下第一批就可能超时并留下死元组
--    （见 up 迁移开头的实测）。若回滚是为了缓解别的故障，
--    请同时确认没人正在跑事件留存 worker。

SET lock_timeout = '2s';
SET statement_timeout = '0';

DROP INDEX CONCURRENTLY IF EXISTS public.idx_route_incident_events_created_at;

RESET lock_timeout;
RESET statement_timeout;

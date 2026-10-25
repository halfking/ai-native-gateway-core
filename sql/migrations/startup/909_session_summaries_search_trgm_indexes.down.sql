-- dbinit:no-transaction —— CREATE/DROP INDEX CONCURRENTLY 不能进事务块
-- （同正本；session_summaries 是热写表，普通 DROP INDEX 会持 AccessExclusiveLock
-- 阻断写入）。installer 的 applySQL 据此走非事务通道
-- （见 installer/internal/dbinit/runner.go noTransactionMarker，822/908 同款）。
-- 会话级 SET + RESET（非事务通道，SET LOCAL 不生效，871 头注同款）。
-- ===========================================================================
-- File:          sql/migrations/startup/909_session_summaries_search_trgm_indexes.down.sql
-- Migration:     909 (down)
-- Database:      llm_gateway
-- Purpose:       回滚 909：删掉 session_summaries 的三条 GIN trigram 索引
--
-- Status:        active
-- Idempotent:    YES（IF EXISTS）
-- Dependencies:  909（正本）
--
-- ★ 回滚代价（★ 必须写清，★ 免得下一个读注释的人低估它）：
--   删掉这三条索引 ⇒ ★★ `admin/session_catalog_usage.go` 的 catalogSearchSQL
--   **立刻退回** 252 生产实测过的形态：
--     Parallel Seq Scan on session_summaries ⇒ **Execution Time ≈ 40.4 秒**
--   ⇒ 而 mergeCatalogSearch 的 ctx 超时是 **2 秒** ⇒ ⇒ **每次都超时**
--   ⇒ ⇒ ★★ **静默降级成「没搜到」**（★ 用户看到的是空结果，★ 不是报错）。
--   ⇒ ★★ **本 down 不是「性能回退一点」，★ 是「把一个静默的错误恢复回来」**。
--   ⇒ ⇒ **回滚前必须先确认有别的承重索引顶上**（★ 当下没有）。
--
-- ★ 本 down 不动数据、不动列、不动扩展 ⇒ 可反复执行。
-- ===========================================================================

SET lock_timeout = '60s';
SET statement_timeout = '10min';

DROP INDEX CONCURRENTLY IF EXISTS public.idx_session_summaries_primary_model_trgm;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_session_summaries_title_trgm;
DROP INDEX CONCURRENTLY IF EXISTS public.idx_session_summaries_session_key_trgm;

RESET statement_timeout;
RESET lock_timeout;

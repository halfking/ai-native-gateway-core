-- ===========================================================================
-- File:          sql/migrations/startup/983c_add_occurred_cursor_index.sql
-- Migration:     983c
-- Database:      llm_gateway
-- Purpose:       给request_metrics补(occurred_at DESC, request_id DESC)游标索引
--
-- Status:        active
-- Idempotent:    YES (CREATE INDEX IF NOT EXISTS)
-- Dependencies:  983 (request_metrics表)
--
-- Background:
--   internal/metricsstore/processor.go 的 watermarkSQL 读复合游标：
--     SELECT occurred_at, request_id FROM request_metrics
--     ORDER BY occurred_at DESC, request_id DESC LIMIT 1
--   ★ 该查询**每批跑一次**（每批读一次水位线）。
--
--   ★★ 2026-11-24 审计轮实测（★ 生产只读，211 / acc-pg17）：★★
--   ★★ ① 迁移 983 的 11 个索引里**没有**这个游标索引 ⇒
--   ★★   而 processor.go 的注释却写着「配套索引见迁移 983
--   ★★   （idx_request_metrics_occurred_cursor），避免每批次全表排序」
--   ★★   ⇒ ★ **那条承诺从未兑现**（注释说的索引从来不存在）★ ★
--   ★★ ② 实测执行计划（request_metrics 340,904 行）：
--   ★★     Parallel Seq Scan on request_metrics_default + top-N heapsort
--   ★★     ⇒ Buffers: shared read=26,572 ⇒ Execution Time: **117.227 ms**
--   ★★ ③ 根因：request_metrics 的主键是 **(request_id, occurred_at)**
--   ★★   （生产实测 request_metrics_pkey 定义）⇒ **列序与本查询的
--   ★★   (occurred_at, request_id) 相反** ⇒ 主键索引**无法用于该排序**
--   ★★   ⇒ 只能全表扫描 + 排序 ⇒ 索引缺失的代价被实测坐实。
--
--   ⇒ 本迁移补上这个索引：列序与 ORDER BY 完全一致 ⇒ 可走索引倒序扫描
--     ⇒ 不再全表排序。
--
-- ★★ 为什么是 983c（★ 同族序篇，★ 与 983b 同款记法）：
--   ★★ ① **9xx 段里大于 983 的三位数号已全部占满**（984-999 逐个核过，
--   ★★   且 990-999 另有同号文件）⇒ 没有三位数空号可用；
--   ★★   而仓内**全部 9xx 迁移的依赖号都小于自身号**（997→547、998→997/606、
--   ★★   955→922 全是降序）⇒ 任何更小的号都会排在 983 之前 ⇒ 逆序依赖复发
--   ★★   ⇒ ★ **字母后缀是本仓在这种号段下的唯一合法形态**
--   ★★   （★ 361b / 487b / 395b / 478p2 / 680b / 682b / 683b / 706b / 707b
--   ★★   共 15 条已交付件是同款先例 ★★）。
--   ★★ ② 排序：versionPrefix("983c_…") 解析成 983 ⇒ 与 983_request_metrics.sql
--   ★★   同号 ⇒ 排序按文件名 ⇒ '983_' < '983b_' < '983c_' ⇒ ★ 本迁移排在
--   ★★   建表件之后 ✓（两条自动枚举通道的字典序与数字序都成立）。
--   ★★ ③ 唯一性门把 983c 折叠回主键 983 且子版本记法不算独立身份 ⇒ 不算撞号。
--   ★★ ④ 部署通道 ver_tok 取整串 '983c' ⇒ 与 '983'/'983b' 不是同一版本。
--
-- Affected Rows: 0 (DDL操作)
-- Breaking Change: NO (纯新增索引)
-- Rollback Script: 983c_add_occurred_cursor_index.down.sql
--
-- Date: 2026-11-24
-- Author: Kiro AI
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

-- ★ 在**父表**上建索引 ⇒ PostgreSQL 自动为每个分区建同名分区索引
--   （★ 2026-11-24 生产只读实测：request_metrics 当前只有 request_metrics_default
--   ★ 一个 DEFAULT 分区 ⇒ 本迁移会在该分区上建 idx_request_metrics_occurred_cursor）。
--
-- ★ 列序必须与 watermarkSQL 的 ORDER BY **逐字一致**：
--   (occurred_at DESC, request_id DESC) ⇒ DESC 在前 ⇒ 才能走索引倒序扫描。
-- ★ 写成 (occurred_at, request_id) 升序也能避免全表排序（btree 可反向扫描），
--   ★ 但 DESC 与查询逐字一致时计划更直白 ⇒ 这里取 DESC。
--
-- ★ 不带 WHERE 子句：游标查询不带任何过滤条件 ⇒ 部分索引用不上。
CREATE INDEX IF NOT EXISTS idx_request_metrics_occurred_cursor
    ON public.request_metrics (occurred_at DESC, request_id DESC);

COMMENT ON INDEX public.request_metrics.idx_request_metrics_occurred_cursor IS
    '复合游标索引：(occurred_at DESC, request_id DESC) —— 与 internal/metricsstore/processor.go 的 watermarkSQL 的 ORDER BY 逐字一致 ⇒ 避免每批次全表排序（2026-11-24 生产实测：无此索引时 340,904 行全表扫描 + top-N 排序 = 117ms/批）';

-- ============================================================
-- 台账自登记（≥952 双账本纪律）
-- ============================================================
-- ★ 缺此段 ⇒ 任何通道应用后运行时台账无行 ⇒ 连续性门/漂移报告假红
INSERT INTO public.schema_migrations (version, description)
VALUES ('983c', 'add_occurred_cursor_index: request_metrics 补 (occurred_at DESC, request_id DESC) 复合游标索引（processor 水位线查询每批跑一次；无此索引时实测 117ms/批 全表排序）')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;

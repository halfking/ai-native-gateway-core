-- 888 down：回退三处索引缺口修复。
--
--   删三条新索引：
--     · idx_session_bodies_tenant_session_turn_request（父表，删父壳会级联删各分区子索引）
--     · idx_session_bodies_hot_tenant_session_turn_request（hot 表，独立一条）
--     · idx_sessions_tenant_session_partition_date（sessions 父表，同上）
--   按原样重建两条被替换的旧索引：
--     · 476 的 idx_session_bodies_tenant_session_turn (tenant_id, session_id, turn_no DESC)
--     · 614 的 idx_session_bodies_hot_lookup (session_id, turn_no, tenant_id)
--   不动数据。
--
-- ⚠ 诚实口径：**回滚会把缺口带回来** —— join 的 request_id 条件重新要回堆
--   recheck，hot 臂的索引列序重新与父表不一致，sessions 的按会话定位重新
--   退化成「session_id 等值 + 回堆过滤 tenant_id」。
--
-- ⚠ 锁口径：本文件**不使用** CONCURRENTLY（回滚路径按普通 DDL 走）⇒
--   父表上的 CREATE INDEX 会级联到每个分区并在构建期持 ShareLock。
--   session_bodies 的月分区在生产是 GB 级 ⇒ 回滚请在低峰窗口执行。
--   （up 方向走的是三段式 CONCURRENTLY，不受此限。）
--
-- ⚠ 本文件**不需要** installer 的非事务豁免标记：三条语句都不是
--   CONCURRENTLY，在 --single-transaction 下也能原子执行，保留事务是有意为之。
--   （此处刻意不写出该标记的字面量——installer 那道门按「注释里出现标记
--   字样」判定，写出来解释反而会被算成声明豁免。）
BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '30min';

-- ── 1. 删三条新索引 ──────────────────────────────────────────────────
-- 删父壳会级联脱离并删除各分区的子索引（ATTACH 关系随之解除）。
DROP INDEX IF EXISTS public.idx_session_bodies_tenant_session_turn_request;
DROP INDEX IF EXISTS public.idx_sessions_tenant_session_partition_date;
-- hot 表是独立表，父表索引不级联到它，必须单独删。
DROP INDEX IF EXISTS public.idx_session_bodies_hot_tenant_session_turn_request;

-- ── 2. 按原样重建两条被替换的旧索引 ─────────────────────────────────
-- 476 原样：(tenant_id, session_id, turn_no DESC)
CREATE INDEX IF NOT EXISTS idx_session_bodies_tenant_session_turn
    ON public.session_bodies USING btree (tenant_id, session_id, turn_no DESC);

-- 614 原样：(session_id, turn_no, tenant_id)
CREATE INDEX IF NOT EXISTS idx_session_bodies_hot_lookup
    ON public.session_bodies_hot USING btree (session_id, turn_no, tenant_id);

COMMIT;

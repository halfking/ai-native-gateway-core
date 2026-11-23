-- ===========================================================================
-- File:          sql/migrations/startup/925_session_summaries_total_credits_charged.sql
-- Migration:     925
-- Purpose:       在 981 **之前**补齐 session_summaries.total_credits_charged
-- Database:      llm_gateway
-- Down:          925_session_summaries_total_credits_charged.down.sql
--
-- ★ 为什么需要它（★ 2026-11-23 实测挖出的既有 P1，★ 非本轮引入）：
--   981_session_analytics_foundation.sql 的 4 个物化视图都引用
--     SUM(ss.total_credits_charged)（★ 例：user_session_aggregates 的
--     external_cost 列）⇒ ⇒ 而这一列在仓内是由 **990** 才
--     `ADD COLUMN IF NOT EXISTS` 加的 ⇒ ⇒ **981 < 990**
--   ⇒ ⇒ 全新安装库跑到 981 时**这一列还不存在** ⇒ ⇒ 42P03
--   ⇒ ⇒ **全新安装链在 981 中止** ⇒ ⇒ 982 之后的全部迁移（★ 含 982-985
--     团队四件套）在新装环境永远拿不到。
--   ★ 真实报错（★ 沙箱实跑）：ERROR: column ss.total_credits_charged does not exist
--     LINE 13: COALESCE(SUM(ss.total_credits_charged), 0) AS extern...
--
-- ★ ★ 为什么生产装得上、新装装不上（★ 2026-11-23 211 只读实测）：
--   211 的 schema_migrations 版本序列是 … 979, 980, **【无 981】**, 990, 991 …
--   ⇒ 独缺 '981'；★ 而 981 的产物**全部在场**（4 个物化视图 + session_dim /
--   session_owners 两张表 + 3 个函数）⇒ ⇒ **它确实跑过**
--   ⇒ ⇒ ★ ⇒ 生产库跑 981 时**那一列早就存在**（★ 生产是遗留 schema 演进来的，
--   ★ session_summaries 的那列早于 9xx 链）⇒ ⇒ 这是「生产装得上、新装装不上」
--   的第二个独立实例（★ 第一个是 956 的模型目录行）。
--   ★★ 顺带说明 989（★ 上游 L20 轮新增）：989 修的是**另一件事**——
--   981 的文件**缺 schema_migrations 自登记行**（★ 记账链断一格）⇒ ★ 与本件
--   的「列依赖」是两回事 ⇒ ★★ 本件**仍需**（★ 989 不解决 42P03）。
--
-- ★ 形态：列定义与 990 **逐字同形** ⇒ ★ 990 之后本列变成 no-op ⇒ 零冲突；
--   幂等：ADD COLUMN IF NOT EXISTS
-- 顺序：必须排在 981 **之前**（★ 981 的视图要读这一列）
-- ===========================================================================

-- ★ 锁与语句预算守卫（★ §44 纪律：>870 且含强锁 DDL 的迁移必须自带守卫；
--   ★ 且实测「事务块外 SET LOCAL 不生效」⇒ ★ 用**会话级** SET ⇒ docker 流式
--   通道也生效 ⇒ ★ 994/995/996 同款）。
SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

ALTER TABLE public.session_summaries
    ADD COLUMN IF NOT EXISTS total_credits_charged BIGINT NOT NULL DEFAULT 0;

-- 台账自登记（≥952 纪律）
INSERT INTO public.schema_migrations (version, description)
VALUES ('925', 'session_summaries.total_credits_charged: pre-981 column precondition (same shape as 990; 981 views reference it)')
ON CONFLICT (version) DO NOTHING;

COMMIT;

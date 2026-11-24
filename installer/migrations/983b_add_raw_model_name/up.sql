-- ===========================================================================
-- File:          sql/migrations/startup/983b_add_raw_model_name.sql
-- Migration:     983b
-- Database:      llm_gateway
-- Purpose:       向request_metrics表添加raw_model_name字段，记录原始模型名称
--
-- Status:        active
-- Idempotent:    YES (ADD COLUMN IF NOT EXISTS)
-- Dependencies:  983 (request_metrics表)
--
-- Background:
--   当前request_metrics.outbound_model存储的是映射后的标准模型名称，
--   但缺少原始模型名称字段。为了支持：
--   1. 追溯原始请求使用的模型名称
--   2. 统计时能灵活选择按原始名或标准名聚合
--   3. 排查模型别名映射问题
--
--   需要添加raw_model_name字段记录usage_facts.raw_model_name的原始值。
--
-- ★★ 编号说明（2026-11-24 12h 修订审计轮）：本迁移原名 994_add_raw_model_name，
--   与 994_session_analytics_owner_key_and_close_path **撞号**（迁移号唯一性门红）
--   且**任何投递通道都没有登记它**（embeddata / StartupFiles / tsv / 部署通道全缺）
--   ⇒ raw_model_name 这一列在生产上根本不存在 ⇒ 写 request_metrics 的那条 SQL
--   （internal/metricsstore/writer.go）必然 42703 ⇒ 整条统计管线静默丢数。
--   按仓内规矩「**新代码必须换新号**」换号，并补齐九面登记。
--
-- ★★★ 改号过程（★ 2026-11-24 12h 修订审计轮**第二轮**：★ 第一次改号引入的
-- ★★★ 新缺陷被自查发现并修掉，★ 下面是最终形态与依据）：
--
--   · 第一次改号取 **1000** ⇒ 被 `TestCanonicalNonPlainMigrationsHaveDeclaredDeliveryLeg`
--     当场拦下 ⇒ **四位数对全部按文件名解析的门完全隐形**（sqlguard 的
--     migrationNumPrefix 钉死 `^[0-9]{3}_`）⇒ 退回三位数。
--   · 第二次改号取 **973** ⇒ ★★ **这一步引入了新缺陷（本文件就是修它的）** ★★：
--     仓内**全部 9xx 迁移的依赖号都小于自身号**（997→547、998→997/606、955→922
--     全是降序；★ 本文件是**全仓唯一**声明了逆序依赖的迁移）⇒ 而**两条按号/按名
--     自动枚举的通道**不读 Dependencies 头、只按号排序：
--       ① installer/internal/upgrader/offline_apply.go 的 forwardMigrationFiles
--          （★ 数字前缀升序 ⇒ 973 < 983 ⇒ 本迁移排在建表件之前）
--          ⇒ 且 :359 用 ON_ERROR_STOP=1 ⇒ **离线升级整体中止**；
--       ② scripts/init-local-db.sh 的 `[0-9]*.sql` glob（★ 字典序 ⇒ 同上）
--          ⇒ 失败只计数 ⇒ 建表但无该列 ⇒ 落库必 42703（★ 正是本轮要修的症状）。
--     ⇒ ★★ **逆序依赖在「自动枚举通道」里是致命的，而不只是两条显式通道的顺序问题**。
--
--   · 最终形态：**983b_add_raw_model_name.sql** ⇒ ★ 「主号+字母后缀」是仓内
--     **既有的合法记法**（361b / 487b / 395b / 478p2 / 680b / 682b / 683b / 706b /
--     707b 共 15 条已交付件）⇒ 三道门各自对它都有既定处置：
--       ① 迁移号唯一性门：把 983b 折叠回主键 983，且子版本记法**不算独立身份**
--          ⇒ 不算撞号（★ 同 361b / 478p2 先例）；
--       ② 顺序：versionPrefix("983b_...") 解析成 983 ⇒ 与 983_request_metrics.sql
--          **同号** ⇒ 排序按文件名 ⇒ ★ '983_request_metrics.sql' < '983b_...' ★
--          ⇒ **本迁移排在建表件之后** ⇒ 两条自动枚举通道（数字序 / 字典序）都成立；
--       ③ 部署通道：ver_tok 取整串 '983b' ⇒ 与 '983' **不是同一版本**
--          ⇒ 冲突判定不误报、落账走 applied_suffix 线性表（★ 361b 同款）。
--     ⇒ ★ 语义上也更准：它是 **983 的补列件**（不是 973 的独立迁移）。
--
--   · ★ 为什么不能并号（把本文件内容并进 983）：
--     ★ **983 在生产上已 applied**（211 / acc-pg17 只读实测：public.schema_migrations
--     ★ 有 '983' 行、无 '983b' 行）⇒ 改 983 的字节 = **内容指纹分叉**（L23 教训）
--     ★ ⇒ 已 applied 的迁移永不重跑 ⇒ 库里旧、仓内新 ⇒ 部署在迁移校验段被拦下。
--     ★ 而**本文件（983b）尚未 applied** ⇒ 改它的字节/编号零风险。
--
--   · ★ 生产现状注记（2026-11-24 只读实测）：request_metrics 上**已经有**
--     ★ raw_model_name 列（45 列，末列）且 idx_request_metrics_raw_model 索引在场
--     ★ ⇒ 该列是被**别的路径**补上的，**不是本迁移**（本迁移未 applied、未记账）
--     ★ ⇒ ★ 本迁移的价值 = **把这条未记账的列补进九面登记 + 补台账自登记**
--     ★   （★ 幂等 ADD COLUMN IF NOT EXISTS ⇒ 重跑无害）★。
--
-- Affected Rows: 0 (DDL操作)
-- Breaking Change: NO (纯新增字段)
-- Rollback Script: 983b_add_raw_model_name.down.sql
--
-- Date: 2026-11-24
-- Author: Kiro AI
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '30s';

-- 添加raw_model_name字段
ALTER TABLE public.request_metrics 
ADD COLUMN IF NOT EXISTS raw_model_name TEXT;

-- 创建索引支持按原始模型名查询
CREATE INDEX IF NOT EXISTS idx_request_metrics_raw_model 
ON public.request_metrics (raw_model_name, occurred_date);

-- 添加注释
COMMENT ON COLUMN public.request_metrics.raw_model_name IS '原始模型名称（来自usage_facts.raw_model_name）';

-- ============================================================
-- 台账自登记（≥952 双账本纪律）
-- ============================================================
-- ★ 缺此段 ⇒ 任何通道应用后运行时台账无行 ⇒ 连续性门/漂移报告假红
-- ★ 本迁移原先**没有**这一段 ⇒ 即使被投递也不会在运行时台账留行
INSERT INTO public.schema_migrations (version, description)
VALUES ('983b', 'add_raw_model_name: request_metrics 补记原始模型名列')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;

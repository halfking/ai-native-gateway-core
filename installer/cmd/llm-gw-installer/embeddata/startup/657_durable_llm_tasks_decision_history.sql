-- 657_durable_llm_tasks_decision_history.sql
--
-- 2026-09-05 审计闭环3：durable task 持久化契约扩展 —— 保存有界
-- DecisionHistory，使 durable_recovery_worker 能够从 AggregateTaskOutcome
-- （无历史、无循环检测）迁移到 AggregateTaskOutcomeWithHistory。
--
-- 背景（docs/audit-2026-09-05-ir-storage-provider.md）：
--   worker 每次是进程冷启动的 detached 执行，DecisionHistory 在前台只存在
--   于 SurvivalResult 内存结构，从没写入 durable 存储；直接换聚合入口只能
--   传零值 history（伪迁移）。本迁移先落地持久化载体，再迁移 worker。
--
-- 语义：
--   - decision_history 由前台 CreateAndClaim 时为 NULL（无历史）；
--   - worker 每次 detached attempt 结束后以 (lease_owner, fencing_token)
--     fencing 条件 UPSERT 追加后的有界历史（appendSurvivalHistory 上限
--     128 条 PriorAttempt，内容零敏感字段：仅序号/模型/provider/kind/
--     action 等 leave-behind 元数据）；
--   - 读取失败/损坏按空历史继续（历史只是循环检测输入，不是正确性门）。

ALTER TABLE durable_llm_tasks ADD COLUMN IF NOT EXISTS decision_history JSONB;

COMMENT ON COLUMN durable_llm_tasks.decision_history IS
'有界 DecisionHistory（errorsx.DecisionHistory JSON，≤128 条 PriorAttempt）；
durable worker 跨重启的循环检测输入。2026-09-05 审计闭环3（迁移 657）。';

DO $do$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'durable_llm_tasks'
          AND column_name = 'decision_history'
    ) THEN
        RAISE EXCEPTION 'migration 657 post-condition failed: decision_history missing';
    END IF;
    RAISE NOTICE 'migration 657 OK: durable_llm_tasks.decision_history added';
END
$do$;

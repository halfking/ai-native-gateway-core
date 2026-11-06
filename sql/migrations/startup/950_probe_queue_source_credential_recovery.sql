-- 950: credential_probe_queue.source 的 CHECK 闭集补 `credential_recovery`。
--
-- 缺口本体（2026-11-07 探测时间点规划轮，本地部署实测）：
--   F1 修复（恢复事件的验证探针）把 credRecovery 的 probeSubmitter 从
--   `Submit(..., source 缺省 → request_failure)` 改为显式
--   `SubmitWithSource(..., "credential_recovery")` —— 该值早在 migration 425
--   就已进入 node_probe_runs.trigger_kind 闭集，但 **队列侧** 的镜像约束
--   `credential_probe_queue_source_check`（489 建、538 重build）从未跟上：
--   538 的注释明写「Keep the queue producer and audit enum in lock-step」，
--   而当时没有任何 producer 用 `credential_recovery` ⇒ 没进闭集。
--   ⇒ 恢复事件提交被 DB 以 **23514** 拒绝 ⇒ 3 次重试烧尽后
--   persistSubmitFailure 把 last_err_code 覆写成 queue_submit_failed
--   （glm-5.3 洪水 P-1 同族的「基础设施失败覆写真实证据」形状）。
--   本地 2946 部署实测：`llmgw_node_probe_queue_submission_total
--   {source="credential_recovery",outcome="failed"}` 5 分钟内 +151。
--
-- ★ 为何是**新迁移**而不是改 538 的字节：538 已进部署台账（content_sha256）
--   ⇒ 改字节会走 content replay 通道并连锁 break（914/928/929 教训）。
--
-- 修法：重建该 CHECK，在 538 六值闭集上加 `credential_recovery` ⇒ 7 项。
--   与 node_probe_runs.trigger_kind 的对应值（425 加入）保持锁步 ——
--   `bg/probe_service.go knownTriggerKind` 早已含它，审计面无需改动。
--
-- 幂等：先读 `pg_get_constraintdef`，若已包含 `credential_recovery` ⇒ 直接跳过。
--   可重入 ⇒ 重复执行不重建约束。
--
-- 锁面：DROP CONSTRAINT + ADD CONSTRAINT 各拿一次 ACCESS EXCLUSIVE。
--   credential_probe_queue 是探测调度表（行数 = 在途/近期任务，量级 ~百），
--   约束重建的锁窗毫秒级；探测任务写入方（ProbeQueueWorker）在锁窗内
--   短暂阻塞，无死锁面（单语句）。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

DO $$
DECLARE
    cur_def text;
BEGIN
    SELECT pg_get_constraintdef(oid) INTO cur_def
    FROM pg_constraint
    WHERE conrelid = 'public.credential_probe_queue'::regclass
      AND conname = 'credential_probe_queue_source_check';

    IF cur_def IS NULL THEN
        RAISE EXCEPTION 'credential_probe_queue_source_check not found — schema drift, refusing to blind-add';
    END IF;

    IF cur_def LIKE '%credential_recovery%' THEN
        RAISE NOTICE '950: source_check already covers credential_recovery, skipping';
        RETURN;
    END IF;

    ALTER TABLE public.credential_probe_queue
        DROP CONSTRAINT credential_probe_queue_source_check;
    ALTER TABLE public.credential_probe_queue
        ADD CONSTRAINT credential_probe_queue_source_check CHECK (
            source IN ('request_failure', 'periodic', 'external_async', 'admin',
                       'integrity_probe_planner', 'selfcheck', 'credential_recovery')
        );
    RAISE NOTICE '950: source_check rebuilt with credential_recovery (7 values)';
END
$$;

COMMIT;

\echo '=== 950 完成：credential_probe_queue.source 闭集 + credential_recovery ==='

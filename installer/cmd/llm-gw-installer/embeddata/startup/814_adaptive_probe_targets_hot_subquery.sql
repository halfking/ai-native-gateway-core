-- ===========================================================================
-- File:          sql/migrations/startup/814_adaptive_probe_targets_hot_subquery.sql
-- Migration:     814
-- Database:      llm_gateway
-- Purpose:       v_adaptive_probe_targets.recent_passive_failures 死列修复
--                （R21 N2 拍板轮：子查询从分区父表改读 candidate_failure_logs_hot）
--
-- Status:        active
-- Idempotent:    YES（CREATE OR REPLACE VIEW；列集/顺序不变）
-- Dependencies:  392（candidate_failure_logs_hot 表）/ 038（视图本体）
--
-- Background：
--   v_adaptive_probe_targets.recent_passive_failures 是相关子查询，读
--   candidate_failure_logs 分区父表；而 0-8h 的行按 promote 保留期语义
--   全部躺在独立双写表 candidate_failure_logs_hot（810/101 事实：hot ⊆
--   分区，"先拷贝后延迟删"）。谓词 ts > now()-'5 minutes' 的行必然远新于
--   8h promote 线 ⇒ 分区侧行集恒为空 ⇒ 该列自 038（07-05）起恒 0
--   （R18 fact 103），probe urgency 的 passive-failure 信号死亡。
--
--   改读 hot 的语义等价性：5 分钟窗 ⊂ 8 小时保留窗 ⇒ hot 是该谓词的
--   精确源（不多不少）；hot 有 (credential_id, ts DESC) /
--   (raw_model_name, ts DESC) 复合索引（252 实测在位）⇒ 相关子查询走
--   索引而非分区父表多臂扫描。
--
--   消费方现状（R21 实证）：全仓 grep 无任何 Go/admin SQL 引用本视图
--   （bg/model_probe.go 的 cycle() 已内联同形过滤、applyPassiveBoosts 走
--   candidate_failure_logs_with_current_month；仅 627 行注释残留）。
--   拍板=修不删：视图在 baseline 三件套中对外存在，修死列保持契约正确，
--   对象退役（如需）归存储轨道清单。R20 §三"probe-target 家族 ×425/
--   2,910s 是本视图代价"的归因经日志取证修正：该家族实为 model_probe
--   cycle 目标查询（JOIN provider_models…）与 project_backfill
--   （WITH targets AS…）两类语句的合并标签，与本视图无关——修复收益
--   是语义正确性（未来消费方不再拿到恒 0 信号），不是现网减负。
-- ===========================================================================

BEGIN;

DO $$
BEGIN
    -- to_regclass 守卫（612 层纪律）：hot 表缺席的库上直通跳过。
    IF to_regclass('public.candidate_failure_logs_hot') IS NULL THEN
        RAISE NOTICE '814: candidate_failure_logs_hot absent; view fix skipped';
        RETURN;
    END IF;

    EXECUTE $view$
CREATE OR REPLACE VIEW public.v_adaptive_probe_targets AS
 SELECT cmb.id AS binding_id,
    cmb.credential_id,
    pm.raw_model_name,
    COALESCE(mps.consecutive_failures, 0) AS consecutive_failures,
    COALESCE(mps.consecutive_successes, 0) AS consecutive_successes,
    COALESCE(mps.state, 'unknown'::text) AS probe_state,
    mps.last_attempt_at,
    mps.next_retry_at,
    EXTRACT(epoch FROM (now() - COALESCE(mps.last_attempt_at, (now() - '01:00:00'::interval)))) AS age_secs,
    ( SELECT count(*) AS count
           FROM public.candidate_failure_logs_hot cfl
          WHERE ((cfl.credential_id = cmb.credential_id) AND (cfl.raw_model_name = pm.raw_model_name) AND (cfl.ts > (now() - '00:05:00'::interval)))) AS recent_passive_failures
   FROM ((((public.credential_model_bindings cmb
     JOIN public.provider_models pm ON ((pm.id = cmb.provider_model_id)))
     JOIN public.credentials c ON ((c.id = cmb.credential_id)))
     JOIN public.providers p ON ((p.id = c.provider_id)))
     LEFT JOIN public.model_probe_state mps ON (((mps.credential_id = cmb.credential_id) AND (mps.raw_model_name = pm.raw_model_name))))
  WHERE ((COALESCE(c.status, 'active'::text) = 'active'::text) AND (COALESCE(c.lifecycle_status, 'active'::text) = 'active'::text) AND (COALESCE(c.availability_state, 'ready'::text) <> 'suspended'::text) AND (COALESCE(c.quota_state, 'ok'::text) <> ALL (ARRAY['permanently_exhausted'::text, 'balance_exhausted'::text])) AND (COALESCE(p.enabled, false) = true) AND (COALESCE(p.manual_disabled, false) = false) AND (COALESCE(c.manual_disabled, false) = false) AND (COALESCE(cmb.unavailable_reason, ''::text) !~~ 'manual%'::text) AND (COALESCE(mps.state, 'unknown'::text) <> 'broken_confirmed'::text));
$view$;

    RAISE NOTICE '814: v_adaptive_probe_targets subquery re-pointed to candidate_failure_logs_hot';
END $$;

COMMIT;

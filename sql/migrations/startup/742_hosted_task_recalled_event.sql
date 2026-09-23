-- ===========================================================================
-- File:          sql/migrations/startup/742_hosted_task_recalled_event.sql
-- Migration:     742
-- Database:      llm_gateway
-- Purpose:       R65（docs/design/hosted-task-delegation-design.md §3.3/§4.3）
--                hosted_task_events 类型约束扩容：新增 'recalled'，供
--                POST /v1/hosted-tasks/{id}/recall（§3.3 轻量快照路径）追加
--                召回事件。三处同步之迁移侧（另两处：domains/hostedtask/
--                types.go EventRecalled、设计文档 §4.3）。
--
--                711 原 CHECK 白名单不含 'recalled'（P0 五端点无召回；
--                §4.3 明确 P1 事件需先改迁移）。迁移号 741 已被 B11 申领，
--                故从 742 起（2026-09-23）。
-- ===========================================================================

BEGIN;

ALTER TABLE hosted_task_events DROP CONSTRAINT hosted_task_events_type_check;

ALTER TABLE hosted_task_events ADD CONSTRAINT hosted_task_events_type_check
    CHECK (event_type IN ('accepted', 'dispatch_degraded', 'running', 'progress',
                          'cancel_requested', 'completed', 'failed', 'expired',
                          'cancelled', 'callback_delivered', 'callback_dlq',
                          'recalled'));

-- 验证：约束定义已含 'recalled'。
DO $$
DECLARE
  def text;
BEGIN
  SELECT pg_get_constraintdef(oid) INTO def
    FROM pg_constraint
   WHERE conname = 'hosted_task_events_type_check'
     AND conrelid = 'hosted_task_events'::regclass;
  IF def IS NULL THEN
    RAISE EXCEPTION '742 up: hosted_task_events_type_check missing';
  END IF;
  IF def NOT LIKE '%recalled%' THEN
    RAISE EXCEPTION '742 up: constraint does not accept recalled: %', def;
  END IF;
  RAISE NOTICE '742 up: hosted_task_events_type_check now accepts recalled';
END $$;

-- Ledger self-registration（710/734/738/740 惯例）。带存在性守卫：一次性
-- 测试库（TEST_PG_URL 直灌 711+742 裸 SQL）没有 installer 基座的
-- schema_migrations 表，守卫使迁移在两种环境都可执行；生产库恒有该表，
-- 行为与 740 一致。
DO $$
BEGIN
  IF to_regclass('public.schema_migrations') IS NOT NULL THEN
    INSERT INTO public.schema_migrations (version, description)
    VALUES ('742', 'hosted_task_events type check admits recalled (R65: POST /v1/hosted-tasks/{id}/recall lightweight handoff path, design §3.3/§4.3)')
    ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
  END IF;
END $$;

COMMIT;

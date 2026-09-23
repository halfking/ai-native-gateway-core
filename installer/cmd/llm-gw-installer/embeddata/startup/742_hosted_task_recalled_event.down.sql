-- ===========================================================================
-- File:          sql/migrations/startup/742_hosted_task_recalled_event.sql.down
-- Migration:     742 down
-- Purpose:       还原 711 的事件类型白名单：先清除 recalled 事件行
--                （append-only 台账的召回事件属可弃数据；任务本体状态在
--                hosted_tasks，不受影响），再换回原 CHECK。
-- ===========================================================================

BEGIN;

DELETE FROM hosted_task_events WHERE event_type = 'recalled';

ALTER TABLE hosted_task_events DROP CONSTRAINT hosted_task_events_type_check;

ALTER TABLE hosted_task_events ADD CONSTRAINT hosted_task_events_type_check
    CHECK (event_type IN ('accepted', 'dispatch_degraded', 'running', 'progress',
                          'cancel_requested', 'completed', 'failed', 'expired',
                          'cancelled', 'callback_delivered', 'callback_dlq'));

DO $$
DECLARE
  def text;
BEGIN
  SELECT pg_get_constraintdef(oid) INTO def
    FROM pg_constraint
   WHERE conname = 'hosted_task_events_type_check'
     AND conrelid = 'hosted_task_events'::regclass;
  IF def IS NULL OR def LIKE '%recalled%' THEN
    RAISE EXCEPTION '742 down: constraint not restored to 711 shape: %', def;
  END IF;
  RAISE NOTICE '742 down: hosted_task_events_type_check restored (no recalled)';
END $$;

DO $$
BEGIN
  IF to_regclass('public.schema_migrations') IS NOT NULL THEN
    DELETE FROM public.schema_migrations WHERE version = '742';
  END IF;
END $$;

COMMIT;

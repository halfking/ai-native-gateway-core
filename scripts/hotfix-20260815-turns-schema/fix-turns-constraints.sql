-- 476 号迁移的幂等落地：V2 表唯一约束改为 tenant 维度（表当前为空，无重建成本）
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.session_turns'::regclass AND conname='session_turns_tenant_request_partition_key') THEN
    ALTER TABLE public.session_turns
      DROP CONSTRAINT IF EXISTS session_turns_session_id_turn_no_partition_date_key,
      DROP CONSTRAINT IF EXISTS session_turns_request_id_partition_date_key;
    ALTER TABLE public.session_turns
      ADD CONSTRAINT session_turns_tenant_session_turn_partition_key UNIQUE (tenant_id, session_id, turn_no, partition_date),
      ADD CONSTRAINT session_turns_tenant_request_partition_key UNIQUE (tenant_id, request_id, partition_date);
  END IF;

  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='public.session_bodies'::regclass AND conname='session_bodies_tenant_request_partition_key') THEN
    ALTER TABLE public.session_bodies
      DROP CONSTRAINT IF EXISTS session_bodies_session_id_turn_no_partition_date_key,
      DROP CONSTRAINT IF EXISTS session_bodies_request_id_partition_date_key;
    ALTER TABLE public.session_bodies
      ADD CONSTRAINT session_bodies_tenant_session_turn_partition_key UNIQUE (tenant_id, session_id, turn_no, partition_date),
      ADD CONSTRAINT session_bodies_tenant_request_partition_key UNIQUE (tenant_id, request_id, partition_date);
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_session_turns_tenant_session_turn
  ON public.session_turns (tenant_id, session_id, turn_no DESC);
CREATE INDEX IF NOT EXISTS idx_session_bodies_tenant_session_turn
  ON public.session_bodies (tenant_id, session_id, turn_no DESC);
SELECT 'constraints ok';

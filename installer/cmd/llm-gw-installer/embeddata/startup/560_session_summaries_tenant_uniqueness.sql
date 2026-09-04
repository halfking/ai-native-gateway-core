-- 560_session_summaries_tenant_uniqueness.sql
-- T11-P0 v2: 把 (tenant_id, session_key) 加入 UNIQUE 约束，防御跨租户 session_key 碰撞。
--
-- 背景：
--   session_summaries 当前 PK 是 session_key（migration 310 + 单独 constraint 文件）。
--   两个租户在 RLS 绕过 / DB 直连审计员路径下共用同一 session_key 时，会互相覆盖
--   summary 行，并触发 summary_version 自增的 xmax race 检测误判。
--
-- 决策（T11-P0 评审结论 + 2026-08-22 部署修正）：
--   - 不替换 PK（避免 production 长锁表）。
--   - 加 UNIQUE(tenant_id, session_key)。PostgreSQL 不允许 UNIQUE ... NOT VALID
--     （仅 FK/CHECK 支持），故直接 ADD CONSTRAINT；在 session_key 已唯一时校验很快。
--   - 现有索引 idx_session_summaries_tenant_time 已覆盖 (tenant_id, last_request_at)
--     查询，本约束不影响。
--
-- 预检（DO $$ 块）：
--   若当前已存在跨租户 session_key 冲突（业务上不应该发生，但 RLS bypass 场景下可能
--   残留），仅打 WARNING，不阻塞 ALTER。运维需先清理冲突数据。

DO $$
DECLARE bad_count INT;
BEGIN
  SELECT COUNT(*) INTO bad_count FROM (
    SELECT session_key FROM public.session_summaries
    GROUP BY session_key HAVING COUNT(DISTINCT tenant_id) > 1
  ) d;
  IF bad_count > 0 THEN
    RAISE WARNING 'session_summaries: % session_key values already collide across tenants; '
                  'manual cleanup required before relying on UNIQUE(tenant_id, session_key)', bad_count;
  END IF;
END $$;

DO $$
BEGIN
  IF to_regclass('public.session_summaries') IS NULL THEN
    RAISE NOTICE 'migration 560: session_summaries is absent; 655 or the base schema must run first';
  ELSIF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conrelid = 'public.session_summaries'::regclass
      AND conname = 'session_summaries_session_key_per_tenant'
  ) THEN
    ALTER TABLE public.session_summaries
      ADD CONSTRAINT session_summaries_session_key_per_tenant
      UNIQUE (tenant_id, session_key);
  ELSE
    RAISE NOTICE 'migration 560: session_summaries_session_key_per_tenant already exists';
  END IF;
END $$;

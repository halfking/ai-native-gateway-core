-- 560_session_summaries_tenant_uniqueness.sql
-- T11-P0 v2: 把 (tenant_id, session_key) 加入 UNIQUE 约束，防御跨租户 session_key 碰撞。
--
-- 背景：
--   session_summaries 当前 PK 是 session_key（migration 310 + 单独 constraint 文件）。
--   两个租户在 RLS 绕过 / DB 直连审计员路径下共用同一 session_key 时，会互相覆盖
--   summary 行，并触发 summary_version 自增的 xmax race 检测误判。
--
-- 决策（T11-P0 评审结论）：
--   - 不替换 PK（避免 production 6h 锁表）。
--   - 加 UNIQUE(tenant_id, session_key)，NOT VALID 不阻塞写，新写入立即校验，
--     旧数据事后 VALIDATE 一次性校验。
--   - 现有索引 idx_session_summaries_tenant_time 已覆盖 (tenant_id, last_request_at)
--     查询，本约束不影响。
--
-- 预检（DO $$ 块）：
--   若当前已存在跨租户 session_key 冲突（业务上不应该发生，但 RLS bypass 场景下可能
--   残留），仅打 WARNING，不阻塞 ALTER。运维需先清理冲突数据，再手动 VALIDATE 约束。

DO $$
DECLARE bad_count INT;
BEGIN
  SELECT COUNT(*) INTO bad_count FROM (
    SELECT session_key FROM public.session_summaries
    GROUP BY session_key HAVING COUNT(DISTINCT tenant_id) > 1
  ) d;
  IF bad_count > 0 THEN
    RAISE WARNING 'session_summaries: % session_key values already collide across tenants; '
                  'manual cleanup required before VALIDATE CONSTRAINT', bad_count;
  END IF;
END $$;

ALTER TABLE public.session_summaries
  ADD CONSTRAINT session_summaries_session_key_per_tenant
  UNIQUE (tenant_id, session_key)
  NOT VALID;

-- 后续（不在本 migration 内）：运维跑 VALIDATE CONSTRAINT session_summaries_session_key_per_tenant;
-- 一次性校验旧数据；耗时与表大小成正比，不阻塞写。
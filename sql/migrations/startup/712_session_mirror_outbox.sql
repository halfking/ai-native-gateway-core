-- Migration 712: session_mirror_outbox — durable replay queue for the
-- sessionv2mirror shadow write (spec §12 GAP 2 闭环).
--
-- 背景（2026-09-15 观察台账 Round 1b）：mirror hook 为 best-effort，写失败
-- （超时/DB 错）与 8 槽满仅进 in-process backlog（cap 10000，重启即丢，无
-- 重放器），本机实测 24h 终态 v1 行缺 turns 3.71%（258/6956），credits 随行
-- 丢失 → D7 计费等值不可能达标 → S4 停写被阻塞。
--
-- 设计（方案对比定案，见 plan §4-S4 前置项）：
--   - 仅失败路径 outbox 化（~3.7% → 回填后趋零），正常路径与 8 槽/2000ms
--     前台预算零改动；全量 outbox 化被否（100% 写两遍与存储优化目标相悖；
--     且 session_aggregate_outbox 的 payload 是 SessionUpdate 聚合快照，
--     重放走 UpdateSession 不写 turns，无法承载本需求）。
--   - payload = 完整 telemetry.RequestLogEntry JSON（struct 全量 json tag）。
--     重放 unmarshal → 复用 entryToProcessedRequest 同一桥接 → v2.Write
--     （request_id 幂等，ON CONFLICT DO NOTHING）。payload 自带全量事实，
--     S4 停写 request_logs 后重放不依赖 v1 反查。
--   - 历史回填（GLOBAL_G2 归零）与 hook 失败登记共用本表：回填 SQL 把 v1
--     终态行按 entry json tag 名投影成同形态 JSON 灌入（source='backfill'），
--     重放器无差别消化。
--   - 状态机 pending → claimed →（成功 DELETE 行 | 失败退避回 pending |
--     超限 dead）。成功删行故无 'done' 态：done 行无重放意义，还拖累
--     request_id 唯一约束的登记幂等。
--
-- reaper 实现在 internal/sessionv2mirror/replay.go，骨架对齐
-- session_aggregate_outbox_reaper（630）：FOR UPDATE SKIP LOCKED 多副本协作、
-- 指数退避、claim lease 防孤儿、dead 落 slog.Error + Prometheus counter。

BEGIN;

CREATE TABLE IF NOT EXISTS public.session_mirror_outbox (
    id            bigserial PRIMARY KEY,
    -- tenant_id 物化自 payload（entry.TenantID；空落 'default'，与主表
    -- INSERT 侧 nonEmpty 语义一致），供 RLS 与运维检索使用。
    tenant_id     text        NOT NULL DEFAULT 'default',
    request_id    text        NOT NULL,
    session_id    text        NOT NULL,
    source        text        NOT NULL DEFAULT 'hook'
                  CHECK (source IN ('hook', 'backfill')),
    fail_reason   text        NOT NULL DEFAULT '',
    payload       jsonb       NOT NULL,
    status        text        NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'claimed', 'dead')),
    attempts      integer     NOT NULL DEFAULT 0,
    last_error    text,
    next_retry_at timestamptz NOT NULL DEFAULT NOW(),
    claimed_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT NOW(),
    updated_at    timestamptz NOT NULL DEFAULT NOW(),
    -- 回填幂等与 hook 重复失败收敛都锚在 request_id（v1 终态行唯一）。
    CONSTRAINT session_mirror_outbox_unique_request UNIQUE (request_id)
);

CREATE INDEX IF NOT EXISTS idx_session_mirror_outbox_pending
    ON public.session_mirror_outbox (next_retry_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_session_mirror_outbox_dead
    ON public.session_mirror_outbox (created_at DESC)
    WHERE status = 'dead';

-- 租户隔离对齐 630：reaper（super-admin 语义）经 app.bypass_rls 旁路。
ALTER TABLE public.session_mirror_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.session_mirror_outbox FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_session_mirror_outbox ON public.session_mirror_outbox;
CREATE POLICY tenant_isolation_session_mirror_outbox ON public.session_mirror_outbox
    USING (
        tenant_id = get_current_tenant()
        OR current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    )
    WITH CHECK (
        tenant_id = get_current_tenant()
        OR current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    );

COMMIT;

-- 双账本自登记（695-705 定式）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('712', 'storage plan v2 S4 prerequisite GAP-2: session_mirror_outbox durable replay queue for sessionv2mirror shadow write')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

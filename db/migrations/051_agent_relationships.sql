-- 051_agent_relationships.sql — Agent 信任/调用关系 (Q1 2027 D1-1)
--
-- 有向边 (A → B) 描述 agent 间的调用或委托关系. 例如:
--   openclaw-agent (id=1) calls brandmind-go-agent (id=2)
--
-- 与 apihub.asset_relationships 的区别: 本表 agent-specific, 用于:
--   - A2A dispatcher 路由决策 (D2-3)
--   - 跨 agent 编排层 (D3-2 DAG 调度)
--   - 信任管理 (跨租户调用需 a2a_agent_trust 表显式授权, D1 follow-on)
--
-- RLS: 双端都在同一租户才能见 (同 asset_relationships 模式).
-- Idempotent: safe to re-run.

BEGIN;

CREATE TABLE IF NOT EXISTS public.agent_relationships (
    src_agent_id    bigint  NOT NULL,
    dst_agent_id    bigint  NOT NULL,
    rel             text    NOT NULL,
    weight          double precision NOT NULL DEFAULT 1.0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_agent_relationships PRIMARY KEY (src_agent_id, dst_agent_id, rel),
    CONSTRAINT chk_agent_rel CHECK (rel IN (
        'calls',         -- A 调用 B (most common)
        'delegates',     -- A 委托 B 处理某类请求
        'depends_on',    -- A 依赖 B 的某个 capability
        'similar_to'     -- A 和 B 是可替代品 (cost / latency 择优)
    )),
    CONSTRAINT chk_agent_rel_no_self CHECK (src_agent_id <> dst_agent_id),
    CONSTRAINT fk_agent_rel_src FOREIGN KEY (src_agent_id)
        REFERENCES public.agents (id) ON DELETE CASCADE,
    CONSTRAINT fk_agent_rel_dst FOREIGN KEY (dst_agent_id)
        REFERENCES public.agents (id) ON DELETE CASCADE
);

-- Forward traversal (A → ?).
CREATE INDEX IF NOT EXISTS idx_agent_rel_src
    ON public.agent_relationships (src_agent_id);

-- Reverse traversal (? → B).
CREATE INDEX IF NOT EXISTS idx_agent_rel_dst
    ON public.agent_relationships (dst_agent_id);

-- RLS: 双端 tenant 一致才可见.
ALTER TABLE public.agent_relationships ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_agent_relationships ON public.agent_relationships;
CREATE POLICY tenant_isolation_agent_relationships ON public.agent_relationships
    USING (
        EXISTS (
            SELECT 1 FROM public.agents a_src
            WHERE a_src.id = agent_relationships.src_agent_id
              AND (a_src.tenant_id)::text = (public.get_current_tenant())::text
        )
        AND EXISTS (
            SELECT 1 FROM public.agents a_dst
            WHERE a_dst.id = agent_relationships.dst_agent_id
              AND (a_dst.tenant_id)::text = (public.get_current_tenant())::text
        )
    );

COMMIT;
-- 048_apihub_relationships.sql — API Hub topology edges (NOW-1 / A0-3)
--
-- Directed edges (A -> B) between assets in the 047_assets table. Supports
-- the topology view (GET /api/admin/hub/topology) and discovery features.
--
-- RLS: enabled. A relationship is only visible if BOTH endpoints belong to
-- the caller's tenant (the policy joins against the assets table). The Go
-- Service additionally rejects cross-tenant links at the application layer.
--
-- Idempotent: safe to re-run.

BEGIN;

CREATE TABLE IF NOT EXISTS public.asset_relationships (
    src_kind    text    NOT NULL,
    src_ref_id  bigint  NOT NULL,
    dst_kind    text    NOT NULL,
    dst_ref_id  bigint  NOT NULL,
    rel         text    NOT NULL,
    weight      double precision NOT NULL DEFAULT 1.0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT pk_asset_relationships PRIMARY KEY (src_kind, src_ref_id, dst_kind, dst_ref_id, rel),
    CONSTRAINT chk_asset_rel_type CHECK (rel IN ('depends_on', 'calls', 'similar_to')),
    CONSTRAINT fk_asset_rel_src FOREIGN KEY (src_kind, src_ref_id)
        REFERENCES public.assets (kind, ref_id) ON DELETE CASCADE,
    CONSTRAINT fk_asset_rel_dst FOREIGN KEY (dst_kind, dst_ref_id)
        REFERENCES public.assets (kind, ref_id) ON DELETE CASCADE
);

-- Index for forward traversal (A -> ?).
CREATE INDEX IF NOT EXISTS idx_asset_rel_src
    ON public.asset_relationships (src_kind, src_ref_id);

-- Index for reverse traversal (? -> B).
CREATE INDEX IF NOT EXISTS idx_asset_rel_dst
    ON public.asset_relationships (dst_kind, dst_ref_id);

-- RLS: a relationship is visible only if both endpoints belong to the
-- caller's tenant. This mirrors the Go Service's cross-tenant rejection.
ALTER TABLE public.asset_relationships ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_asset_relationships ON public.asset_relationships;
CREATE POLICY tenant_isolation_asset_relationships ON public.asset_relationships
    USING (
        EXISTS (
            SELECT 1 FROM public.assets a_src
            WHERE a_src.kind = asset_relationships.src_kind
              AND a_src.ref_id = asset_relationships.src_ref_id
              AND (a_src.tenant_id)::text = (public.get_current_tenant())::text
        )
        AND EXISTS (
            SELECT 1 FROM public.assets a_dst
            WHERE a_dst.kind = asset_relationships.dst_kind
              AND a_dst.ref_id = asset_relationships.dst_ref_id
              AND (a_dst.tenant_id)::text = (public.get_current_tenant())::text
        )
    );

COMMIT;

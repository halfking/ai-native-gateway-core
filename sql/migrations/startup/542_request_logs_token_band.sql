-- 542_request_logs_token_band.sql
-- 2026-08-19: token-band observability for multi-layer session compression.
--
-- The compression pipeline now classifies the actual outbound body (post
-- session delta-append, post tool-cache, post thinking-strip) into one of
-- three bands so the operator can see when a session has been carrying a
-- large compressed history plus a fresh delta even though the raw request
-- itself was small.
--
--   below       — outbound tokens <= consider threshold (default 200k)
--   preliminary — outbound tokens > consider threshold (no compression fires)
--   forced      — outbound tokens > force threshold (default 400k, forces
--                 compression regardless of model context window)
--
-- The values are also written into compression_meta.token_band for callers
-- that prefer JSONB queries; this dedicated column exists so the
-- admin/compression/stats endpoint can GROUP BY band without an extra
-- extraction in the hot path.
--
-- The column is intentionally nullable: rows written before this migration
-- (and rows where the session compressor was not active) have NULL.

ALTER TABLE public.request_logs
    ADD COLUMN IF NOT EXISTS token_band TEXT;

-- 2026-08-19 hot-table patch: telemetry INSERT path targets request_logs_hot
-- (independent heap table, 0-7 day window) directly — not the parent
-- request_logs. ALTER TABLE on a partitioned parent does NOT propagate to
-- independent hot/archive tables. Add the column explicitly so future
-- deploys that re-run this migration on fresh databases don't reintroduce
-- the bug that produced `column "token_band" does not exist (SQLSTATE 42703)`
-- at 23:32-23:37 on 245 (see changelog 2026-08-19-llmgo-245-followup-a2-a3-a4.md
-- §3 + this changelog §2).
ALTER TABLE public.request_logs_hot
    ADD COLUMN IF NOT EXISTS token_band TEXT;

-- Backfill from existing compression_meta payloads so historical rows are
-- queryable. Idempotent: rows already non-NULL keep their value because
-- the CASE only updates NULL inputs.
UPDATE public.request_logs
   SET token_band = compression_meta->>'token_band'
 WHERE token_band IS NULL
   AND compression_meta ? 'token_band'
   AND compression_meta->>'token_band' IN ('below','preliminary','forced');

UPDATE public.request_logs_hot
   SET token_band = compression_meta->>'token_band'
 WHERE token_band IS NULL
   AND compression_meta ? 'token_band'
   AND compression_meta->>'token_band' IN ('below','preliminary','forced');

-- Partial index: only rows that landed in any band (the cheap filter for
-- "show me forced compressions last hour" admin queries). Most rows have
-- NULL and never enter the index.
-- 2026-10-01 fresh-install e2e 修订（802 形态裁决同款处方）：原文件按
-- 特定环境当时存在的分区（default/2026_09/2026_10）硬编码 ON ONLY 母表
-- + 叶索引 + ATTACH——分区由运行期 ensure tick 按月滚动创建，全新安装
-- 上这些叶索引的目标永远不存在，序列必炸（e2e 实证 42P01）。改为递归
-- 形态（对齐 525/802 惯例）：对既有分区自动建叶并挂载（原目标环境终态
-- 相同且 indisvalid=true），对未来分区由 PG 自动继承。
CREATE INDEX IF NOT EXISTS idx_request_logs_token_band_ts
    ON public.request_logs (token_band, ts DESC)
    WHERE token_band IS NOT NULL;

-- CHECK constraint locks the value space so an out-of-band string from a
-- regression cannot poison GROUP BY aggregates.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_request_logs_token_band'
          AND conrelid = 'public.request_logs'::regclass
    ) THEN
        ALTER TABLE public.request_logs
            ADD CONSTRAINT chk_request_logs_token_band
            CHECK (token_band IS NULL OR token_band IN ('below','preliminary','forced'));
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_request_logs_hot_token_band'
          AND conrelid = 'public.request_logs_hot'::regclass
    ) THEN
        ALTER TABLE public.request_logs_hot
            ADD CONSTRAINT chk_request_logs_hot_token_band
            CHECK (token_band IS NULL OR token_band IN ('below','preliminary','forced'));
    END IF;
END$$;

-- 2026-08-19 hot-table patch: same partial index for the hot table so the
-- admin/compression/stats endpoint GROUP BY band query is also fast on the
-- 0-7 day window.
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_token_band_ts
    ON public.request_logs_hot (token_band, ts DESC)
    WHERE token_band IS NOT NULL;

COMMENT ON COLUMN public.request_logs.token_band IS
    '2026-08-19: classification of the actual outbound body after session delta-append + tool cache + thinking strip. below / preliminary / forced. Mirrors compression_meta.token_band but queryable directly. See domains/hooks/compression/window.go (OutboundTokenBand).';

COMMENT ON COLUMN public.request_logs_hot.token_band IS
    '2026-08-19 hot-table mirror of request_logs.token_band. Added by 542 hot-table patch to fix the SQLSTATE 42703 errors observed on 245 telemetry INSERT path.';

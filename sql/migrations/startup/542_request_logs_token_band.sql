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

-- Backfill from existing compression_meta payloads so historical rows are
-- queryable. Idempotent: rows already non-NULL keep their value because
-- the CASE only updates NULL inputs.
UPDATE public.request_logs
   SET token_band = compression_meta->>'token_band'
 WHERE token_band IS NULL
   AND compression_meta ? 'token_band'
   AND compression_meta->>'token_band' IN ('below','preliminary','forced');

-- Partial index: only rows that landed in any band (the cheap filter for
-- "show me forced compressions last hour" admin queries). Most rows have
-- NULL and never enter the index.
CREATE INDEX IF NOT EXISTS idx_request_logs_token_band_ts
    ON ONLY public.request_logs (token_band, ts DESC)
    WHERE token_band IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_request_logs_token_band_ts_default
    ON public.request_logs_default (token_band, ts DESC)
    WHERE token_band IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_request_logs_token_band_ts_2026_09
    ON public.request_logs_2026_09 (token_band, ts DESC)
    WHERE token_band IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_request_logs_token_band_ts_2026_10
    ON public.request_logs_2026_10 (token_band, ts DESC)
    WHERE token_band IS NOT NULL;

ALTER INDEX idx_request_logs_token_band_ts
    ATTACH PARTITION idx_request_logs_token_band_ts_default;

ALTER INDEX idx_request_logs_token_band_ts
    ATTACH PARTITION idx_request_logs_token_band_ts_2026_09;

ALTER INDEX idx_request_logs_token_band_ts
    ATTACH PARTITION idx_request_logs_token_band_ts_2026_10;

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
END$$;

COMMENT ON COLUMN public.request_logs.token_band IS
    '2026-08-19: classification of the actual outbound body after session delta-append + tool cache + thinking strip. below / preliminary / forced. Mirrors compression_meta.token_band but queryable directly. See domains/hooks/compression/window.go (OutboundTokenBand).';

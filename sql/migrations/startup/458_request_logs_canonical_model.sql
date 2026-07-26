-- Migration 458: Add canonical_model text to request_logs + request_logs_hot
--
-- 2026-07-27 Boss directive: "实时请求流的按模型过滤时，需要匹配标准模型。
--                          因此要检查请求流中的请求数据，一定要有标准模型名称属性和值。"
--
-- Background:
--   - request_logs.client_model holds what the client wrote in `model: "..."`
--     (raw, may include vendor prefix, dates, casing like "GPT-4O" or
--     "MiniMax-M3" — not standardized).
--   - request_logs.outbound_model is the resolved/raw model sent upstream
--     after routing (may carry vendor prefix like "z-ai/glm-5.2").
--   - request_logs.canonical_id is the FK into models_canonical.id.
--     The standard name lives at models_canonical.canonical_name.
--   - Until now, every analytics / dashboard query joins
--     `models_canonical mc ON mc.id = rl.canonical_id` to obtain the
--     standard name, falling back via COALESCE chain:
--       COALESCE(NULLIF(mc.canonical_name, ''),
--                NULLIF(rl.client_model, ''),
--                rl.outbound_model, '')
--     This works but:
--       (a) is expensive on hot table (every GROUP BY needs the JOIN)
--       (b) produces stale data if models_canonical.canonical_name is
--           renamed (FK keeps old id, but our COALESCE picks the new name —
--           which is fine but the rename audit loses traceability).
--
-- 2026-07-16 prior fix already standardized the live-stream SSE path
-- (admin/live_stream_sse.go:1702 LiveRequestFromTelemetry uses the same
-- COALESCE chain and caches via sync.Map). This migration extends the
-- standardization to the request_logs table itself by denormalizing the
-- canonical name into a dedicated text column.
--
-- Schema reality:
--   - request_logs is partitioned BY RANGE (ts); PostgreSQL 11+ propagates
--     ADD COLUMN to existing partitions automatically (no need to enumerate).
--   - request_logs_hot is the heap hot table (0–7d window); needs its own ADD.
--   - Column is NULLABLE so historical rows survive the migration; the
--     application writes it from the canonical resolution path that
--     already runs at INSERT time (modelResolution.CanonicalName).
--
-- This migration is idempotent (ADD COLUMN IF NOT EXISTS).

BEGIN;

-- ─── 1. Add canonical_model text to partitioned parent table ───
-- PostgreSQL propagates this to all existing monthly partitions
-- (request_logs_2026_07, request_logs_2026_08, etc.) automatically.
ALTER TABLE request_logs
    ADD COLUMN IF NOT EXISTS canonical_model TEXT;
COMMENT ON COLUMN request_logs.canonical_model IS
    'Standard/canonical model name (lowercase, models_canonical.canonical_name). '
    'Denormalized for cheap GROUP BY / filter without joining models_canonical. '
    'NULL when modelResolution did not match a canonical row.';

-- ─── 2. Add the same column to the hot table ───
ALTER TABLE request_logs_hot
    ADD COLUMN IF NOT EXISTS canonical_model TEXT;
COMMENT ON COLUMN request_logs_hot.canonical_model IS
    'Standard/canonical model name (lowercase). See request_logs.canonical_model.';

-- ─── 2b. Client-perception columns on request_logs_hot ───
-- 2026-07-27 (audit fix): commit 26d676ba updated client.go to INSERT
-- agent_name / agent_type / client_protocol / virtual_client_id into
-- request_logs_hot and to COALESCE them in ON CONFLICT DO UPDATE. But the
-- only prior migration that added those columns was 443_observability_fields.sql,
-- which targeted the partitioned request_logs PARENT only — not the
-- request_logs_hot heap table that the INSERT actually writes to. Without
-- these ALTERs every request_logs_hot INSERT would fail at runtime with
-- SQLSTATE 42703 "column ... does not exist", the same P0 class of bug as
-- the 2026-07-13 multimodal-token-fields-hot incident. They are added here
-- (not in a new migration) so that the existing 458 down.sql, which already
-- DROPs them, stays a correct inverse of this up.sql.
-- Idempotent (ADD COLUMN IF NOT EXISTS). NULLABLE so historical rows survive.
ALTER TABLE request_logs_hot
    ADD COLUMN IF NOT EXISTS agent_name       VARCHAR(255),
    ADD COLUMN IF NOT EXISTS agent_type       VARCHAR(50),
    ADD COLUMN IF NOT EXISTS client_protocol  VARCHAR(50),
    ADD COLUMN IF NOT EXISTS virtual_client_id VARCHAR(64);
COMMENT ON COLUMN request_logs_hot.agent_name IS
    'Agent/application name (claude-code/cursor/curl/...). Mirrors request_logs.agent_name.';
COMMENT ON COLUMN request_logs_hot.agent_type IS
    'Agent type: web/cli/api/bot/mobile/unknown. Mirrors request_logs.agent_type.';
COMMENT ON COLUMN request_logs_hot.client_protocol IS
    'Client protocol (openai-chat/anthropic-messages/gemini-generate). Mirrors request_logs.client_protocol.';
COMMENT ON COLUMN request_logs_hot.virtual_client_id IS
    'Stable virtual client id ("vc-" + hash[:16]). Mirrors request_logs.virtual_client_id.';

-- ─── 3. Backfill hot table from existing canonical_id join ───
-- Idempotent: only updates rows where canonical_model IS NULL.
UPDATE request_logs_hot rl
SET canonical_model = mc.canonical_name
FROM models_canonical mc
WHERE rl.canonical_id = mc.id
  AND rl.canonical_model IS NULL;

-- ─── 4. Backfill partitioned parent (cascades to all partitions) ───
-- Same idempotent guard: only touch rows where the value is missing.
UPDATE request_logs rl
SET canonical_model = mc.canonical_name
FROM models_canonical mc
WHERE rl.canonical_id = mc.id
  AND rl.canonical_model IS NULL;

-- ─── 5. Indexes for hot + parent (dashboard filter speedup) ───
-- Partial index because most rows will have a value but some legacy
-- rows may not (NULL skip), keeping the index small and tight.
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_canonical_model_ts
    ON request_logs_hot (canonical_model, ts DESC)
    WHERE canonical_model IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_request_logs_canonical_model_ts
    ON request_logs (canonical_model, ts DESC)
    WHERE canonical_model IS NOT NULL;

COMMIT;
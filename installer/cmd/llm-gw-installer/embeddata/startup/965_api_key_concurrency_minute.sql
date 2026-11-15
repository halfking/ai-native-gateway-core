-- ===========================================================================
-- File:          965_api_key_concurrency_minute.sql
-- Migration:     965
-- Purpose:       per-key 并发实时观测——「每个 key 的实时并发数据按分钟落库」
--                （2026-11-15 三点审计轮：apikey 并发限制三点审计的第③点）。
--
--                ★ 背景：api_keys.rate_limit_concurrent 已是真执法
--                  （streaming.KeyConcurrentGate，429 标准信封），但执法状态
--                  只活在进程内存——「key 10 现在在飞多少/这分钟峰值多少/
--                  被拒多少」在闸外不可见。运维要回答「key 是否被并发帽
--                  卡住/帽该调多大」必须有按分钟的实测数据，而不是猜。
--
--                ★ 写入方只有 bg.KeyConcurrencyStatsWriter（间隔
--                  LLM_GATEWAY_KEY_CONCURRENCY_STATS_INTERVAL，默认 60s；
--                  样本来自 KeyConcurrentGate.SnapshotAndResetMinute——
--                  in_flight=采样时刻实时在飞、peak_in_flight=窗口内峰值、
--                  rejections=窗口内 429 次数、concurrent_limit=生效帽）。
--
--                ★ 空窗口零写：窗口内无活动的 key 不产生行——零行本身即
--                  信息（该 key 该分钟零并发），表保持极小。
--                ★ instance_id 进主键：蓝绿双槽并行期两实例各写各的行
--                  （进程内计数 per-instance）；聚合读面按
--                  (api_key_id, minute_ts) SUM/取 max 即得全域视图。
--                ★ retention：bg worker 每小时清理超期行（默认 7 天，
--                  LLM_GATEWAY_KEY_CONCURRENCY_RETENTION 可调）。分钟级并发
--                  观测没有长期价值（用量归 request_stats_minute 族管）。
--
-- Down: 965_api_key_concurrency_minute.down.sql
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS public.api_key_concurrency_minute (
    minute_ts        timestamptz NOT NULL,
    api_key_id       bigint NOT NULL,
    instance_id      text NOT NULL DEFAULT '',
    in_flight        int NOT NULL DEFAULT 0,
    peak_in_flight   int NOT NULL DEFAULT 0,
    rejections       int NOT NULL DEFAULT 0,
    concurrent_limit int NOT NULL DEFAULT 0,
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (minute_ts, api_key_id, instance_id)
);

COMMENT ON TABLE public.api_key_concurrency_minute IS
    'Per-minute per-key realtime concurrency observations (965, 2026-11-15 audit round). Writer = bg.KeyConcurrencyStatsWriter only (samples from streaming.KeyConcurrentGate: in_flight snapshot, within-window peak, 429 rejections, explicit cap in force). instance_id separates blue/green slots — aggregate with SUM(in_flight)/MAX(peak_in_flight) over (api_key_id, minute_ts) for the fleet-wide view. Idle minutes produce no rows (zero row = zero concurrency). Retention trimmed by the writer (default 7d).';
COMMENT ON COLUMN public.api_key_concurrency_minute.concurrent_limit IS
    'Explicit api_keys.rate_limit_concurrent cap in force at sample time (0 = no explicit cap; the gate is explicit-only since 2026-11-15).';
COMMENT ON COLUMN public.api_key_concurrency_minute.rejections IS
    'Count of 429 key_concurrent_limit rejections for this key within this minute window (same-minute repeated windows accumulate via UPSERT).';

CREATE INDEX IF NOT EXISTS idx_akcm_key_minute
    ON public.api_key_concurrency_minute (api_key_id, minute_ts DESC);

-- ── 记账（≥952 自登记纪律，门 TestMigrationRegistryStaysContinuous / 自登记门）──
INSERT INTO public.schema_migrations (version, description)
VALUES ('965', 'api_key_concurrency_minute: per-minute per-key realtime concurrency observations (in-flight snapshot, window peak, 429 rejections, cap in force) written by bg.KeyConcurrencyStatsWriter from the KeyConcurrentGate; instance_id separates blue/green slots; retention default 7d')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;

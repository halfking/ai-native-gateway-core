-- ===========================================================================
-- File:          sql/migrations/startup/817_ursm_snapshot_typed_columns.sql
-- Migration:     817
-- Database:      llm_gateway
-- Purpose:       ursm_node_snapshot_min.payload 字段拆分 —— 31 个 hash 键
--                提升为 typed 列，payload 退化为「未知字段的前向兼容仓」
--                并剔除 7 个与现有列纯重复的键。
--
-- 动机（252 生产实测，2026-10-02）：
--   - 该表 22 GB 占库 56%，日增 860 MB，**运行时零读方**（全仓检索仅
--     persist.Writer 的 INSERT、retention 的 DELETE、一个已执行的
--     09-20 一次性脚本，无任何 SELECT）。
--   - payload 占 heap 的 **66%**：872 MB/天，292 B/行（表 heap
--     8,768 MB / 19.9M 行 = 441 B/行）。
--   - payload 实有 31 个键。其中 **7 个是行内 typed 列的纯重复**：
--     available / generation / source_priority / fail_streak /
--     sr_1m / sr_5m / sr_30m —— 全部已在表内有同名列，且在 payload 中
--     又以 JSON 字符串再存一遍（"generation":"43" 而列是 bigint）。
--     重复部分合计 70.8 B/行 = payload 的 24%。
--
-- 设计取舍（**不是**「把 payload 拆干净」）：
--   persist/writer.go:167-169 的原注释写明 payload 是**故意**保留全量
--   hash 的 ——「以防未来需要恢复其他字段（pricing, concurrency, etc.）」。
--   hash 来自 Redis HGETALL，是 map[string]string，**schema 会演进**。
--   若把 31 个键硬编码成列而不留 payload，hash 新增字段时该数据就永久丢失。
--   ⇒ 本迁移的形态是：**已知字段全部 typed 化（可查询、可索引、不再付
--   JSON 字符串税），payload 仅保留 typed 列覆盖不到的键**。
--   前向兼容承诺被保留，而不是被牺牲。
--
-- **刻意不回填历史行**：
--   - 20M 行全表 UPDATE 会重写整个 10 GB heap，产生等量 bloat，
--     需再跑一次 VACUUM FULL（另需 ~10 GB 临时空间 + 数小时锁窗口）。
--   - 收益并不会因此提前：保留期已改为 7 天（URSM_SNAPSHOT_RETENTION_DAYS），
--     历史行最迟 7 天内自然退休，**收益随数据滚出自动实现**。
--   - 新列从 NULL 起，对历史行语义正确（「该时刻未采集」而非「采集到空」）。
--   ⇒ ADD COLUMN 全部 nullable 且无 DEFAULT，PG 11+ 不触发重写。
-- ===========================================================================
BEGIN;

-- --- epoch 毫秒与序号（10 列）---------------------------------------------
-- 原始形态是 13 位十进制字符串（jsonb_each_text 实测 avg_len 13.0），
-- 落成 bigint 省去引号 + 键名 + JSON 标量开销。
ALTER TABLE public.ursm_node_snapshot_min
  ADD COLUMN IF NOT EXISTS updated_at_ms            bigint,
  ADD COLUMN IF NOT EXISTS last_probe_at_ms         bigint,
  ADD COLUMN IF NOT EXISTS last_probe_latency_ms    bigint,
  ADD COLUMN IF NOT EXISTS last_attempt_ms          bigint,
  ADD COLUMN IF NOT EXISTS last_ok_ms               bigint,
  ADD COLUMN IF NOT EXISTS last_request_at_ms       bigint,
  ADD COLUMN IF NOT EXISTS last_request_error_at_ms bigint,
  ADD COLUMN IF NOT EXISTS manual_at_ms             bigint,
  ADD COLUMN IF NOT EXISTS cool_until_ms            bigint,
  ADD COLUMN IF NOT EXISTS event_seq                bigint;

-- --- 布尔标志（3 列，实测取值恒为 "0"/"1"，distinct = 2）------------------
ALTER TABLE public.ursm_node_snapshot_min
  ADD COLUMN IF NOT EXISTS disabled                 boolean,
  ADD COLUMN IF NOT EXISTS last_direct_ok           boolean,
  ADD COLUMN IF NOT EXISTS manual_hold              boolean;

-- --- 计数（3 列）----------------------------------------------------------
ALTER TABLE public.ursm_node_snapshot_min
  ADD COLUMN IF NOT EXISTS success_count             integer,
  ADD COLUMN IF NOT EXISTS failure_count            integer,
  ADD COLUMN IF NOT EXISTS disable_count            integer;

-- --- 比率 / 延迟均值（3 列）-----------------------------------------------
ALTER TABLE public.ursm_node_snapshot_min
  ADD COLUMN IF NOT EXISTS lat_ewma_ms              real,
  ADD COLUMN IF NOT EXISTS empty_response_rate_1m   real,
  ADD COLUMN IF NOT EXISTS empty_response_rate_30m  real;

-- --- 文本（5 列，均为低基数：distinct 1~17）------------------------------
ALTER TABLE public.ursm_node_snapshot_min
  ADD COLUMN IF NOT EXISTS last_err                 text,
  ADD COLUMN IF NOT EXISTS manual_reason            text,
  ADD COLUMN IF NOT EXISTS manual_actor             text,
  ADD COLUMN IF NOT EXISTS disabled_reason          text,
  ADD COLUMN IF NOT EXISTS cool_reason              text;

COMMENT ON COLUMN public.ursm_node_snapshot_min.updated_at_ms IS
  '817: 由 payload->>''updated_at_ms'' 提升。原为 13 位十进制字符串。';
COMMENT ON COLUMN public.ursm_node_snapshot_min.last_err IS
  '817: 由 payload->>''last_err'' 提升。实测 17 个取值，是该表最常被人工排查引用的字段。';
COMMENT ON COLUMN public.ursm_node_snapshot_min.payload IS
  '817 起语义收窄：仅保留**未被 24 个 typed 列覆盖**的 hash 键（writer.go 已剔除 '
  'available/generation/source_priority/fail_streak/sr_1m/sr_5m/sr_30m 七个重复键）。'
  '保留 payload 的目的是保住 hash schema 演进时的前向兼容（见迁移头注释）。'
  '历史行的 payload 仍是全量 —— 本迁移刻意不回填（理由见迁移头注释）。';

COMMIT;

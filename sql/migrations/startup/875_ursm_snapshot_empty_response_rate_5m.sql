-- ===========================================================================
-- File:          sql/migrations/startup/875_ursm_snapshot_empty_response_rate_5m.sql
-- Migration:     875
-- Database:      llm_gateway
-- Purpose:       补齐 ursm_node_snapshot_min 的一个漏网键 ——
--                empty_response_rate_5m 提升为 typed 列。
--
-- 动机（2026-10-18 §10.110.62/§10.110.65 生产只读实测）：
--
--   818 把 31 个 hash 键提升为 typed 列，但 `empty_response_rate_5m` 被漏掉了：
--
--     empty_response_rate_1m   → typed 列（818 第 70 行）
--     empty_response_rate_30m  → typed 列（818 第 71 行）
--     empty_response_rate_5m   → ✗ 仍在 payload 里
--
--   实测该键由**同一批 270 行（15%）**携带，另 1531 行（85%）带空 `{}`；
--   生产侧每轮 payload 合计 65,274 B，占整行 324 B 的 11.7%。
--   而 store/pipeline.go:132 一直在读它 —— 也就是说一个被生产代码读取的字段，
--   在快照表里**只能靠 payload->>'...' 取**，走不了索引、不受类型检查。
--
-- 为什么这道门没抓到：`TestPayloadDuplicateKeysCoverAllTypedHashKeys` 的
-- 全集是 writer.go 自己的 hash["..."] 读取，"store 读了而 writer 没读"
-- 的键对它结构性不可见。875 随附补上这个方向（§10.110.62）。
--
-- 设计取舍（与 818 一致）：
--   - 同一个值从 jsonb 挪到 real 列，**不新增任何数据**；
--   - 刻意**不回填历史行**：全表 UPDATE 会重写整个 heap、产生等量 bloat
--     （与 818 同一取舍）。历史行该列保持 NULL，与另两个兄弟一致 ——
--     另两个兄弟当年同样没被回填。
--   - 列可空：与 empty_response_rate_1m / _30m 相同形态。
--
-- 并发安全（§44 / 873 / 874 同形）：
--   ADD COLUMN 走 ACCESS EXCLUSIVE，但**可空且无默认值** ⇒ PG 只改目录，
--   不重写 heap、不逐行回填（这也是「刻意不回填历史行」能成立的原因）。
--   代价全部在**拿锁那一瞬**：分区父表上要级联到 9 个分区。
--     ① lock_timeout = 2s —— 抢不到就快速失败，不排在写流量后面
--        （表上有每分钟 1,800 行的 persist 写入，排队等锁能等到分钟级）；
--     ② statement_timeout = 60s —— 显式写出，不依赖共享 PG 的服务端默认值。
--   失败是可重试的：未拿到锁 ⇒ 事务回滚 ⇒ **什么都没发生**，稍后重跑即可。
-- ===========================================================================
BEGIN;

SET LOCAL statement_timeout = '60s';
SET LOCAL lock_timeout = '2s';

ALTER TABLE public.ursm_node_snapshot_min
  ADD COLUMN IF NOT EXISTS empty_response_rate_5m real;

COMMENT ON COLUMN public.ursm_node_snapshot_min.empty_response_rate_5m IS
  '875: 由 payload->>''empty_response_rate_5m'' 提升。818 漏排 —— 其两个兄弟 '
  'empty_response_rate_1m / _30m 当时已是 typed 列。store/pipeline.go:132 读它。'
  '刻意不回填历史行（与 818 同取舍），故本列对历史行恒为 NULL。';

-- payload 的注释同步收窄：818 的措辞里写死了「24 个 typed 列」，
-- 875 之后是 25 个。不改会让下一个读注释的人算错 payload 里残留的键数。
COMMENT ON COLUMN public.ursm_node_snapshot_min.payload IS
  '818 起语义收窄：仅保留**未被 typed 列覆盖**的 hash 键（writer.go 的 '
  'payloadDuplicateKeys 逐条登记，共 41 项，含 875 新增的 '
  'empty_response_rate_5m）。保留 payload 的目的是保住 hash schema '
  '演进时的前向兼容（见 818 迁移头注释）。历史行的 payload 仍是全量 —— '
  '818 与 875 均刻意不回填。';

COMMIT;

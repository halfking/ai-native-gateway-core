-- 746 down: 回退内部对帐维度补齐。
--
-- 仅删除本轮新增的三列；tenant_id 的 text→bigint 反向转换**有意不做**：
-- text ⊃ bigint，已写入 'default' 等非数字租户键后反向 cast 必然失败，
-- 强转等于丢数据。需要完整回退到 745 前状态时走 745 down（DROP TABLE）。

ALTER TABLE report_snapshots
    DROP COLUMN IF EXISTS credits_charged,
    DROP COLUMN IF EXISTS latency_p50_ms,
    DROP COLUMN IF EXISTS latency_p95_ms;

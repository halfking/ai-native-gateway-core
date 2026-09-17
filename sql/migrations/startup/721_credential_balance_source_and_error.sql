-- Migration 721: credential balance provenance (source) + last probe error.
--
-- Feature (2026-09-18): 让 /providers 凭据抽屉能区分"余额是 API 探测回来的
-- 还是操作员手工填的"，并在厂商余额端点故障时把失败原因展示到 UI。
--
-- 1) balance_source
--    'manual'  — 操作员经 PATCH credentials 显式写入（updateCredential）。
--                bg/balance_floor_guard.refreshBalance 与
--                bg/credential_probe_v2.probeBalance 在 24h 保护期内跳过
--                此类行，避免自动探测覆盖人工校准值。
--    'api'     — 余额 API 探测成功写入（refresh-balance 端点 / floor guard
--                Pass A / probe_v2 cycleAll）。
--    NULL      — 历史行（迁移前写入）保持 NULL，语义同 'api'（不参与保护）。
--
-- 2) balance_error
--    最近一次余额探测失败的错误摘要（截断 500 字符）；成功时置 NULL。
--    仅由探测路径写入，PATCH 手工输入同样清空它。
--
-- Compatibility: 纯 ADD COLUMN IF NOT EXISTS，对既有行零回填、零锁风险
-- （credentials 是热表，与 701/720 同样的元数据级变更）。

ALTER TABLE credentials ADD COLUMN IF NOT EXISTS balance_source text;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS balance_error text;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'credentials_balance_source_chk'
          AND conrelid = 'credentials'::regclass
    ) THEN
        ALTER TABLE credentials ADD CONSTRAINT credentials_balance_source_chk
            CHECK (balance_source IS NULL OR balance_source IN ('manual', 'api'));
    END IF;
END $$;

COMMENT ON COLUMN credentials.balance_source IS
    '余额来源: manual=操作员手工输入(24h 内自动探测不覆盖), api=厂商余额 API 探测, NULL=迁移前历史行';
COMMENT ON COLUMN credentials.balance_error IS
    '最近一次余额探测失败的错误摘要(≤500字符), 成功时为 NULL';

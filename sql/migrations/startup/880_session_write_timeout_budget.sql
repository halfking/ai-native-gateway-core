-- 880_session_write_timeout_budget.sql
-- 2026-10-20：把 session 写路径的**超时预算**在所有环境上对齐到 2000ms。
--
-- -----------------------------------------------------------------------
-- 为什么要一条迁移（而不是只改代码里的默认值）
-- -----------------------------------------------------------------------
-- 缺陷本体（不是推断，是实测）：
--
-- ① `internal/sessionv2mirror/hook.go` 的注释写着「raise the default budget
--    to 2000ms」，它把常量 `defaultShadowWriteTimeoutMs` 从 500 提到了 2000。
--    **但那次修复从未生效。**
--
-- ② `settings.GetPlatformInt(key, fallback)` 的解析顺序是
--       **DB → env → spec.Default → 代码里的 fallback**
--    （`settings/helpers.go:getPlatformInt` → `Registry.EffectiveValue`）。
--    `spec.Default` 命中时，代码里那个 2000 **永远走不到**。
--    而 `settings/spec_sessions_v2.go` 里这条的 `Default: 500` 一直没改
--    ⇒ 实际预算一直是 **500ms**。
--
-- ③ 500ms 撑不住一次 shadow write 的串行步骤：
--       session_dim 维度写（其触发器还要 UPDATE session_summaries）
--       → session_turns → session_turn_details → session_bodies
--       → session_summaries
--    实测（本地 PG 17.10）：细节层写超时 ⇒
--       ① 正文（`session_bodies.request_delta/response_delta/outbound_body`）
--          **一行都不落**（`jsonb_typeof` 读数是 `null`，不是 `array`）；
--       ② 整笔写失败 ⇒ 走 outbox 异步重放 ⇒ **重放顺序 ≠ 请求顺序**
--          ⇒ `turn_no` 与时间序错位（实测：第 1 轮拿到 `turn_no=3`）；
--       ③ trace 找不到父行 ⇒ `trace_events` 丢失。
--    ⇒ 全是**静默**失效：接口返回 200、日志只有 WARN。
--
-- ④ 同一轮还查出一个叠加根因（已在本机修好，属环境不属于迁移）：
--    本机 PG 的 `shared_buffers` 被手改成 400kB ⇒ 每条语句**规划** ~1s。
--    规划慢 ⇒ 写路径在毫秒级 deadline 内超时。**改完这条迁移而不改那个，
--    症状依旧。**（本机已 ALTER SYSTEM 到 2GB；生产 154/245 是 3.5GB。）
--
-- -----------------------------------------------------------------------
-- 这条迁移做什么（以及刻意不做什么）
-- -----------------------------------------------------------------------
-- 做：**把平台上「已经被持久化过、且偏小」的预算抬到 2000**。
--
-- 刻意不做：
--   · **不覆盖偏大的值**（运维手工调大过的 3000/5000 保留）——
--     判据是「抬到 2000」，不是「无条件设成 2000」。
--   · **不写死 DB 里的值当唯一真相**：`settings/spec_sessions_v2.go` 的
--     `Default` 已同步改成 2000，本迁移是给**已经持久化过旧值**的环境兜底。
--     两处的一致性由 `settings/session_write_timeout_default_test.go` 钉住。
--
-- 幂等：重复执行不会改变已达 2000 的行。

BEGIN;

-- 1) 抬小值：已持久化过、且小于 2000 的行。
UPDATE settings_kv
   SET value       = to_jsonb(2000::int),
       value_type  = 'int',
       updated_by  = 'migration-880',
       updated_at  = now(),
       prev_value  = value,
       prev_updated_at = now()
 WHERE key = 'sessions_v2.write_timeout_ms'
   AND (value #>> '{}')::int < 2000;

-- 2) 不存在则补上（让各环境显式同标准，而不是各靠各自的 spec 默认）。
INSERT INTO settings_kv (key, value, value_type, scope, category, updated_by, updated_at)
VALUES ('sessions_v2.write_timeout_ms', to_jsonb(2000::int), 'int', 'platform', 'session',
        'migration-880', now())
ON CONFLICT (key) DO NOTHING;

-- 3) 后置条件：最终值必须 ≥ 2000。
DO $guard_880$
DECLARE
    v_val INT;
BEGIN
    SELECT (value #>> '{}')::int INTO v_val
      FROM settings_kv WHERE key = 'sessions_v2.write_timeout_ms';
    IF v_val IS NULL THEN
        RAISE EXCEPTION '880: sessions_v2.write_timeout_ms missing after upsert';
    END IF;
    IF v_val < 2000 THEN
        RAISE EXCEPTION '880: sessions_v2.write_timeout_ms = % < 2000', v_val;
    END IF;
END
$guard_880$;

COMMIT;

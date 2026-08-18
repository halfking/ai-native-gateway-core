-- ============================================================================
-- 2026-08-19: Clean up pseudo-success rows in node_probe_state
-- ============================================================================
--
-- 背景 (docs/handoff/2026-08-18-global-routing-audit-handoff.md §6.1):
--   修复前 bg/credential_recovery.go:443-485 的 30s 循环每 30s 直接写
--   last_direct_ok=TRUE / last_gateway_ok=TRUE / next_retry_at=now()+1h,
--   并清空 last_err_code / last_err_detail。现场 126 行凭证中 82 行处于
--   这种伪成功/未来状态, 51 行被后续恢复 SQL 改写。
--
-- 修复 (commit 8ebaee0b2):
--   - 删除了 443-485 行的无条件 UPDATE 整段。
--   - 改为 reconcileStaleNodeProbeStates() 走真实 durable probe enqueue。
--   - ProbeService.Run 跑出真实 direct+gateway 双轮成功后才通过
--     mirrorNodeProbeState 写回 last_direct_ok/last_gateway_ok/next_retry_at。
--
-- 本脚本目标:
--   清理已经被旧代码污染的现场行, 让新的代码路径自然重新探测。
--   不会删除任何行 (DELETE), 只会把污染字段清回 NULL/FALSE/now(),
--   让 reconcileStaleNodeProbeStates 看到这些行进入真实探测路径。
--
-- ============================================================================
-- 前置检查 (必须先执行, 确认命中数和范围)
-- ============================================================================

-- 1) 命中数 (与交付文档"82 行"对账)
SELECT COUNT(*) AS pseudo_success_rows
FROM node_probe_state
WHERE last_direct_ok = TRUE
  AND last_gateway_ok = TRUE
  AND next_retry_at > now() + interval '5 minutes';

-- 2) 按 credential 聚合 (确认范围符合预期)
SELECT credential_id, COUNT(*) AS cnt
FROM node_probe_state
WHERE last_direct_ok = TRUE
  AND last_gateway_ok = TRUE
  AND next_retry_at > now() + interval '5 minutes'
GROUP BY credential_id
ORDER BY cnt DESC
LIMIT 20;

-- 3) 当前 paused 状态 (避免误清)
SELECT paused, COUNT(*)
FROM node_probe_state
WHERE last_direct_ok = TRUE
  AND last_gateway_ok = TRUE
  AND next_retry_at > now() + interval '5 minutes'
GROUP BY paused;

-- 4) 备份 (强烈建议先执行; 若已有近 1h 物理备份可跳过)
CREATE TABLE IF NOT EXISTS node_probe_state_pseudo_success_backup_20260819 AS
SELECT *
FROM node_probe_state
WHERE last_direct_ok = TRUE
  AND last_gateway_ok = TRUE
  AND next_retry_at > now() + interval '5 minutes';

SELECT COUNT(*) AS backup_rows
FROM node_probe_state_pseudo_success_backup_20260819;

-- ============================================================================
-- 清理 (在确认上面备份与命中数符合预期后, 单独执行本段)
-- ============================================================================
--
-- 安全保证:
--   - 不删除任何行 (DELETE), 只重置污染字段。
--   - 不动 paused / consecutive_failures / last_attempt_at /
--     last_run_id / in_flight_until, 这些字段是 probe worker 真实写入的。
--   - 把 last_direct_ok / last_gateway_ok 从 TRUE 清回 NULL
--     (ProbeService mirrorNodeProbeState 写入时这两个字段可以为 NULL,
--      只有真实探测成功才写 TRUE, 这是新的状态机契约)。
--   - 把 next_retry_at 重置为 now(), 触发 reconcileStaleNodeProbeStates
--     在下一个 30s tick 入队真实探针。
--   - 把 next_retry_seconds 重置为 5 (7-step backoff ladder 起点)。
--   - 把 last_err_code / last_err_detail 清空 (原 UPDATE 也是清空,
--     新代码不依赖这两个字段做路由决策, 仅做运维排障)。
--   - updated_at 同步刷新, 让审计字段一致。
--
-- 注意: 不开事务 (避免长事务阻塞在线流量); 走小批次循环,
-- 每批 1000 行, 用 SKIP LOCKED 避免与 probe worker 冲突。

DO $$
DECLARE
    batch_size INT := 1000;
    affected INT := 0;
    total INT := 0;
BEGIN
    LOOP
        WITH targets AS (
            SELECT ctid
            FROM node_probe_state
            WHERE last_direct_ok = TRUE
              AND last_gateway_ok = TRUE
              AND next_retry_at > now() + interval '5 minutes'
            ORDER BY ctid
            LIMIT batch_size
            FOR UPDATE SKIP LOCKED
        )
        UPDATE node_probe_state nps
        SET last_direct_ok     = NULL,
            last_gateway_ok    = NULL,
            next_retry_at      = now(),
            next_retry_seconds = 5,
            last_err_code      = NULL,
            last_err_detail    = NULL,
            updated_at         = now()
        FROM targets
        WHERE nps.ctid = targets.ctid;

        GET DIAGNOSTICS affected = ROW_COUNT;
        total := total + affected;

        EXIT WHEN affected = 0;

        RAISE NOTICE 'pseudo_success_cleanup: batch=% rows=%', total, affected;

        PERFORM pg_sleep(0.05);
    END LOOP;

    RAISE NOTICE 'pseudo_success_cleanup: total rows reset = %', total;
END $$;

-- ============================================================================
-- 后置验证
-- ============================================================================

-- 5) 命中数应已清零
SELECT COUNT(*) AS remaining_pseudo_success_rows
FROM node_probe_state
WHERE last_direct_ok = TRUE
  AND last_gateway_ok = TRUE
  AND next_retry_at > now() + interval '5 minutes';

-- 6) 备份表行数应与清理前行数一致 (审计/回滚参考)
SELECT COUNT(*) AS backup_rows_preserved
FROM node_probe_state_pseudo_success_backup_20260819;

-- 7) 触发新版本 reconcileStaleNodeProbeStates 入队 (人工执行后立即可见)
--    期望: 新版本代码下一个 30s tick 会把清理后的行重新入队
--          credential_probe_queue, 走真实 direct + gateway 双轮探测。
--    验证命令 (后台执行, 不要阻塞):
--      SELECT id, credential_id, raw_model_name, status, trigger_kind,
--             next_run_at
--      FROM credential_probe_queue
--      WHERE enqueued_at > now() - interval '2 minutes'
--      ORDER BY id DESC
--      LIMIT 50;
--
-- ============================================================================
-- 回滚 (若清理后 30 分钟内发现严重问题)
-- ============================================================================
--
-- 仅在你能确认 backup 表与现场 DB schema 一致时使用; 不在事务中执行;
-- 用 SKIP LOCKED 避免与 probe worker 冲突。
--
-- UPDATE node_probe_state nps
-- SET last_direct_ok     = b.last_direct_ok,
--     last_gateway_ok    = b.last_gateway_ok,
--     next_retry_at      = b.next_retry_at,
--     next_retry_seconds = b.next_retry_seconds,
--     last_err_code      = b.last_err_code,
--     last_err_detail    = b.last_err_detail,
--     updated_at         = now()
-- FROM node_probe_state_pseudo_success_backup_20260819 b
-- WHERE nps.credential_id = b.credential_id
--   AND nps.raw_model_name = b.raw_model_name;
--
-- ============================================================================
-- 备份表清理 (确认系统稳定 24h 后执行)
-- ============================================================================
--
-- DROP TABLE IF EXISTS node_probe_state_pseudo_success_backup_20260819;
--
-- ============================================================================

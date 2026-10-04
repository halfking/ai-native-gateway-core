-- Migration 823: session_turns.request_status —— 把网关已算好的生命周期标签落进会话族
--
-- Motivation (audit §9.150 / §9.150.4 / §9.155, 2026-10-04):
--
--   telemetry.RequestLogEntry.RequestStatus 一直带着
--   success | failure | rate_limited | in_progress 四态，其中 `rate_limited`
--   由限流路径显式写入（request_log_pipeline.go:1257/1260、embeddings.go:97）；
--   ResolveRequestStatus（client.go:3265）只产出 success/failure/in_progress，
--   **永远不会**返回 rate_limited，所以这四态无法由 Success+ErrorKind 推导。
--
--   internal/sessionv2mirror 的 entryToProcessedRequest 此前只复制
--   Success/ErrorKind/StatusCode，**丢掉了 RequestStatus**；session_turns 也没有
--   对应列 ⇒ 会话族无法区分「限流拒绝」与「真实上游失败」。
--
--   为什么必须在退役 request_logs **之前**补上：
--   394,614 行 rate_limited（连停写窗口共 ≈446,819 行，见 §9.150.3）早已被镜像
--   进 session_turns。request_logs 一旦删除，这批行将**永久失去可识别的标签**，
--   而 session_* 现有列无法干净筛出（最优代理误报 41.2%，§9.147.3）。
--
-- Scope: additive only. No backfill — 历史行的 request_status 必须保持 NULL，
--   因为判定所需信号（v1 的 request_logs）届时已不存在。回填只会是猜测。
--   新增列之后写入的行 100% 正确（写入时信号就在手上）。
--
-- 列契约：promote_session_turns_hot_to_partition（迁移 707 起）要求
--   session_turns 与 session_turns_hot 的 (列名:类型:非空) 集合**全等**，
--   否则函数入口直接 RAISE EXCEPTION。故此处必须**两侧同名同类型同可空性**加列。
--   该函数按目录派生的列名清单 INSERT（v_cols），因此新列**自动流经 promote**，
--   本迁移无需改动 promote 函数。
--
-- 幂等：IF NOT EXISTS，可安全重放。

BEGIN;

-- 1) 父表（分区表）：列会按分区树传播到既有分区。
ALTER TABLE public.session_turns
    ADD COLUMN IF NOT EXISTS request_status TEXT;

-- 2) hot 写入表：必须与父表严格对称，否则 promote 入口的列契约检查会拒绝执行。
ALTER TABLE public.session_turns_hot
    ADD COLUMN IF NOT EXISTS request_status TEXT;

COMMIT;

-- 契约自检：两侧形状必须完全一致，否则 promote 会在运行时抛错。
DO $$
DECLARE
    v_parent TEXT;
    v_hot    TEXT;
BEGIN
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_parent
      FROM pg_attribute
     WHERE attrelid = 'public.session_turns'::regclass
       AND attnum > 0 AND NOT attisdropped;
    SELECT COALESCE(string_agg(attname || ':' || format_type(atttypid, atttypmod)
                      || ':' || attnotnull, E'\n' ORDER BY attname), '')
      INTO v_hot
      FROM pg_attribute
     WHERE attrelid = 'public.session_turns_hot'::regclass
       AND attnum > 0 AND NOT attisdropped;
    IF v_parent IS DISTINCT FROM v_hot THEN
        RAISE EXCEPTION 'session_turns hot/parent column contract has drifted (column set mismatch)';
    END IF;
    RAISE NOTICE 'session_turns hot/parent column contract intact (% columns)', array_length(string_to_array(v_parent, E'\n'), 1);
END;
$$;

COMMENT ON COLUMN public.session_turns.request_status IS
    'Gateway lifecycle label: success | failure | rate_limited | in_progress. Not derivable from success/error_kind (ResolveRequestStatus never emits rate_limited). NULL for rows written before migration 823 — no backfill, the signal lived only in request_logs. Audit §9.155.';
COMMENT ON COLUMN public.session_turns_hot.request_status IS
    'Same lifecycle label as public.session_turns.request_status. Added symmetrically because promote_session_turns_hot_to_partition enforces an exact column-set contract between hot and parent. Audit §9.155.';

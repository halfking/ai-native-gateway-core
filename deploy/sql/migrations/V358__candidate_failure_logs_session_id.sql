-- V358: candidate_failure_logs 补 session_id 列 + 恢复 ts 默认值
-- 2026-08-17: 供应商切换/重试过程中，每个失败尝试一行（request_id 维度），
-- 但会话维度的归集此前只能通过 request_id → request_logs.gw_session_id
-- 间接关联。用户/运营需要直接按会话查看"同一次会话内的全部节点切换尝试"：
--   - 同一会话的多次切换尝试全部挂在同一个 session_id 下；
--   - 只有最终成功的那次体现在 request_logs 的 success 行上，
--     失败尝试全部落在 candidate_failure_logs（天然"仅一条成功"）。
--
-- ADD COLUMN 对分区表自动传播到所有分区；新列可空，兼容旧写入路径。

ALTER TABLE public.candidate_failure_logs
    ADD COLUMN IF NOT EXISTS session_id text;

-- 会话维度查询：拉取一个会话的全部失败尝试（时间倒序）。
-- 包在 DO 块里做异常容忍：分区形态下（startup 392 架构）父表建索引会
-- 级联到 columnar 子分区，个别 Citus 版本对 columnar 的 btree 支持不完整；
-- 索引建不上不应阻塞关键的加列（会话过滤在 columnar 上由 Chunk Group
-- Filters 剪枝承担，252 实测 planner 即走此路径）。
DO $$
BEGIN
    CREATE INDEX IF NOT EXISTS idx_candidate_failure_logs_session_ts
        ON public.candidate_failure_logs (session_id, ts DESC);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'V358: CREATE INDEX skipped (%) — columnar 分区不支持 btree 时可安全跳过', SQLERRM;
END $$;

-- 恢复 ts 默认值（2026-08-17 252 审计发现）：
-- 原始 037 schema 为 ts TIMESTAMPTZ NOT NULL DEFAULT now()；252 的表在
-- 历史 columnar 重建时默认值丢失，而写入方（candidate_failure_logger.go）
-- 的 INSERT 不携带 ts/id 列 → 新行全部落成 NULL ts，对 admin 列表
-- （WHERE ts >= since）、监控（max(ts)）、TTL 清理（按 ts 删除）全部不可见。
-- SET DEFAULT 仅改 catalog，columnar 上可执行、不重写数据。
-- id 不恢复默认值：startup 392 架构（hot + 月度分区）中 id 为无默认的
-- 普通 bigint（应用层去重、不强制唯一），新行 id 为 NULL 属设计内行为，
-- admin 读端已改为 NULL 容忍。
ALTER TABLE public.candidate_failure_logs
    ALTER COLUMN ts SET DEFAULT now();

-- 回填：用 request_logs.gw_session_id 补齐历史行（一次性，幂等）。
-- 注意：request_logs 的会话列是 gw_session_id（X-Gw-Session-Id 解析值，
-- 缺省时生成 gw_<uuid>），不是 session_id；executor 写入新行的
-- session_id 与该列同一标识空间，回填后新老行可直接按会话归集。
-- 限制单批 50 万行避免长事务；重复执行直到 0 行为止。
--
-- 2026-08-17 上线实测（252 / Citus 13.3）：该环境的
-- candidate_failure_logs 是 citus_columnar 单表（INSERT-only，
-- 不支持 UPDATE，报 "UPDATE and CTID scans not supported for
-- ColumnarScan"）。故用 DO 块按 access method 守护（父表或任一
-- 子分区为 columnar 即跳过）：全 heap 时正常分批回填；否则跳过
-- （历史行保持 NULL——此时历史 request_logs 行通常也已随热表
-- 轮转缺失，回填本就无可补；新行由写入链路直接携带 session_id）。
DO $$
DECLARE
    affected    bigint;
    has_columnar boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1
          FROM pg_class c
          LEFT JOIN pg_am a ON a.oid = c.relam
         WHERE c.oid = 'public.candidate_failure_logs'::regclass
           AND COALESCE(a.amname, 'heap') = 'columnar'
    ) OR EXISTS (
        SELECT 1
          FROM pg_inherits i
          JOIN pg_class ch ON ch.oid = i.inhrelid
          LEFT JOIN pg_am a ON a.oid = ch.relam
         WHERE i.inhparent = 'public.candidate_failure_logs'::regclass
           AND COALESCE(a.amname, 'heap') = 'columnar'
    ) INTO has_columnar;

    IF has_columnar THEN
        RAISE NOTICE 'V358: candidate_failure_logs（父表或子分区）为 columnar（INSERT-only）；跳过回填，历史行保持 NULL';
        RETURN;
    END IF;

    UPDATE public.candidate_failure_logs c
    SET session_id = r.gw_session_id
    FROM public.request_logs r
    WHERE c.request_id = r.request_id
      AND c.session_id IS NULL
      AND r.gw_session_id IS NOT NULL
      AND r.gw_session_id <> ''
      AND c.id IN (
          SELECT id FROM public.candidate_failure_logs
          WHERE session_id IS NULL
          LIMIT 500000
      );
    GET DIAGNOSTICS affected = ROW_COUNT;
    RAISE NOTICE 'V358 backfill batch: % rows updated (re-run until 0)', affected;
END $$;

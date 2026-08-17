-- V358: candidate_failure_logs 补 session_id 列
-- 2026-08-17: 供应商切换/重试过程中，每个失败尝试一行（request_id 维度），
-- 但会话维度的归集此前只能通过 request_id → request_logs.session_id 间接关联。
-- 用户/运营需要直接按会话查看"同一次会话内的全部节点切换尝试"：
--   - 同一会话的多次切换尝试全部挂在同一个 session_id 下；
--   - 只有最终成功的那次体现在 request_logs 的 success 行上，
--     失败尝试全部落在 candidate_failure_logs（天然"仅一条成功"）。
--
-- ADD COLUMN 对分区表自动传播到所有分区；新列可空，兼容旧写入路径。

ALTER TABLE public.candidate_failure_logs
    ADD COLUMN IF NOT EXISTS session_id text;

-- 会话维度查询：拉取一个会话的全部失败尝试（时间倒序）。
CREATE INDEX IF NOT EXISTS idx_candidate_failure_logs_session_ts
    ON public.candidate_failure_logs (session_id, ts DESC);

-- 回填：用 request_logs.session_id 补齐历史行（一次性，幂等）。
-- 限制单批 50 万行避免长事务；重复执行直到 0 行为止。
UPDATE public.candidate_failure_logs c
SET session_id = r.session_id
FROM public.request_logs r
WHERE c.request_id = r.request_id
  AND c.session_id IS NULL
  AND r.session_id IS NOT NULL
  AND c.id IN (
      SELECT id FROM public.candidate_failure_logs
      WHERE session_id IS NULL
      LIMIT 500000
  );

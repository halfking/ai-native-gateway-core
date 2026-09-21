-- 729: 252 部署验证轮（2026-09-21）session_turns 分支长窗专项索引。
-- 证据来源：252 + 本机双存量真库 EXPLAIN A/B，详见
-- docs/audit/2026-09-21-252-deploy-verification.md。
--
-- F | provider-model 抽屉轮询查询 72h 长窗的 session_turns 分支堆过滤：
--   admin/credential_monitor.go slidingWindowFromRequestLogs
--     SELECT ... FROM request_logs_with_current_month
--     WHERE credential_id = $1
--       AND lower(COALESCE(outbound_model, client_model)) = lower($2)
--       AND ts > NOW() - $window ORDER BY ts DESC LIMIT $n
--   728 已把母表分支（request_logs_hot + 月分区）收进 Index Scan，但
--   session_turns 分支（视图链冻结不可改投影）仍靠
--   session_turns_<月>_tenant_id_ts_idx 的裸 ts 范围扫 + 逐行
--   CASE-credential 过滤：窗口跨月伸入当月分区时该分支占全查询 cost 的
--   ~85%（252 EXPLAIN：2026_09 分支 60650 / 总 71260；72h 窗实测残余
--   10.9s）。
--
-- 设计定案（本机 1.2GB 2026_09 分区真库 A/B）：
--   - §七原案三列 (CASE credential, lower(model), ts DESC) **不取**——查询
--     谓词是 lower(COALESCE(t.model, d.client_model))（跨 JOIN），中列
--     lower(model) 与之永不匹配，反而挡住第三列 ts 的边界使用；
--   - 采用两列 (CASE credential, ts DESC)：等值前缀 + ts 范围双边界 +
--     ts DESC 保序，正例 EXPLAIN ANALYZE 401ms（基线 ~10s）；
--   - 会话存在性 EXISTS（727 F-B effective_session 索引）不受影响，
--     两索引并存，planner 按谓词各取所需。
--
-- 表达式列与视图投影逐字一致
-- （CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END，
-- regex+cast 均 immutable，可建索引），planner 才会在视图 UNION 分支上
-- 复用。session_turns_hot 是独立 hot 表（非分区，dual-write 架构），
-- 父表索引不级联，必须单独建（727 同款处置）。
--
-- 实现约束与 727/728 相同：PG17 不支持在分区父表上
-- CREATE INDEX CONCURRENTLY（42809），采用标准三段式：
--   1) 对每个"已存在"的分区单独 CONCURRENTLY 建（\gexec 生成，全新库无
--      分区时自然为空操作，不阻塞写入）；
--   2) 父表 ONLY 建分区索引壳（无扫描，仅短暂元数据锁）；
--   3) 逐个 ATTACH 子索引（幂等 DO 守卫）。
-- 未来月分区经 PARTITION OF 自动继承父索引。fresh install 走 startup
-- 序列执行本迁移，同样得到父壳 + 后续分区继承。

-- ── 第 1 段：对已存在的 session_turns 分区逐个 CONCURRENTLY 建 ──────────
SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.%I ((CASE WHEN credential_id ~ ''^[0-9]+$'' THEN credential_id::bigint END), ts DESC)',
  c.relname || '_credential_ts', c.relname)
FROM pg_class c
WHERE c.oid IN (
  SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.session_turns'::regclass
)
ORDER BY c.relname
\gexec

-- ── 第 2 段：父表分区索引壳 ────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_session_turns_credential_ts
  ON ONLY public.session_turns ((CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END), ts DESC);

-- ── 第 3 段：ATTACH 已建好的子索引（幂等守卫）──────────────────────────
DO $$
DECLARE
  part text;
BEGIN
  FOR part IN
    SELECT c.relname
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    WHERE i.inhparent = 'public.session_turns'::regclass
  LOOP
    IF to_regclass(format('public.%I', part || '_credential_ts')) IS NOT NULL
       AND NOT EXISTS (
         SELECT 1 FROM pg_inherits ci
         WHERE ci.inhparent = 'public.idx_session_turns_credential_ts'::regclass
           AND ci.inhrelid = to_regclass(format('public.%I', part || '_credential_ts'))
       ) THEN
      EXECUTE format('ALTER INDEX public.idx_session_turns_credential_ts ATTACH PARTITION public.%I',
                     part || '_credential_ts');
    END IF;
  END LOOP;
END $$;

-- ── 热侧：session_turns_hot 独立表（727 同款单独建）─────────────────────
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_turns_hot_credential_ts
  ON public.session_turns_hot ((CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END), ts DESC);

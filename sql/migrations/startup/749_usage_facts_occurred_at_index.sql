-- 749: usage_facts occurred_at 前导索引（R67 24h 审计轮，2026-09-26）
--
-- 背景：usage_facts 是 PARTITION BY RANGE (occurred_at) 的分区父表
-- （537），但仅有一个 DEFAULT 分区且无按日分区函数；4 个二级索引全部
-- 以 request_id/tenant_id/provider_id/canonical_id 打头，occurred_at
-- 只能作二级列被范围条件以外的路径用到。每日 rollup 五查询
--（domains/reportrollup/rollup.go providerModelDaySQL/totalDaySQL/
-- internalTenantDaySQL/internalModelDaySQL/internalPersonDaySQL）与
-- stats 对账查询（domains/stats/reconciliation.go）全是纯
-- occurred_at >= $1 AND occurred_at < $2 范围条件——无任何前导索引
-- 可用，只能对父表全量顺序扫，随表线性退化；在 252 共享 PG
--（statement_timeout 30s）上达到阈值后 rollup/对账成批被 57014
-- 击杀 → 报表缺口 + 对账永久 failed。
--
-- 修法：occurred_at 前导 btree。分区父表不支持 CREATE INDEX
-- CONCURRENTLY（PG17 42809），走 744 同款三段式：
--   ① 逐分区 CONCURRENTLY 建（不阻塞 245/154/dev 实例对共享库的
--      持续 telemetry 写入）；
--   ② 父表 ON ONLY 元数据壳（瞬时锁，无数据扫描）；
--   ③ 逐分区 ATTACH 挂接。未来接入按日分区函数后，新分区经
--      PARTITION OF 自动继承父索引。
-- 中断残留的 INVALID 索引由 Go ensure
--（db.ensureUsageFactsOccurredAtIndex，744 buildConcurrently 同款）
-- 先 DROP 再建，否则 IF NOT EXISTS 永远跳过重建。
-- 本文件不经 installer（psql --single-transaction 无法承载
-- CONCURRENTLY；744 同理走升级通道 + Go ensure 双通道），全新安装由
-- gateway 首启 ensure 链兜底。
--
-- down：DROP 父索引（attached 子索引级联消失）+ 循环清理尚未 ATTACH
-- 的孤儿子索引（744 down 同款）。

-- ① 逐分区 CONCURRENTLY（含现存 DEFAULT 分区；未来日分区回放本文件时同样覆盖）
SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.%I (occurred_at DESC)',
  c.relname || '_occurred_at_idx', c.relname)
FROM pg_class c
WHERE c.oid IN (
  SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.usage_facts'::regclass
)
ORDER BY c.relname
\gexec

-- ② 父表 ONLY 壳（元数据级瞬时锁）
CREATE INDEX IF NOT EXISTS idx_usage_facts_occurred_at
  ON ONLY public.usage_facts (occurred_at DESC);

-- ③ 逐分区 ATTACH
DO $$
DECLARE
  part text;
BEGIN
  FOR part IN
    SELECT c.relname
    FROM pg_class c
    WHERE c.oid IN (
      SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.usage_facts'::regclass
    )
    ORDER BY c.relname
  LOOP
    EXECUTE format(
      'ALTER INDEX public.idx_usage_facts_occurred_at ATTACH PARTITION public.%I',
      part || '_occurred_at_idx');
  END LOOP;
END $$;

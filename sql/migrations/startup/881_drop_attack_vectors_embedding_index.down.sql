-- Rollback for migration 881_drop_attack_vectors_embedding_index.sql.
-- Recreates the ivfflat index on injection_attack_vectors.embedding. No data is touched.
--
-- ⚠ 这不是「零成本回滚」，请先读完再决定：
--
--   1. 重建的是 **ivfflat WITH (lists = 100)**（与 364 建的那条同形）。
--      ★ ivfflat 的召回率依赖 `lists` 与表规模：lists=100 是**为十万行以上**
--        的表选的。PG 自己在建索引时就会告警：
--          NOTICE: ivfflat index created with little data
--          DETAIL: This will cause low recall.
--          HINT: Drop the index until the table has more data.
--      ⇒ 若表只有几百行，**不建**比建了更诚实：一个召回率不可知、
--        却看起来"有向量检索能力"的索引，正是本轮要消灭的那类误导性对象。
--        本 down 文件保留重建路径，是为了让「曾经有过这个能力」这件事可追溯，
--        并在表规模真的上来时可用；**不是**建议现在就回滚。
--
--   2. 回滚会把写放大一并带回来：一次性 PG 17.10 实测（EXPLAIN ANALYZE,
--      BUFFERS，200 行 1536 维向量插入）
--        有 ivfflat：3229.421 ms，Buffers read=21279
--        无 ivfflat：1149.310 ms，Buffers read=12
--      ⇒ 每 200 行多付约 2.08 秒与 2.1 万次缓冲读。
--
--   3. 若将来真要上向量检索，**正确的顺序**是先有写入方（把 embedding 填上）、
--      再按实际行数重算 lists（ivfflat 官方建议 lists ≈ rows/1000 ~ rows/10000），
--      最后才建索引 —— 而不是把这条为"曾经 0 行"选的索引原样重建。
--
-- 幂等：已存在则只校验形态（ivfflat + 键列 embedding），形态不对就 loudly 失败，
--        不默默重建（与 876 down 同形态）。
--
-- 锁：普通表 ⇒ DROP/CREATE INDEX 都可以 CONCURRENTLY；但本表极小，
--   CREATE INDEX 空表是毫秒级 ⇒ 走单事务链 + lock_timeout=2s
--   （抢不到锁快速失败、整体回滚、稍后重跑）。

BEGIN;

SET LOCAL statement_timeout = '10min';
SET LOCAL lock_timeout = '2s';

DO $recreate_881_attack_vectors_embedding$
BEGIN
  IF to_regclass('public.injection_attack_vectors') IS NULL THEN
    RAISE NOTICE '881 down: injection_attack_vectors 不存在（表未建）⇒ 无事可做，跳过';
    RETURN;
  END IF;

  IF to_regclass('public.idx_attack_vectors_embedding') IS NULL THEN
    EXECUTE 'CREATE INDEX idx_attack_vectors_embedding
             ON public.injection_attack_vectors
             USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100)';
    RAISE NOTICE '881 down: recreated idx_attack_vectors_embedding (ivfflat, lists=100)';
  ELSIF NOT EXISTS (
    SELECT 1 FROM pg_index
    WHERE indexrelid = to_regclass('public.idx_attack_vectors_embedding')
      AND indrelid = 'public.injection_attack_vectors'::regclass
      AND pg_get_indexdef(indexrelid, 1, true) = 'embedding'
  ) THEN
    RAISE EXCEPTION
      '881 down: idx_attack_vectors_embedding exists but is not an index on embedding of injection_attack_vectors';
  ELSE
    RAISE NOTICE '881 down: idx_attack_vectors_embedding already present and matches';
  END IF;
END
$recreate_881_attack_vectors_embedding$;

COMMIT;

-- 881_drop_attack_vectors_embedding_index.sql
-- 2026-10-20：删掉 `injection_attack_vectors` 上那条**只写不读**的 ivfflat 索引。
--
-- -----------------------------------------------------------------------
-- 缺陷本体（读方普查 + 写侧普查，不是推理）
--
-- ① **没有任何读方**。全仓（排除 vendor/ 与审计 scratch）搜 `<=>` / `vector_cosine_ops`：
--      唯一的 `<=>` 读方在 autoroute/embedding_classifier.go:97-100，
--      打的是 `task_type_centroids.centroid`（**另一张表**，索引是活的、保留）。
--      `injection_attack_vectors` 在全仓只有两处引用：
--        domains/promptinjection/detector.go:807  —— INSERT
--        admin/prompt_injection_handler.go:1219    —— SELECT（按 tenant_id 列表，
--                                                       **不碰 embedding**）
--      ⇒ 这条 ivfflat 索引**零读方**。
--
-- ② **`embedding` 列恒为 NULL**。364 建表时该列是 `embedding TEXT`，
--    随后 `ALTER COLUMN embedding TYPE VECTOR(1536) USING NULL`。
--    而**唯一**的写入方 detector.go 的 INSERT 列清单是
--      (tenant_id, attack_text, attack_hash, categories, severity, source, detected_at)
--    —— **没有 embedding** ⇒ 该列**永远**是 NULL，
--    ivfflat 索引里**一个条目都没有**，却要在每次写入时付维护成本。
--
-- ③ **写放大实测**（PG 17.10，同机同数据，唯一差别是这条索引）：
--      EXPLAIN (ANALYZE, BUFFERS) INSERT 200 行 1536 维向量
--
--      第一次实测（2026-10-20）：
--        有 ivfflat：Execution Time = 3229.421 ms
--                     Buffers: shared hit=2463 read=21279 dirtied=1434 written=1737
--        无 ivfflat：Execution Time = 1149.310 ms   （-64.4%）
--                     Buffers: shared hit=3116 read=12    dirtied=214  written=313
--
--      ⚠️ **订正（2026-10-20 同日复测，结论不变但理由换掉）**：
--        有 ivfflat：Execution Time = 1190.438 ms
--                     Buffers: shared hit=23954 read=2 dirtied=409 written=412
--        无 ivfflat：Execution Time =  656.242 ms   （-44.9%）
--                     Buffers: shared hit=3343 read=2 dirtied=210 written=213
--
--        ⇒ 「read 21279 → 12（-99.9%）」**不是索引带来的差别**，而是**冷/热缓存
--          的切分**：同一批约 23.8k 个页，两次实测只是分别落在 read 还是 hit 上。
--          **与缓存状态无关的稳健指标是 hit+read 的总触碰次数**：
--            有 ivfflat ≈ 23,742 / 23,956     无 ivfflat ≈ 3,128 / 3,345
--            ⇒ **约 7.2 倍，-86%**
--          墙钟差 1.8~2.8 倍（绝对值随机器负载变化，**差异方向不随变化**）。
--
--        （旁证：pgvector 建这条索引时自己就提醒
--          NOTICE: ivfflat index created with little data
--          DETAIL: This will cause low recall.
--          HINT:  Drop the index until the table has more data.
--          —— 与本迁移的处置方向一致。）
--
--      复现方式：临时 schema 建同形表（id + vector(1536)），两腿各插 200 行
--      随机向量，INSERT 外面套 EXPLAIN (ANALYZE, BUFFERS)，比 hit+read 总数。
--
-- ★ 为什么这条索引此前**完全隐形**：表恒为空。
--   detector.go 的写入方把 json.Marshal 的 []byte 直接绑进枚举数组列
--   `injection_category[]` ⇒ PG 报 `malformed array literal` ⇒
--   **每一次写入都失败**，失败被 slog.Warn 吞掉 ⇒ 静默失效。
--   本轮已修（见 docs/audit/2026-10-20-prompt-injection-attack-vector-write.md）：
--   写侧修好之后表**开始有行**，这条索引就从"零成本的死物"变成
--   "有成本的死物" ⇒ 必须与写侧修复**同轮**处理，否则修完写侧反而更慢。
--
-- -----------------------------------------------------------------------
-- 守卫：在**本文件里现算**，不写死结论
--
--   写死的守卫会随 schema 演化悄悄失效。这里证两件事，任一不成立 ⇒ 整条回滚：
--
--   ① `injection_attack_vectors.embedding` **没有非 NULL 值**。
--      ⇒ 若将来有人开始真的写 embedding（= 真有人要向量检索了），
--        本迁移**拒绝删**，逼人重新评估。
--   ② 目标索引确实存在且是 **ivfflat on embedding**（形态对得上才删）。
--
-- ★ 与 876 的区别：876 的守卫证的是"有覆盖方"（证明删了不亏），
--   本文件的守卫证的是"**没人写、没人读**"（证明删了不亏）。
--   两者的共同点：证不出来就**整条回滚**，不"先删了再说"。
--
-- -----------------------------------------------------------------------
-- 锁与回滚
--
-- `injection_attack_vectors` 是**普通表**（不是分区父表）⇒ PG 支持
-- DROP INDEX CONCURRENTLY；但本表极小、DROP INDEX 只改元数据不重写数据，
-- 正常是毫秒级 ⇒ 走单事务链 + 普通 DROP INDEX（876 同款），
-- lock_timeout=2s：抢不到锁就**快速失败**，不排在写流量后面；
-- 失败时事务整体回滚，什么都没发生，稍后重跑即可。
--
-- 回滚：down 文件按原样重建（lists=100，vector_cosine_ops）——
--   但 down 会把上面那 64% 的写放大**一并带回来**，请先读 down 文件头。
--
-- ⚠ 普查口径的诚实边界（876 §82 的教训）：
--   本条的"零读方"结论来自**全仓源码普查**（grep `<=>` / 表名），
--   **不是** pg_stat_statements 普查 ⇒ 不受"8 天窗口 + top-N 97.7% 容量"
--   那两条限制的影响（876 的同类结论正是被那两条削弱的）。
--   ★ 但源码普查**看不到本仓之外**的读方（别的服务直连这张表）。
--     本表的全部读方都在 admin handler 与 detector 两个文件里
--     （全仓仅两处引用，见上），故这个边界对本表**不构成实质风险** ——
--     但如果将来出现第三方直连，本条结论需要重新评估。
--
-- ===========================================================================

BEGIN;

SET LOCAL statement_timeout = '5min';
SET LOCAL lock_timeout = '2s';

DO $guard_881_attack_vectors_embedding$
DECLARE
  v_table   text := 'public.injection_attack_vectors';
  v_index   text := 'public.idx_attack_vectors_embedding';
  v_nonnull bigint;
  v_is_ivf  boolean;
BEGIN
  IF to_regclass(v_table) IS NULL THEN
    RAISE NOTICE '881: % 不存在（表未建）⇒ 无事可做，跳过', v_table;
    RETURN;
  END IF;

  IF to_regclass(v_index) IS NULL THEN
    RAISE NOTICE '881: % 不存在（已删或未建）⇒ 无事可做，跳过', v_index;
    RETURN;
  END IF;

  -- 守卫 ②：形态必须对得上（ivfflat + 键列就是 embedding）
  SELECT EXISTS (
    SELECT 1
    FROM pg_index i
    JOIN pg_class ic ON ic.oid = i.indexrelid
    JOIN pg_am am ON am.oid = ic.relam
    WHERE i.indexrelid = to_regclass(v_index)
      AND am.amname = 'ivfflat'
      AND pg_get_indexdef(i.indexrelid, 1, true) = 'embedding'
  ) INTO v_is_ivf;

  IF NOT v_is_ivf THEN
    RAISE EXCEPTION
      '881: % 存在但形态不是「ivfflat on embedding」⇒ 拒绝删除（可能已被改造成别的索引）',
      v_index;
  END IF;

  -- 守卫 ①：embedding 列里不许有非 NULL 值
  EXECUTE format('SELECT count(*) FROM %s WHERE embedding IS NOT NULL', v_table)
    INTO v_nonnull;

  IF v_nonnull > 0 THEN
    RAISE EXCEPTION
      '881: % 里已有 % 行 embedding 非 NULL ⇒ 该索引可能已在服务向量检索，拒绝删除。'
      '请先确认读方与用途，再决定是删索引还是保留。',
      v_table, v_nonnull;
  END IF;

  RAISE NOTICE '881: 守卫通过 —— % 无非 NULL embedding、形态为 ivfflat ⇒ 将删除', v_index;
END
$guard_881_attack_vectors_embedding$;

DROP INDEX IF EXISTS public.idx_attack_vectors_embedding;

COMMIT;

-- ---------------------------------------------------------------------------
-- 怎么验收（★ 别按索引名 grep 就算完，也要确认**没有**别的 embedding 索引残留）
--
--   SELECT i.indexrelid::regclass::text AS idx,
--          am.amname,
--          pg_get_indexdef(i.indexrelid) AS def
--   FROM pg_index i
--   JOIN pg_class ic ON ic.oid = i.indexrelid
--   JOIN pg_am am ON am.oid = ic.relam
--   WHERE i.indrelid = 'public.injection_attack_vectors'::regclass
--     AND pg_get_indexdef(i.indexrelid) LIKE '%embedding%';
--
-- 期望：**0 行**。若仍有行，说明删的不是目标索引，重跑本文件即可（幂等）。
--
-- 反向验收（确认另外三条索引还在，删之前最该看的那一眼）：
--
--   SELECT ic.relname, pg_get_indexdef(i.indexrelid)
--   FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid
--   WHERE i.indrelid = 'public.injection_attack_vectors'::regclass
--   ORDER BY 1;
--
-- 期望：仍有 idx_attack_vectors_categories（GIN）/ idx_attack_vectors_tenant
--       / unique_attack_hash / injection_attack_vectors_pkey。
-- ---------------------------------------------------------------------------

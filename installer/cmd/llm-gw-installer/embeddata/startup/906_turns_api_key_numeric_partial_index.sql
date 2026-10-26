-- Migration 906: 给「会话列表的 api_key 回填」补**部分索引**（父表 + hot 表）。
--
-- ═══════════════════════════════════════════════════════════════════════
-- 为什么需要它（★ 全部为 2026-10-24 生产只读实测，结论可复算）
-- ═══════════════════════════════════════════════════════════════════════
--
-- 【背景：R93 已把 500@30s 修成 200，但端点仍要 ~1.5 s】
--   R93 把 enrich 查询从「视图 ⋈ 全量 history source」（**同一批行扫两遍**）
--   改成「每 source 先 GROUP BY session_id + max(turn_no) 再回定位」：
--     旧形态 13,066 ms  →  新形态 1,350~1,569 ms（探针批次）
--   ⇒ 500@30s 消失（2843 上线后 `?model=` 200×8，1.03~3.10 s），
--     但 **enrich 仍是端点的绝对主力**（pg_stat_statements 窗口差分：
--     6 次请求里 enrich 占 9,143 ms / 均值 1,523.8 ms，而列表查询只 17.2 ms）。
--
-- 【★ 真因：过滤条件无法从索引判定 ⇒ 必须回表 ⇒ 扫 82,297 行堆，产出 0 行】
--   探针会话（`sys:probe:*`）的 api_key_id **全是 NULL**（252 实测 462,394 行全空）⇒
--   enrich 的过滤条件 `(CASE WHEN api_key_id ~ '^[0-9]+$' THEN api_key_id::bigint END)
--   IS NOT NULL` **在索引里判定不了** ⇒ 必须回表 ⇒ 生产 EXPLAIN 实测：
--
--     Index Scan using session_turns_2026_09_heap_tenant_id_session_id_turn_no_idx
--       Index Cond: (session_id = ANY('{20 个 sys:probe:* 会话}'))
--       Filter: (CASE WHEN (api_key_id ~ '^[0-9]+$') THEN (api_key_id)::bigint END IS NOT NULL)
--       Rows Removed by Filter: **82,297**
--       Buffers: shared hit=66,606
--
--   ⇒ ★★ **扫了 82,297 行堆，全部被过滤掉，最终产出 0 行**，却花了 1,350~1,569 ms。
--   ⇒ ★ 父表本来就有 `(session_id, turn_no DESC)` 索引（idx_session_turns_session），
--     但它**不含 api_key_id** ⇒ 用不上 index-only ⇒ 回表 ⇒ 慢。
--
-- 【★ 修法：部分索引 —— 只索引「api_key_id 是数字」的行】
--   谓词与查询里的过滤条件**逐字一致** ⇒ 规划器能认出来 ⇒ 走 Index Only Scan：
--     · 探针流量：api_key_id 全 NULL ⇒ **索引里一行都没有** ⇒ 几乎不花时间；
--     · 业务流量：api_key_id 是数字 ⇒ 索引里有 ⇒ 命中。
--
-- 【★ 实测（252 生产，事务内 CREATE INDEX + EXPLAIN ANALYZE + ROLLBACK ⇒ 生产零残留）】
--
--   形态                                              探针批次执行时间
--   ─────────────────────────────────────────────────────────────────────────
--   CASE 形式过滤（无部分索引，= 2843 生产形态）              1,350.8 ms
--   裸正则过滤（无部分索引）                                  1,569.3 ms
--   ★★ 裸正则过滤 + 本迁移的部分索引                        **4.57 ms**   ← 295×
--   （对照）INCLUDE(api_key_id) 的普通索引                     0.62 ms
--        ⇒ 但它需要 ANALYZE 才能让规划器认账（实测 ANALYZE 35.9 s），
--          且索引体积大得多（**含全部 api_key_id 为 NULL 的行**）
--        ⇒ ★ 选部分索引：收益已足够（4.57 ms），不引入 ANALYZE。
--
-- 【★ 口径等价（252 生产，真实非空批次，同一事务内同一快照，双向 EXCEPT ALL）】
--   取「非 sys: 前缀 + api_key_id 是数字」的会话里轮次最多的 20 个（业务批次）：
--     old_rows=**20** / new_rows=**20** / only_new=**0** / only_old=**0**
--   ⇒ ★ 裸正则过滤与原 CASE 形式**逐行一致** ⇒ 只差「能不能用上索引」，
--     不差「取哪些行」。
--   （逐情形推导亦等价：NULL ⇒ 两边都排除；非数字 ⇒ 两边都排除；数字 ⇒ 两边都保留。）
--
-- 【★ 语义耦合】
--   本迁移的谓词必须与 `db.SessionFamilyAPIKeyIDNumericPredExpr`
--   （= `t.api_key_id ~ '^[0-9]+$'`）**逐字一致**
--   ⇒ ★ PG 的谓词蕴含判定是**语法的** ⇒ 谓词写成 CASE 形式就认不出来
--   ⇒ 由 migration_906_turns_api_key_numeric_idx_test.go 钉住。
--
-- 【锁与时长】
--   public.session_turns 是**分区热写表**（父表 4.7 GB / 1,014,911 行）
--   ⇒ 分区父表不支持 CREATE INDEX CONCURRENTLY ⇒ 用普通 CREATE INDEX：
--   · lock_timeout=60s：抢不到锁快速失败、整体回滚、稍后重跑（903/904/905 同款）
--   · statement_timeout=10min：共享 PG 服务端 statement_timeout=30s 会掐断
--     长索引构建，57014 击穿后迁移永不记账，成为永久卡点（903/904/905 同款）
--   · ★ 实测构建时长（252 生产，事务内真实文件体 + ROLLBACK）：
--       C1 idx_session_turns_api_key_numeric        **6,026 ms**
--       C2 idx_session_turns_hot_api_key_numeric          **21 ms**
--     ⇒ ★ 这 **~6 秒**里 session_turns 的**写入被阻塞**（SHARE 锁）
--     ⇒ 比 905 的 18.5 s 短得多（部分索引只含合格行）⇒ 窗口压力小。
--
-- 【幂等】IF NOT EXISTS。Down：按名删两条索引。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

-- C1：父表（分区父表 ⇒ 自动传播到既有子分区与未来分区）。
--   列序 = (session_id, turn_no DESC) ⇒ 供 GROUP BY session_id + max(turn_no) 走
--   index-only；WHERE 谓词 = api_key 归一后非空（★ 必须与 Go 侧逐字一致）。
CREATE INDEX IF NOT EXISTS idx_session_turns_api_key_numeric
    ON public.session_turns (session_id, turn_no DESC)
    WHERE api_key_id ~ '^[0-9]+$';

-- C2：hot 腿（同构，hot 表只有 3,073 行 ⇒ 构建 21 ms）。
CREATE INDEX IF NOT EXISTS idx_session_turns_hot_api_key_numeric
    ON public.session_turns_hot (session_id, turn_no DESC)
    WHERE api_key_id ~ '^[0-9]+$';

COMMIT;

-- POST_CONDITION:
--   SELECT indexname, pg_get_indexdef((quote_ident(indexname))::regclass)
--     FROM pg_indexes
--    WHERE schemaname = 'public'
--      AND indexname IN ('idx_session_turns_api_key_numeric',
--                         'idx_session_turns_hot_api_key_numeric');
--   -- 分区父表的 pg_relation_size 恒为 0（真实体积在子分区侧，
--   --   按 pg_index.indisparent 关联核对，不按名字 grep —— 877 台账口径）。
--   -- 预期收益（252 生产 EXPLAIN 口径，2026-10-24 实测）：
--   --   ?model= 探针批次 1,350~1,569 ms → **4.57 ms**
--   --   ?model= 端点整体 1.03~3.10 s → 预期降到 ~0.2 s 量级

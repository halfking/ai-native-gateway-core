-- Migration 905: 给「会话列表的轮次级过滤」补 model 维度的索引（父表 + hot 表）。
--
-- ═══════════════════════════════════════════════════════════════════════
-- 为什么需要它（★ 全部为 2026-10-24 生产只读实测，结论可复算）
-- ═══════════════════════════════════════════════════════════════════════
--
-- 【症状】GET /api/admin/turns/sessions 带 `?model=` 时生产**恒 500@30s**。
--   154 直连 REPEAT=3（build 2841，R90 的两腿 EXISTS 改写**已上线**）：
--     ?limit=20                                        200 200 200   0.878 0.155 0.111
--     ?limit=20&api_key_id=105                         200 200 200   9.576 0.216 0.151
--     ?limit=20&model=deepseek-ai%2F...-instruct       500 500 500  30.008 30.007 30.018
--     ?limit=20&provider=14                            200 200 200   0.358 0.100 0.080
--     ?limit=20&status_code=500                        200 200 200   0.554 0.181 0.153
--   ⇒ 三条已修好（200），**只有 model 那条仍 500** ⇒
--   ⇒ ★★ **R90「四条过滤路径全部修好」的结论被本条证伪**（R90 改写把
--      每行的反连接成本去掉了，但没解决「稀有过滤 ⇒ 外层深扫」）。
--
-- 【★ 根因：不是「形状」问题，是「选择性」问题 —— 外层会话驱动扫穿全表】
--   该 model 的实际分布（252 生产只读）：
--     session_turns（父表）  **458 轮 / 30 个会话**
--     session_turns_hot      **0 轮 / 0 个会话**
--     public.sessions       **441,577 行 / 441,112 个会话**
--   ⇒ 过滤条件只命中 **30 / 441,112** 个会话。
--   ⇒ 会话列表主查询是 `FROM public.sessions s … ORDER BY s.updated_at DESC
--      , s.session_id DESC LIMIT 21` ⇒ ★ **必须扫到凑够 21 行**。
--   ⇒ 252 EXPLAIN ANALYZE（事务内建索引后，生产零持久变更）实测：
--       Index Scan using sessions_2026_09_updated_at_session_id_idx
--         → Rows Removed by Filter: **72,455**
--       Index Scan using sessions_2026_10_updated_at_session_id_idx
--         → Rows Removed by Filter: **277,899**
--       ⇒ ★ **外层一共扫了 350,354 个会话才凑出 21 行**，
--         每行还要跑一次「去重 SubPlan + 半连接探测」⇒ **17,501 ms**。
--   ⇒ 对照：provider='14' 命中 181,907 轮、status_code=500 命中 671,117 轮
--     ⇒ 前 21 个会话立刻凑齐 ⇒ 0.358s / 0.554s ⇒ ★ **同一个形状，
--     只因「过滤值稀有度」不同，一个 0.3s 一个 30s**。
--
-- 【★ 排除项：不是统计信息陈旧 ⇒ 「VACUUM ANALYZE 就能修」不成立】
--   pg_stat_all_tables 实测：session_turns last_analyze = **2026-10-19**，
--   session_turns_2026_10 last_autoanalyze = **2026-10-24** ⇒ 统计是新鲜的。
--   pg_stats：session_turns.model n_distinct=567、MCV 100 个、top_freq 0.16；
--   规划器对 `model = 'deepseek-ai/...'` 的行数估计 **109+19=128**，
--   实际 **458** ⇒ 只低估 3.6× ⇒ ★ **不是「统计缺失」能解释的量级**。
--
-- 【★ 修法：索引（让轮次侧变便宜）+ 形态改写（让规划器能自己选驱动方向）】
--   两条腿**缺一不可**（252 事务内实测，ROLLBACK ⇒ 生产零残留）：
--
--   形态                                     model=稀有        provider='14'(常见)
--   ─────────────────────────────────────────────────────────────────────────
--   旧形态（两条 EXISTS，OR 组合）              17,501 ms  ✗        （会话驱动）
--   只加索引、不改形态                          17,501 ms  ✗        （仍会话驱动）
--   只改形态成 JOIN 派生表（无索引）             ——       ——          **10,080 ms** ✗✗
--   ★ V2 形态（派生表 UNION ALL + EXISTS）无索引  退化到会话驱动（与今天同量级）
--   ★★ V2 形态 + 本迁移的索引                 **8.0 ms** ✓✓      **48.6 ms** ✓
--
--   ★ 为什么 V2 形态是关键：把「OR 两条 EXISTS」改成
--     `EXISTS (SELECT 1 FROM (… UNION ALL …) ft WHERE ft.session_id = s.session_id …)`
--     ⇒ 派生表可被 planner **pull-up** ⇒ 半连接的两个驱动方向都可选：
--     · 过滤值**稀有** ⇒ 轮次侧走 model 索引（458 行）⇒ 轮次驱动（外层只 30 行）
--     · 过滤值**常见** ⇒ 轮次侧无索引 ⇒ 顺序扫 110 万行太贵 ⇒ planner 自动
--       选会话驱动（外层前 21 个会话立刻凑齐）
--   ⇒ ★ **不写死「轮次驱动」** —— 那是 V3，实测在常见过滤上退化到 10,080 ms。
--
-- 【★ 口径等价（252 生产，同一事务内同一快照，逐行对拍）】
--   新旧两形态各跑一次，取同一组 21 行，双向 EXCEPT ALL：
--     model（稀有）    : only_r90=**0** / only_v2=**0** / r90_rows=21 / v2_rows=21
--     provider='14'    : only_r90=**0** / only_v2=**0** / r90_rows=21 / v2_rows=21
--   ⇒ **结果集逐行一致** ⇒ 两形态只差「求值顺序」，不差「取哪些行」。
--   ⇒ 结构上也等价：旧形态 =「hot 腿 EXISTS ∨ 父表腿 EXISTS」，
--     新形态 =「(hot ∪ 父表) 派生表上的 EXISTS」⇒ 同一谓词集、同一半连接语义
--     （NULL 两侧都用 `=` ⇒ NULL 不匹配 ⇒ 行为一致）。
--
-- 【★ 为什么只给 model 建索引，不给 provider / status_code 建】
--   pg_stats 实测：provider n_distinct=**20**（top_freq 0.194）、
--   status_code n_distinct=**8**（top_freq 0.635）⇒ 这两列**没有选择性**
--   ⇒ 给它们建索引只会让 planner 更倾向于「轮次驱动 + 大哈希构建」
--   ⇒ 实测 V3 形态正是这样退化的（provider 10,080 ms）。
--   ⇒ ★ **只给真正稀有的列建索引**，其余交给 planner 选会话驱动。
--
-- 【锁与时长】
--   public.session_turns 是**分区热写表**（父表 4.7 GB / 1,014,911 行；
--   2026_09 分区 2,735 MB / 798,278 行、2026_10 分区 2,013 MB / 307,336 行）。
--   ⇒ 分区父表不支持 CREATE INDEX CONCURRENTLY ⇒ 用普通 CREATE INDEX：
--   · lock_timeout=60s：抢不到锁快速失败、整体回滚、稍后重跑
--     （882/876/891/901/903/904 同款；startup_ddl_lock_guard_gate_test 钉住
--     强锁 DDL 必须带 lock_timeout）
--   · statement_timeout=10min：索引构建本身可能很久；共享 PG 服务端
--     statement_timeout=30s 会掐断长索引构建，57014 击穿后迁移永不记账，
--     成为永久卡点（903/904 同款）
--   · ★ 实测构建时长（252 生产，事务内真实文件体 + ROLLBACK）：
--       C1 idx_session_turns_model_session       **18,512 ms**
--       C2 idx_session_turns_hot_model_session         **28 ms**
--     ⇒ ★ 这 **~18.5 秒**里 session_turns 的**写入被阻塞**（SHARE 锁）
--     ⇒ 该迁移应落在低峰窗口。
--
-- 【幂等】IF NOT EXISTS。Down：按名删两条索引。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

-- C1：父表（分区父表 ⇒ 自动传播到 6 个既有子分区与未来分区）。
--   列顺序 = (过滤列, 关联键)，使 planner 能既按 model 定位、
--   又能按 (model, tenant_id, session_id) 直接命中关联。
--   ★ 语义耦合：colPred 必须与 admin/turns_sessions.go 的 `?model=` 过滤
--     逐字对应 —— 由 migration_905_turns_session_model_idx_test.go 钉住。
CREATE INDEX IF NOT EXISTS idx_session_turns_model_session
    ON public.session_turns (model, tenant_id, session_id);

-- C2：hot 腿（同构，hot 表只有 3,073 行 ⇒ 构建 28 ms）。
CREATE INDEX IF NOT EXISTS idx_session_turns_hot_model_session
    ON public.session_turns_hot (model, tenant_id, session_id);

COMMIT;

-- POST_CONDITION:
--   SELECT indexname, pg_size_pretty(pg_relation_size((quote_ident(indexname))::regclass))
--     FROM pg_indexes
--    WHERE schemaname = 'public'
--      AND indexname IN ('idx_session_turns_model_session',
--                         'idx_session_turns_hot_model_session');
--   -- 分区父表的 pg_relation_size 恒为 0（真实体积在子分区侧，
--   --   按 pg_index.indisparent 关联核对，不按名字 grep —— 877 台账口径）。
--   -- 预期收益（252 生产 EXPLAIN 口径，2026-10-24 实测）：
--   --   ?model= 稀有值  30,008 ms（500@30s）→ **8.0 ms**
--   --   ?provider=14     358 ms → **48.6 ms**
--   --   ?status_code=500 554 ms → **249.6 ms**

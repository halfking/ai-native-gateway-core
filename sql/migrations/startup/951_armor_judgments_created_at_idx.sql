-- 951_armor_judgments_created_at_idx.sql
-- 2026-11-07：armor_judgments 补 (created_at) 前导索引 —— 根修
-- bg/audit_trimmer.go 90 天 retention DELETE 批的「整条复合索引顺扫」。
--
-- -----------------------------------------------------------------------
-- 缺陷本体（2026-11-07 252 生产 SQL 日志审计轮实测，非推理）
--
-- trim 批形状（bg/audit_trimmer.go，armorBatchSize=5000、
-- armorMaxBatchesPerTick=20、24h tick + boot 初始 trim）：
--     DELETE FROM armor_judgments
--     WHERE ctid IN (SELECT ctid FROM armor_judgments
--                    WHERE created_at < NOW() - $1::interval LIMIT 5000)
--
-- 内层 SELECT 只按 created_at 过滤，而该表全部三条二级索引都是
-- (tenant_id, created_at DESC) / (request_id) / (check_type, decision)
-- —— 没有任何一条以 created_at 为前导。规划器的唯一出路是顺着
-- idx_armor_judgments_tenant_time 把**整条索引顺序扫一遍**（前导列无谓词
-- ⇒ 没有边界可落）、逐行 filter created_at，直到 LIMIT 5000 凑满或扫完
-- 证明空集。
--
-- 252 生产实测（471,648 行 / 155MB）：
--   • 安静时点 EXPLAIN (ANALYZE, BUFFERS)（R189 审计轮，宿主清洗后）：
--     **3,689 ms** —— `Index Scan using idx_armor_judgments_tenant_time`、
--     read=3,219 buffers，90 天外行 ≈ 0 时每批也要把索引从头走到尾；
--   • pg_stat_statements（-d llm_gateway）：同形状 DELETE 累计
--     calls=52 mean 13.2s + calls=13 mean 18.2s（R188 之前的饱和窗加权，
--     quiet 值 3.7s × 饱和通胀 4-5× 与之吻合）；
--   • 24h 日志窗（11-06 06:41 → 11-07 05:46）：≥10s 慢查 5 条
--     （mean 12.9s / max 19.1s）——24h tick 每次 boot trim 至少白烧
--     20 批 × 3.7s ≈ 74s 纯扫描，90 天外行开始累积后每批还要真删。
--
-- 为什么是「白烧」：retention 90 天（audit_trimmer.go armorRetention），
-- 表自 2026-07 存在至今最早行也在窗口边缘 —— 当前每个 trim tick 的
-- 内层 SELECT 都在花 3.7s 证明「没有行可删」。这不是 canary 语义
-- （与 882 的负成本检查不同），trim 是真 TTL 路径：随着表龄跨过 90 天，
-- 每批从「白扫」变成「扫完再删」，成本曲线只升不降。
--
-- -----------------------------------------------------------------------
-- 修法：补 (created_at) 单列前导索引
--
--   CREATE INDEX … ON public.armor_judgments (created_at)
--
--   • created_at 范围谓词直接落在 B-tree 边界：0 行可删时亚毫秒证明空集，
--     满批时按序取 5000 条 ctid，不再有全索引顺扫；
--   • 为什么不并入现有 (tenant_id, created_at)：该索引的职责是 admin
--     租户隔离列表（RLS + 「按租户 + 时间倒序」，049 头注自证），
--     前导 tenant_id 服务的是另一类访问模式；trim 谓词天然无 tenant
--     —— 两个模式各配各的索引，不动既有索引的键序（821 键序先例）；
--   • 写放大：写入方 security/armor/logger.go 每判定一条 INSERT
--     （pss 20 万+ calls，mean ~18ms），第 4 条二级索引的维护代价
--     在该频度下可忽略；表体量被 90d retention 钉在 ~50 万行量级，
--     索引 ~30MB 级别。
--
-- 锁与时长：普通 CREATE INDEX 持 SHARE 锁到构建完成；155MB 表构建
-- 秒级，写入方（判定 INSERT，均值 ~0.2/s）锁窗内短暂阻塞，无死锁面。
-- lock_timeout=60s：抢不到锁快速失败、整体回滚、稍后重跑（878/882 同款，
-- startup_ddl_lock_guard_gate_test 钉住强锁 DDL 必须带 lock_timeout）。
-- 幂等：IF NOT EXISTS。Down：DROP INDEX IF EXISTS。
--
-- 台账/通道：canonical 与 installer embeddata 两副本 SHA 现场一致；
-- installer go:embed + map、dbinit StartupFiles、部署通道、.down.sql
-- 均已就位。契约门：migration_951_armor_judgments_created_at_idx_test.go
-- （钉键序/同名 down/两份字节一致/trim 侧谓词 cross-pin）。

BEGIN;

SET LOCAL lock_timeout = '60s';
SET LOCAL statement_timeout = '10min';

CREATE INDEX IF NOT EXISTS idx_armor_judgments_created_at
    ON public.armor_judgments (created_at);

COMMIT;

-- POST_CONDITION:
--   SELECT indexname FROM pg_indexes
--    WHERE tablename = 'armor_judgments' AND indexname = 'idx_armor_judgments_created_at';

-- ===========================================================================
-- File:          sql/migrations/startup/974_market_price_identity_and_tier.sql
-- Migration:     974
-- Purpose:       行情面**模型身份对齐** + 阶梯/缓存价落库（对齐 salesdecorate
--                Compare 现价模式，2026-11-17 定价对比完善轮）。
--
-- 背景（211 真库取证，2026-11-17）：
--  ① market_price_platform_quotes（970）的 PK 是
--     (dimension, canonical_name, platform_id) ⇒ **阶梯价（tier）互相覆盖**：
--     同一模型同一平台的「输入32K+」与「≤32K」两档只能留一行 ⇒
--     上下文阶梯价在采集侧被压平（salesdecorate 侧有 258 个 tier / 2600+ 行
--     带 tier 的观测，源库 price_observation.tier 事实存在）。
--  ② 源侧 price_observation 有 **cache_hit_price**（9524/19181 = 49.7% 有值）
--     ⇒ 缓存命中价是真实成本的大头，而网关侧**一个列都没有** ⇒ 对比面看不到。
--  ③ market_price_reference（940）的 canonical_name 历史上存的是
--     **salesdecorate 的 canonical_key**（如 `openai:-gpt-4o`），而
--     supplier_market_position（评分面）存的是**网关 canonical 名**
--     ⇒ 两个名字空间交集仅 218/1746 = 12.5% ⇒ 绝大部分「我方价格 vs 市场地板」
--     **没有地板可比**。本迁移清掉这批身份漂移的遗留行，
--     让下一次同步以「网关 canonical 名」重新落地（同步器侧同批改造）。
--
-- 本迁移只做三件事（**不写任何价格数值**）：
--  ① 970 表加 4 列：tier / model_kind / cache_hit_per_1m / cny_cache_hit_per_1m；
--  ② 970 表 PK 改为 (dimension, canonical_name, platform_id, tier)；
--  ③ 清理 940 里 canonical_id 与 models_canonical 名字不一致的遗留行
--     （= 旧身份口径的行；否则新同步按网关名落地时会撞 uq_mpr_canonical_id）。
--
-- 红线：只读/改**行情面**表（market_price_reference / market_price_platform_quotes）；
-- 绝不触碰 model_credit_rates / model_price_versions / credential_model_bindings
-- （路由与计费面）—— 行情价禁止回流路由。
--
-- Down: 974_market_price_identity_and_tier.down.sql
--
-- ── 超时预算（门 TestHeavyDDLModernMigrationsDeclareMeasuredBudget）─────
--   本文件含 2 条**非并发 CREATE INDEX**（970 表）+ 1 条 PRIMARY KEY 重建
--   （建唯一索引，扫全表）+ 1 条全表 DELETE ⇒ 命中「全表扫描类 DDL」判据。
--   ⇒ 按 761 先例（实测 18.9s ⇒ 抬 10min）声明 **10min** 预算。
--   实测（目标环境 = 211，acc-pg17 / PostgreSQL 17 / arm64 Docker，
--        2026-11-17 16:5x，psql \timing，**在真实表上跑完 ROLLBACK**，量的是
--        「已存在的 970 表（0 行）+ 940 表（1085 行）」的真实形态）：
--     ALTER TABLE ADD COLUMN ×4      7.350 ms
--     DROP CONSTRAINT (PK)            8.659 ms
--     ADD CONSTRAINT (PK 重建)        3.851 ms
--     DELETE（940 身份漂移遗留行）    9.347 ms（**命中 32 行**）
--     合计 < 40 ms
--   ⇒ 与 10min 预算差四个数量级；余量留给「迁移窗口内网关仍在写」
--     造成的索引维护与锁等待（761 的 18.9s 正是在那种争用下越过了角色级上限）。
-- ===========================================================================

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- ── ① 970 表加列（阶梯 / 类别 / 缓存命中价）────────────────────────────
-- tier：salesdecorate 的 price_observation.tier（上下文阶梯 / Priority / Flex
--        等）。★ 之前 tier 只活在采集侧 ⇒ 阶梯价在 940 聚合层被压平成
--   「各 tier 的最小值」⇒ 长上下文请求的真实成本被系统性低估。
-- model_kind：源侧模型类别（文本/对话 / 多模态 / 推理 / 向量 / 图像 / 语音…）
--        ⇒ 读面可按类别过滤（网关自己也服务 embedding 模型 ⇒ 不按类别
--   一刀切丢弃，只做可筛选）。
-- cache_hit_per_1m / cny_cache_hit_per_1m：缓存命中价（源侧 cache_hit_price
--        + 采集时点 CNY 折算）。NULL≠0：源侧没给就是 NULL（未知 ≠ 免费）。
ALTER TABLE public.market_price_platform_quotes
    ADD COLUMN IF NOT EXISTS tier                 text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS model_kind           text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS cache_hit_per_1m     numeric(14,6),
    ADD COLUMN IF NOT EXISTS cny_cache_hit_per_1m numeric(14,6);

COMMENT ON COLUMN public.market_price_platform_quotes.tier IS
    'Price tier from salesdecorate price_observation.tier (context-length band / Priority / Flex). Empty = default tier.';
COMMENT ON COLUMN public.market_price_platform_quotes.model_kind IS
    'Model category from salesdecorate model.model_kind (文本/对话 | 多模态 | 推理(深度思考) | 向量/Embedding | ...).';
COMMENT ON COLUMN public.market_price_platform_quotes.cache_hit_per_1m IS
    'Native-currency cache-hit price per 1M tokens; NULL = unknown (NULL≠0).';

-- ── ② PK 并入 tier（阶梯价不再互相覆盖）───────────────────────────────
-- ★ 先去重再改 PK：若表里已有同 (dimension, canonical_name, platform_id, tier)
--   的重复行，ADD PRIMARY KEY 会直接失败（23505）。当前线上该表 0 行，
--   但同步器是 DELETE+INSERT 快照语义 ⇒ 仍按「可能已有数据」做幂等防护。
DELETE FROM public.market_price_platform_quotes a
      USING public.market_price_platform_quotes b
 WHERE a.ctid < b.ctid
   AND a.dimension = b.dimension
   AND a.canonical_name = b.canonical_name
   AND a.platform_id = b.platform_id
   AND a.tier = b.tier;

ALTER TABLE public.market_price_platform_quotes
    DROP CONSTRAINT IF EXISTS market_price_platform_quotes_pkey;
ALTER TABLE public.market_price_platform_quotes
    ADD CONSTRAINT market_price_platform_quotes_pkey
    PRIMARY KEY (dimension, canonical_name, platform_id, tier);

-- ── ③ 索引（970 已有同名索引 ⇒ IF NOT EXISTS 幂等重放）────────────────
CREATE INDEX IF NOT EXISTS idx_mppq_canon
    ON public.market_price_platform_quotes (canonical_name, dimension);
CREATE INDEX IF NOT EXISTS idx_mppq_platform
    ON public.market_price_platform_quotes (platform_id, run_date DESC);

-- ── ④ 清理 940 身份漂移遗留行 ─────────────────────────────────────────
-- ★ 旧口径：market_price_reference.canonical_name = salesdecorate 的
--   canonical_key（带 `vendor:` 命名空间前缀），canonical_id 却是网关的 id
--   ⇒ 「名字不是网关名、id 是网关 id」的矛盾行。
-- ★ 这些行必须在同步器按「网关 canonical 名」重新落地**之前**清掉：
--   否则新行（canonical_name = 网关名）会撞 uq_mpr_canonical_id
--   （UNIQUE (canonical_id, dimension) WHERE canonical_id IS NOT NULL）⇒ 整事务 abort。
DELETE FROM public.market_price_reference mpr
 WHERE mpr.canonical_id IS NOT NULL
   AND NOT EXISTS (
       SELECT 1 FROM public.models_canonical mc
        WHERE mc.id = mpr.canonical_id
          AND lower(mc.canonical_name) = lower(mpr.canonical_name));

-- 旧口径遗留的「带命名空间前缀的未落定行」：源 key 形如 `openai:-gpt-4o`。
-- ★ 判据不能是「名字含 ':'」——网关自己有 10 个 Ollama 风格名
--   （如 `qwen2.5:7b`、`claude-opus-5-5:high`）⇒ 含 ':' ≠ 一定是遗留行。
--   ⇒ 判据＝「含 ':' 且冒号后紧跟 '-'」——那是采集侧 `vendor:-name` 形态，
--   网关自身的 10 个 ':' 名一个都不长这样（实测 211 全量核对）。
DELETE FROM public.market_price_reference mpr
 WHERE mpr.canonical_id IS NULL
   AND mpr.canonical_name ~ ':-';

INSERT INTO public.schema_migrations (version, description)
VALUES ('974', 'market price identity alignment: platform-quote tier/model_kind/cache-hit columns, tier-aware PK, legacy canonical-name drift cleanup')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;

-- ===========================================================================
-- File:          sql/migrations/startup/959_market_dual_floor_a17.sql
-- Migration:     959
-- Purpose:       「运营管理/价格计费体系」总体设计定稿（2026-11-11）裁决一
--                A-17 双币市场价的地基改造（定稿 §3.3/§4.1 的 M1-T2，编号
--                因 957/958 被并行轮占用顺延为 959）：
--                ① market_price_reference 双地板 ALTER（17 列，加列不重建）：
--                  · P1 原厂刊例价升级为「区域原生币」——vendor_list_currency
--                    + 原生 vendor_list_in/out + 双向折算快照列（CNY/USD）；
--                  · P2a 海外地板（USD 原生）+ 折 CNY 快照列；
--                  · P2b 国内地板（CNY 原生）+ 折 USD 快照列；
--                  · 旧 market_min_* 语义钉死为「综合地板」＝双地板按快照
--                    汇率折到 CNY 的**逐轴全局最低**（与 940 旧值同口径，币种
--                    恒 CNY ⇒ overview/941 视图/报价 P2 轴零漂移）；
--                    定稿建议的「currency = vendor_list_currency 镜像」被
--                    **有意偏离**（镜像会破坏 941 视图旧门与消费方标注的
--                    币种一致性；镜像语义由 vendor_list_currency 新列承载，
--                    偏离理由钉在本文件 currency 列 COMMENT）。
--                ② supplier_market_position 双读数列（5 列）：
--                  vs_overseas / vs_domestic / market_ref_used / fx_used /
--                  vendor_ref_currency —— 评分 job（定稿 M2-T3）落库面。
--                ③ market_platform_registry 补种 12 平台：
--                  958 种子取自网关 provider_catalog（34 平台），与同步源
--                  salesdecorate.platform（25 平台）对拍缺 12 个
--                  （aws-bedrock / azure-foundry / deepinfra / baidu-qianfan /
--                  bailian / huawei-modelarts / iflytek-spark /
--                  jdcloud-joybuilder / ppio / qiniu / tencent-hunyuan /
--                  volcengine-ark —— 2026-11-11 211 acc-pg17 两库实测）。
--                  缺口后果：A-17 拒绝族 platform_unmapped_region 会把一半
--                  源平台拒掉 ⇒ 双地板失明。958 已 applied(245/252) 字节冻结
--                  （migration_version_unique_test grandfathered）⇒ 补种只能
--                  走新迁移，ON CONFLICT DO NOTHING 幂等。
--                ④ v_supplier_price_vs_baseline v3：末尾追加双地板投影 +
--                  双 C2 读数 + market_ref_used（844/941 同款「只追加末尾
--                  列」纪律；前 33 列名/序/型逐字不变；两个既有 LATERAL 的
--                  JOIN 条件 / ORDER BY / LIMIT 1 一字不改）。
--
-- 与 844/941/949 完全同款纪律：
--   · CREATE OR REPLACE VIEW 只追加末尾列 ⇒ 既有 **30 列（基线 = 树的最新视图
--     定义 949**，= 941 的 29 + 949 末尾追加的 provider_model_id）位置/类型/
--     顺序逐字不变（消费方 sales/service.go、sales/quote.go、
--     bg/routing_health_checks.go 全部显式列名，无 SELECT *）；
--     ★ 211 部署实测教训（2026-11-12）：按 941 文本（29 列）重写会撞
--     「cannot change name of view column provider_model_id」——**重写视图前
--     必须以树的最新定义（最后一个改它的迁移）为基线**，不是头注或更早的版本；
--   · 基线 JOIN（LATERAL mc）与市场 LATERAL（mpr）的命中条件不变，
--     mpr LATERAL 只扩充 SELECT 列清单（不改 WHERE/ORDER/LIMIT）；
--   · 双 C2 读数（vs_overseas / vs_domestic）只在「绑定币种 == 该区原生币」
--     时原生比较（同币零 FX —— A-17 原则 4）；跨区折算只发生在行情层
--     折算列（快照 fx_rate），视图不做任何隐式折算。
--   · 无 VALIDATE CONSTRAINT / 无 CREATE INDEX ⇒ 不触发 DDL 预算门；
--     ADD COLUMN DEFAULT 在 689 行存量（211 实测）下毫秒级。
--
-- Down: 959_market_dual_floor_a17.down.sql（剥列 + 撤补种 + 视图回 941 版）
-- ===========================================================================

BEGIN;

-- ★ 锁守卫照 941 形态；★ 实测（2026-11-11 211 acc-pg17：market_price_reference
--   689 行 / supplier_market_position 0 行）：加列均为元数据级或毫秒级，
--   无全表扫描、无索引重建。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- ── ① market_price_reference：双币双地板（17 列，加列不重建） ───────────
ALTER TABLE public.market_price_reference
    -- P1 原厂刊例价：原生币种 + 双向折算（快照 fx_rate）
    ADD COLUMN IF NOT EXISTS vendor_list_currency        text NOT NULL DEFAULT 'USD',
    ADD COLUMN IF NOT EXISTS vendor_list_cny_in_per_1m   numeric(14,6),
    ADD COLUMN IF NOT EXISTS vendor_list_cny_out_per_1m  numeric(14,6),
    ADD COLUMN IF NOT EXISTS vendor_list_usd_in_per_1m   numeric(14,6),
    ADD COLUMN IF NOT EXISTS vendor_list_usd_out_per_1m  numeric(14,6),
    -- P2a 海外市场地板（USD 原生）
    ADD COLUMN IF NOT EXISTS overseas_min_in_per_1m      numeric(14,6),
    ADD COLUMN IF NOT EXISTS overseas_min_out_per_1m     numeric(14,6),
    ADD COLUMN IF NOT EXISTS overseas_min_platform_id    text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS overseas_min_platform_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS overseas_min_cny_in_per_1m  numeric(14,6),
    ADD COLUMN IF NOT EXISTS overseas_min_cny_out_per_1m numeric(14,6),
    -- P2b 国内市场地板（CNY 原生）
    ADD COLUMN IF NOT EXISTS domestic_min_in_per_1m      numeric(14,6),
    ADD COLUMN IF NOT EXISTS domestic_min_out_per_1m     numeric(14,6),
    ADD COLUMN IF NOT EXISTS domestic_min_platform_id    text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS domestic_min_platform_count integer NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS domestic_min_usd_in_per_1m  numeric(14,6),
    ADD COLUMN IF NOT EXISTS domestic_min_usd_out_per_1m numeric(14,6);

COMMENT ON COLUMN public.market_price_reference.currency IS
    'A-17 落地口径（959）：本列描述 market_min_*（综合地板）的币种 = 综合地板折算基准 CNY，保持写 ''CNY'' —— 定稿原建议「= vendor_list_currency 镜像」被本轮**有意偏离**：941 视图的 market_position 旧门（cmb.currency IS DISTINCT FROM mpr.currency ⇒ NULL）与全部既有消费者的标注都按「本列=market_min 的币种」工作；镜像语义由新列 vendor_list_currency 承载（厂商原生币）。';
COMMENT ON COLUMN public.market_price_reference.vendor_list_currency IS
    'P1 原厂刊例价的原生币（registry 裁定）；vendor_list_in/out_per_1m 即该币种的值。折算列（vendor_list_cny_*/usd_*）只是派生视图，原币列是事实源（A-17 工程红线）。';
COMMENT ON COLUMN public.market_price_reference.vendor_list_cny_in_per_1m IS
    'P1 折 CNY 表达（快照 fx_rate），派生列；NULL = 无原厂价或快照 fx 缺失（未知，不默认）。';
COMMENT ON COLUMN public.market_price_reference.vendor_list_usd_in_per_1m IS
    'P1 折 USD 表达（快照 fx_rate），派生列。';
COMMENT ON COLUMN public.market_price_reference.overseas_min_in_per_1m IS
    'P2a 海外地板：overseas 区（registry 裁定）非 vendor 非 我方 平台的 USD 原生最低价。NULL = 该区无观测。';
COMMENT ON COLUMN public.market_price_reference.overseas_min_platform_count IS
    'P2a 独立平台数；<3 时该地板降级为参考（≥3 才可作判据，告警 market_ref_thin_coverage）。';
COMMENT ON COLUMN public.market_price_reference.overseas_min_cny_in_per_1m IS
    'P2a 折 CNY 表达（快照 fx_rate）。';
COMMENT ON COLUMN public.market_price_reference.domestic_min_in_per_1m IS
    'P2b 国内地板：domestic 区非 vendor 非 我方 平台的 CNY 原生最低价。NULL = 该区无观测。';
COMMENT ON COLUMN public.market_price_reference.domestic_min_usd_in_per_1m IS
    'P2b 折 USD 表达（快照 fx_rate）。';
COMMENT ON COLUMN public.market_price_reference.market_min_in_per_1m IS
    '959 语义钉死：综合地板 = 双地板按快照 fx 折到 CNY 后的**逐轴全局最低**（in/out 各自取 min；与 940 旧值逐字节同口径 ⇒ overview/941 视图/报价 P2 轴零漂移），币种恒 CNY（= market_min_cny_* 同值双列：market_min_* 是 940 原列位，market_min_cny_* 是定稿命名的 CNY 表达位）。区域原生事实在 overseas_min_*（USD）/ domestic_min_*（CNY）两列组。';

-- ── ② supplier_market_position：双读数列（评分 job 落库面，定稿 M2-T3） ──
ALTER TABLE public.supplier_market_position
    ADD COLUMN IF NOT EXISTS market_position_vs_overseas numeric(10,4),
    ADD COLUMN IF NOT EXISTS market_position_vs_domestic numeric(10,4),
    ADD COLUMN IF NOT EXISTS market_ref_used             text NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS fx_used                     numeric(12,6),
    ADD COLUMN IF NOT EXISTS vendor_ref_currency         text NOT NULL DEFAULT 'USD';

COMMENT ON COLUMN public.supplier_market_position.market_position_vs_overseas IS
    'C2 双读数之一：vs 海外地板（USD 侧）。绑定 USD 时同币原生比较（零 FX）；绑定 CNY 时按快照 fx 折算，fx_used 记录。NULL = 地板缺失/计数不足/币种不可比。';
COMMENT ON COLUMN public.supplier_market_position.market_position_vs_domestic IS
    'C2 双读数之二：vs 国内地板（CNY 侧）。绑定 CNY 时同币原生比较（零 FX）。';
COMMENT ON COLUMN public.supplier_market_position.market_ref_used IS
    '主列（market_position）用的地板：overseas | domestic（= 绑定同区地板，路由 MarketAdvantage 同源）；'''' = 未用。';
COMMENT ON COLUMN public.supplier_market_position.fx_used IS
    '本次折算所用快照汇率；NULL = 同币零折算。';
COMMENT ON COLUMN public.supplier_market_position.vendor_ref_currency IS
    'C1 所用厂商参考价的原生币。';

-- ── ③ market_platform_registry 补种 12 平台（对拍 salesdecorate.platform）──
-- 对拍证据（2026-11-11，211 acc-pg17，salesdecorate 库 platform 表 25 行，
-- 全部 price_scope='标准按量'/unit='per_1M_tokens' 观测 11,742 行，币种与
-- 区域原生币 100% 对齐：海外 9 平台全 USD、国内 16 平台全 CNY）：
--   ∩ 958 已有 13：anthropic deepseek fireworks google-gemini minimax
--     moonshot openai openrouter sensenova siliconflow stepfun together zhipu
--   ✗ 958 缺 12（本段补种）：海外 USD ×3（aws-bedrock / azure-foundry /
--     deepinfra）+ 国内 CNY ×9（baidu-qianfan / bailian / huawei-modelarts /
--     iflytek-spark / jdcloud-joybuilder / ppio / qiniu / tencent-hunyuan /
--     volcengine-ark）。
-- is_ours 维持 958 口径（全 false，待运营核对后手工 UPDATE）。
INSERT INTO public.market_platform_registry
    (platform_id, platform_name, region, native_currency, is_ours, enabled, notes)
VALUES
    ('aws-bedrock',       'AWS Bedrock',            'overseas', 'USD', false, true, '959 对拍补种（salesdecorate 源平台）'),
    ('azure-foundry',     'Azure Foundry',          'overseas', 'USD', false, true, '959 对拍补种（salesdecorate 源平台）'),
    ('deepinfra',         'DeepInfra',              'overseas', 'USD', false, true, '959 对拍补种（salesdecorate 源平台）'),
    ('baidu-qianfan',     '百度千帆',               'domestic', 'CNY', false, true, '959 对拍补种（salesdecorate 源平台；vendor=百度）'),
    ('bailian',           '阿里云百炼',             'domestic', 'CNY', false, true, '959 对拍补种（salesdecorate 源平台；vendor=阿里云）'),
    ('huawei-modelarts',  '华为 ModelArts',         'domestic', 'CNY', false, true, '959 对拍补种（salesdecorate 源平台）'),
    ('iflytek-spark',     '科大讯飞星火',           'domestic', 'CNY', false, true, '959 对拍补种（salesdecorate 源平台；vendor=科大讯飞）'),
    ('jdcloud-joybuilder','京东云 JoyBuilder',      'domestic', 'CNY', false, true, '959 对拍补种（salesdecorate 源平台）'),
    ('ppio',              'PPIO 派欧云',            'domestic', 'CNY', false, true, '959 对拍补种（salesdecorate 源平台）'),
    ('qiniu',             '七牛云',                 'domestic', 'CNY', false, true, '959 对拍补种（salesdecorate 源平台）'),
    ('tencent-hunyuan',   '腾讯混元',               'domestic', 'CNY', false, true, '959 对拍补种（salesdecorate 源平台；vendor=腾讯）'),
    ('volcengine-ark',    '火山方舟',               'domestic', 'CNY', false, true, '959 对拍补种（salesdecorate 源平台；vendor=字节跳动）')
ON CONFLICT (platform_id) DO NOTHING;

-- ── ④ v_supplier_price_vs_baseline v3：只追加末尾列（基线 = 949 的 30 列逐字不动） ──
CREATE OR REPLACE VIEW public.v_supplier_price_vs_baseline AS
SELECT
    cmb.credential_id,
    pm.raw_model_name,
    COALESCE(pm.canonical_raw_name, mc.canonical_name) AS canonical_name,
    COALESCE(p.id::text, '')                         AS provider_name,
    COALESCE(cmb.currency, 'USD')                    AS supplier_currency,
    COALESCE(cmb.billing_mode, 'per_token')          AS billing_mode,
    cmb.unit_price_in_per_1m                        AS supplier_in_per_1m,
    cmb.unit_price_out_per_1m                       AS supplier_out_per_1m,
    cmb.pricing_source                              AS supplier_pricing_source,
    cmb.pricing_updated_at                          AS supplier_pricing_updated_at,
    mc.baseline_price_currency                      AS baseline_currency,
    mc.baseline_input_price_per_1m                  AS baseline_in_per_1m,
    mc.baseline_output_price_per_1m                 AS baseline_out_per_1m,
    -- 倍率：供应商实付 / 原厂标准。>1 = 比原厂贵，<1 = 有折扣。
    -- 只有在**币种相同**时才算得出来，所以下面同时给出可比性标记。
    CASE
      WHEN mc.baseline_input_price_per_1m IS NULL
        OR mc.baseline_input_price_per_1m = 0
        OR COALESCE(cmb.currency, 'USD') IS DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD')
      THEN NULL
      ELSE round(cmb.unit_price_in_per_1m / mc.baseline_input_price_per_1m, 4)
    END                                             AS input_price_ratio,
    CASE
      WHEN mc.baseline_output_price_per_1m IS NULL
        OR mc.baseline_output_price_per_1m = 0
        OR COALESCE(cmb.currency, 'USD') IS DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD')
      THEN NULL
      ELSE round(cmb.unit_price_out_per_1m / mc.baseline_output_price_per_1m, 4)
    END                                             AS output_price_ratio,
    -- 币种不一致时必须显眼：拿 CNY 的供应商价去比 USD 的基准价，出来的
    -- 偏差是纯噪声，但它在报表里长得和真偏差一模一样。
    (COALESCE(cmb.currency, 'USD')
       IS NOT DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD'))  AS currency_comparable,
    (mc.baseline_input_price_per_1m IS NOT NULL)    AS has_baseline,
    mc.baseline_price_fetched_at                    AS baseline_fetched_at,
    -- ── 844：两个缓存基准价 ─────────────────────────────────────────────
    mc.baseline_cache_read_price_per_1m             AS baseline_cache_read_per_1m,
    mc.baseline_cache_write_price_per_1m            AS baseline_cache_write_per_1m,
    CASE
      WHEN mc.baseline_cache_read_price_per_1m IS NULL
        OR mc.baseline_cache_read_price_per_1m = 0
        OR cmb.cache_read_price_per_1m IS NULL
        OR COALESCE(cmb.currency, 'USD') IS DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD')
      THEN NULL
      ELSE round(cmb.cache_read_price_per_1m / mc.baseline_cache_read_price_per_1m, 4)
    END                                             AS cache_read_price_ratio,
    CASE
      WHEN mc.baseline_cache_write_price_per_1m IS NULL
        OR mc.baseline_cache_write_price_per_1m = 0
        OR cmb.cache_write_price_per_1m IS NULL
        OR COALESCE(cmb.currency, 'USD') IS DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD')
      THEN NULL
      ELSE round(cmb.cache_write_price_per_1m / mc.baseline_cache_write_price_per_1m, 4)
    END                                             AS cache_write_price_ratio,
    -- ── 941：市场口径（P2 市场最低 + C2 位势 + C3 置信度门） ─────────────
    mpr.currency                                    AS market_ref_currency,
    mpr.market_min_in_per_1m                        AS market_ref_in_per_1m,
    mpr.market_min_out_per_1m                       AS market_ref_out_per_1m,
    mpr.market_min_platform_id                      AS market_ref_platform_id,
    mpr.market_min_platform_count                   AS market_ref_platform_count,
    -- C2 = 市场最低价 / 供应商价（blended）。>1 = 我们比市场便宜。
    -- 只在币种可比且两侧都有正价时算；否则 NULL（与 baseline 倍率同纪律）。
    CASE
      WHEN mpr.currency IS NULL
        OR COALESCE(cmb.currency, 'USD') IS DISTINCT FROM mpr.currency
        OR COALESCE(cmb.unit_price_in_per_1m, 0) + COALESCE(cmb.unit_price_out_per_1m, 0) <= 0
        OR COALESCE(mpr.market_min_in_per_1m, 0) + COALESCE(mpr.market_min_out_per_1m, 0) <= 0
      THEN NULL
      ELSE round(
        (mpr.market_min_in_per_1m + mpr.market_min_out_per_1m)
        / (cmb.unit_price_in_per_1m + cmb.unit_price_out_per_1m), 4)
    END                                             AS market_position,
    -- C3 价格置信度门（不是分）：口径镜像 offer_price_looks_like_placeholder
    -- （对称正价=占位候选 / 0/0=empty 非缺陷 / 币种不在已知集=unknown_currency）。
    CASE
      WHEN COALESCE(cmb.currency, 'USD') NOT IN ('USD', 'CNY')
        THEN 'unknown_currency'
      WHEN COALESCE(cmb.unit_price_in_per_1m, 0) = 0
       AND COALESCE(cmb.unit_price_out_per_1m, 0) = 0
        THEN 'empty'
      WHEN cmb.unit_price_in_per_1m IS NOT NULL
       AND cmb.unit_price_in_per_1m = cmb.unit_price_out_per_1m
        THEN 'suspect_symmetric'
      ELSE 'trusted'
    END                                             AS price_confidence,
    -- ── 949 新增：binding 身份（供给登记 942 的精确 join 键） ─────────────
    cmb.provider_model_id                           AS provider_model_id,
    -- ── 959 新增（A-17 双币制）：双地板原生投影 + 双 C2 读数 ─────────────
    mpr.overseas_min_in_per_1m                      AS overseas_ref_in_per_1m,
    mpr.overseas_min_out_per_1m                     AS overseas_ref_out_per_1m,
    mpr.overseas_min_platform_id                    AS overseas_ref_platform_id,
    mpr.overseas_min_platform_count                 AS overseas_ref_platform_count,
    mpr.domestic_min_in_per_1m                      AS domestic_ref_in_per_1m,
    mpr.domestic_min_out_per_1m                     AS domestic_ref_out_per_1m,
    mpr.domestic_min_platform_id                    AS domestic_ref_platform_id,
    mpr.domestic_min_platform_count                 AS domestic_ref_platform_count,
    -- C2 双读数（A-17 原则 4「同区比较零 FX」）：vs_overseas 只在绑定 USD 且
    -- 海外地板两侧有正价时原生比较；vs_domestic 只在绑定 CNY 且国内地板两侧
    -- 有正价时原生比较。其余 ⇒ NULL（不猜币种、不做隐式折算）。
    CASE
      WHEN COALESCE(cmb.currency, 'USD') <> 'USD'
        OR COALESCE(cmb.unit_price_in_per_1m, 0) + COALESCE(cmb.unit_price_out_per_1m, 0) <= 0
        OR COALESCE(mpr.overseas_min_in_per_1m, 0) + COALESCE(mpr.overseas_min_out_per_1m, 0) <= 0
      THEN NULL
      ELSE round(
        (mpr.overseas_min_in_per_1m + mpr.overseas_min_out_per_1m)
        / (cmb.unit_price_in_per_1m + cmb.unit_price_out_per_1m), 4)
    END                                             AS market_position_vs_overseas,
    CASE
      WHEN COALESCE(cmb.currency, 'USD') <> 'CNY'
        OR COALESCE(cmb.unit_price_in_per_1m, 0) + COALESCE(cmb.unit_price_out_per_1m, 0) <= 0
        OR COALESCE(mpr.domestic_min_in_per_1m, 0) + COALESCE(mpr.domestic_min_out_per_1m, 0) <= 0
      THEN NULL
      ELSE round(
        (mpr.domestic_min_in_per_1m + mpr.domestic_min_out_per_1m)
        / (cmb.unit_price_in_per_1m + cmb.unit_price_out_per_1m), 4)
    END                                             AS market_position_vs_domestic,
    -- 绑定同区地板标识（A-17 C2 主列 = 绑定同区地板；评分 job 与路由
    -- MarketAdvantage 同源读这里）。
    CASE
      WHEN COALESCE(cmb.currency, 'USD') = 'USD' THEN 'overseas'
      WHEN COALESCE(cmb.currency, 'USD') = 'CNY' THEN 'domestic'
      ELSE ''
    END                                             AS market_ref_used
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
JOIN credentials c      ON c.id = cmb.credential_id
LEFT JOIN providers p   ON p.id = c.provider_id
-- ★ 这个 JOIN 的条件、ORDER BY、LIMIT 1 与 826/844/941 **逐字一致**，一字未改。
LEFT JOIN LATERAL (
    SELECT mc.id, mc.canonical_name, mc.baseline_price_currency,
           mc.baseline_input_price_per_1m, mc.baseline_output_price_per_1m,
           mc.baseline_price_fetched_at,
           mc.baseline_cache_read_price_per_1m, mc.baseline_cache_write_price_per_1m
      FROM public.models_canonical mc
     WHERE mc.id = pm.canonical_id
        OR (pm.canonical_id IS NULL
            AND lower(mc.canonical_name) = lower(pm.canonical_raw_name))
     ORDER BY (pm.canonical_id IS NOT NULL AND mc.id = pm.canonical_id) DESC
     LIMIT 1
) mc ON true
-- ★ 941 的市场 LATERAL（dedup：canonical_id 解析优先，AF-8），逐字保留。
LEFT JOIN LATERAL (
    SELECT mpr.currency, mpr.market_min_in_per_1m, mpr.market_min_out_per_1m,
           mpr.market_min_platform_id, mpr.market_min_platform_count,
           mpr.overseas_min_in_per_1m, mpr.overseas_min_out_per_1m,
           mpr.overseas_min_platform_id, mpr.overseas_min_platform_count,
           mpr.domestic_min_in_per_1m, mpr.domestic_min_out_per_1m,
           mpr.domestic_min_platform_id, mpr.domestic_min_platform_count
      FROM public.market_price_reference mpr
     WHERE mpr.dimension = 'standard'
       AND (mpr.canonical_id = mc.id
            OR (mpr.canonical_id IS NULL
                AND lower(mpr.canonical_name) = lower(mc.canonical_name)))
     ORDER BY (mpr.canonical_id IS NOT NULL) DESC
     LIMIT 1
) mpr ON true;

COMMENT ON VIEW public.v_supplier_price_vs_baseline IS
    'Per (credential, model) supplier price against the vendor baseline. Ratios are NULL unless the currencies match and the baseline is set. 941 appends the market dimension (P2 minimum + C2 + C3 gate); 949 appends provider_model_id; 959 (A-17 dual currency) appends the dual regional floors in native currency plus the dual C2 readings (same-region native comparison only, zero FX in-view) and market_ref_used. Baseline = the 949 definition (30 columns) preserved byte-wise, 11 columns appended. Market FX conversion is deliberately NOT done here — the sync/scoring layer owns currency folding with snapshot fx_rate.';

INSERT INTO public.schema_migrations (version, description)
VALUES ('959', 'A-17 dual-currency market floors: market_price_reference +17 cols (vendor native currency + overseas/domestic floors + snapshot folds), supplier_market_position +5 dual-readings cols, registry +12 platforms (salesdecorate reconciliation), view v3 append dual-floor projections')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;

-- 759: report_snapshots 补齐最细粒度（grain）维度列（2026-09-29 对帐页多维筛选轮）
--
-- 缺陷：读面（admin/report_rollup.go）只暴露 provider_id / tenant_id /
-- raw_model_name 三个维度，而对帐页要求的凭据、用户、apikey 三类筛选条件
-- **在 schema 层无处可取**——不是读面没实现，是快照表根本没落这三列。
-- 实测（本地真库 llm_gateway，usage_facts 近 30 天）：credential_id 有 52
-- 个取值、api_key_id 有 98 个、person（end_user_id/person_hash 回落链）
-- 有 19 个，全部只存在于 usage_facts 的请求级行上。
--
-- 为什么是「加列 + 新 scope」而不是给六个既有 scope 各加一层：
--   既有六 scope 是**边缘汇总**（daily_total / by_provider / by_model /
--   internal_tenant / internal_person / internal_model），任意**两个**维度
--   同时过滤就无法从任何单 scope 折叠出来（读面 report.go 的 filter.empty()
--   分支树正是这个限制的化石）。要支持任意维度组合，唯一自洽的做法是让
--   快照存**最细粒度**的行，读面用 GROUPING SETS 一次性算出全部汇总层级。
--
-- 新增两列 + 一列文本：
--   · credential_id BIGINT —— usage_facts.credential_id（86 个凭据）
--   · api_key_id     BIGINT —— usage_facts.api_key_id（171 个 apikey）
--   · person         TEXT   —— end_user_id 优先，缺失回落 'person:'+person_hash，
--                               双缺回落 'unknown'（与 internal_person 同款回落
--                               链，写面 domains/reportrollup/rollup.go 单一实现）
--
-- scope 枚举扩员（TEXT 无 CHECK，靠 SSOT 注记约束）：
--   · daily_grain   scope_key = 规范编码(provider,credential,api_key,tenant,person)，
--                   raw_model_name = 出站模型名，覆盖**全部流量类**（供应商成本口径）
--   · internal_grain 同粒度，仅 **business 流量**（内部计费口径）
--   两者的存在使读面「任意维度组合过滤 → 总计 = Σ 各分组」恒成立（此前只有
--   单维过滤才成立）。旧 scope 行一律保留：读面按日期回落（某日无 grain 行
--   则该日走旧 scope），迁移当日不必回填历史即可出图。
--
-- ## 顶层执行（必读，见 758 文件头）
-- 把 DDL 包进 DO $$ … EXECUTE $ddl$…$ddl$; $$ 会**静默无效**——不报错、
-- 列没加、前后自校验照常通过。所以 DDL 一律顶层，下面只用 DO 块做纯
-- SELECT 断言（那是有效的）。

-- ── 前置断言：这些列应当尚未存在 ────────────────────────────────────────────
DO $pre$
DECLARE
    n int;
BEGIN
    SELECT count(*) INTO n
      FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'report_snapshots'
       AND column_name IN ('credential_id', 'api_key_id', 'person');
    IF n > 0 THEN
        RAISE NOTICE 'report_snapshots 已含 % 个 grain 列 — 本迁移幂等，仍继续', n;
    ELSE
        RAISE NOTICE 'report_snapshots 尚无 grain 列 — 正常首跑';
    END IF;
END
$pre$;

-- ── 顶层执行（必须顶层）────────────────────────────────────────────────────
ALTER TABLE public.report_snapshots
    ADD COLUMN IF NOT EXISTS credential_id BIGINT,
    ADD COLUMN IF NOT EXISTS api_key_id     BIGINT,
    ADD COLUMN IF NOT EXISTS person         TEXT;

-- grain 日期区间索引。GrainSQL 的 WHERE 是 `scope = ANY(ARRAY['daily_grain'])`
-- + report_date 区间，而 idx_report_snapshots_scope_date 的前导列是 scope：
-- 对 ANY 数组等值，PG 的行数估算按「scope 列的 distinct 值数」摊分，本列 8 个
-- scope 值里 7 个与本次查询无关 ⇒ 估算量远大于真实匹配量，规划器据此选
-- Parallel Seq Scan 扫**整张表**，成本从 O(区间内行数) 退化成 O(表大小)。
-- partial 谓词是精确的（scope = 单值），估算即区间内真实行数。
--
-- 实测（本地真库合成集 366 天 = 680028 行 grain，tools/benchreport，
-- span=7，取热缓存重复中位数；带索引 vs NO_INDEX=1 同一数据集对照）：
--   view=provider detail=false   22ms  vs  43ms
--   view=provider detail=true    39ms  vs  49ms
--   view=internal detail=false   21ms  vs  37ms
--   view=internal detail=true    52ms  vs  60ms
-- 即汇总口径约 2×，明细口径接近打平——绝对耗时被本机 128MB shared_buffers
-- 与慢存储主导，不宜当生产数字用。留这两个索引的理由是**增长形态**而非当前
-- 耗时：无索引时单次成本随表总行数线性上升（当时 825ms 那次正是冷缓存 seq
-- scan 扫 22 万行被 filter 掉），有索引时只随区间天数上升。
-- 不做 INCLUDE 覆盖：19 个 INCLUDE 列会把索引撑到行宽的数倍，实测
-- Heap Fetches=0 后在本机仍无可测收益，无法与写放大相抵。
CREATE INDEX IF NOT EXISTS idx_report_snapshots_grain_date
    ON public.report_snapshots (report_date)
    WHERE scope = 'daily_grain';
CREATE INDEX IF NOT EXISTS idx_report_snapshots_internal_grain_date
    ON public.report_snapshots (report_date)
    WHERE scope = 'internal_grain';

CREATE INDEX IF NOT EXISTS idx_report_snapshots_credential_date
    ON public.report_snapshots (credential_id, report_date DESC)
    WHERE scope IN ('daily_grain', 'internal_grain');
CREATE INDEX IF NOT EXISTS idx_report_snapshots_api_key_date
    ON public.report_snapshots (api_key_id, report_date DESC)
    WHERE scope IN ('daily_grain', 'internal_grain');

COMMENT ON COLUMN public.report_snapshots.credential_id IS
    'grain scopes only: usage_facts.credential_id (outbound provider credential)';
COMMENT ON COLUMN public.report_snapshots.api_key_id IS
    'grain scopes only: usage_facts.api_key_id (inbound gateway API key)';
COMMENT ON COLUMN public.report_snapshots.person IS
    'grain/internal_person scopes: usage_facts.end_user_id, falling back to ''person:''||person_hash then ''unknown''';

COMMENT ON COLUMN public.report_snapshots.scope IS
    'daily_total | daily_by_provider | daily_by_model | internal_tenant | internal_person | internal_model | daily_grain | internal_grain; provider-facing scopes include all traffic classes, internal scopes are business traffic only. *_grain scopes carry the finest dimension tuple (provider/credential/api_key/tenant/person x outbound model) so the read face can fold any dimension combination';

-- ── 后置断言：读系统目录确认真的加上了；不满足即报错，绝不静默通过 ───────────
DO $post$
DECLARE
    n int;
BEGIN
    SELECT count(*) INTO n
      FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'report_snapshots'
       AND column_name IN ('credential_id', 'api_key_id', 'person');
    IF n <> 3 THEN
        RAISE EXCEPTION 'report_snapshots grain 列应 3 个，实得 % 个 — ROLLBACK', n;
    END IF;

    -- 形状也要对：credential_id/api_key_id 必须是 bigint（usage_facts 同型，
    -- 建成 text 会让 pgx 直扫 **string 与 int64 绑定行为分叉）。
    PERFORM 1 FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'report_snapshots'
       AND column_name = 'credential_id' AND data_type = 'bigint';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'report_snapshots.credential_id 类型不是 bigint — ROLLBACK';
    END IF;
    PERFORM 1 FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'report_snapshots'
       AND column_name = 'api_key_id' AND data_type = 'bigint';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'report_snapshots.api_key_id 类型不是 bigint — ROLLBACK';
    END IF;
    PERFORM 1 FROM information_schema.columns
     WHERE table_schema = 'public' AND table_name = 'report_snapshots'
       AND column_name = 'person' AND data_type = 'text';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'report_snapshots.person 类型不是 text — ROLLBACK';
    END IF;

    -- 索引同样要真的在位（CREATE INDEX IF NOT EXISTS 与迁移重复执行无害，
    -- 但列被手工删过时会静默跳过——这里显式断言）。
    PERFORM 1 FROM pg_indexes
     WHERE schemaname = 'public' AND indexname = 'idx_report_snapshots_credential_date';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'idx_report_snapshots_credential_date 未建成 — ROLLBACK';
    END IF;
    PERFORM 1 FROM pg_indexes
     WHERE schemaname = 'public' AND indexname = 'idx_report_snapshots_grain_date';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'idx_report_snapshots_grain_date 未建成 — ROLLBACK';
    END IF;
    PERFORM 1 FROM pg_indexes
     WHERE schemaname = 'public' AND indexname = 'idx_report_snapshots_internal_grain_date';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'idx_report_snapshots_internal_grain_date 未建成 — ROLLBACK';
    END IF;
    PERFORM 1 FROM pg_indexes
     WHERE schemaname = 'public' AND indexname = 'idx_report_snapshots_api_key_date';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'idx_report_snapshots_api_key_date 未建成 — ROLLBACK';
    END IF;

    RAISE NOTICE 'post-check ok: report_snapshots +credential_id/+api_key_id/+person, grain scope 注记与 4 个 partial 索引（日期区间 ×2 + 维度裁剪 ×2）就位';
END
$post$;

--
-- Name: report_snapshots; Type: TABLE; Schema: public; Owner: -
--
-- 日报表快照表（对账报表）。SSOT 规范体；startup 迁移
-- sql/migrations/startup/745_report_snapshots.sql（建表）+
-- 746_report_snapshots_internal_dims.sql（内部对帐维度补齐）+
-- 759_report_snapshots_grain_dims.sql（grain 维度列 + 最细粒度 scope）与
-- db.go 的 ensureReportSnapshots 均以本文件为准（迁移体带幂等守卫）。
--
-- 状态：消费方已落地 —— bg/report_rollup_worker.go（每日 RunHour 聚合
-- usage_facts 写入，默认 02:00 UTC，settings 键 reports.daily_rollup.hour）
-- 与 admin/report_rollup.go（区间汇总读面 + Excel 导出）。
--
-- 唯一键 (scope, scope_key, report_date, raw_model_name)：
--   - scope ∈ daily_total | daily_by_provider | daily_by_model |
--     internal_tenant | internal_person | internal_model | daily_grain |
--     internal_grain（TEXT 无 CHECK，本注记为唯一约束源）
--   - provider 面三 scope（daily_*）含全部流量类（探针/自检同样烧供应
--     商钱，对帐须全量）；internal 面三 scope 仅 business 流量（内部计
--     费口径）。
--   - daily_by_model 行：scope_key = provider_id（文本化）、raw_model_name
--     = 出站模型名（usage_facts.raw_model_name = outbound_model 回落
--     client_model）；internal_model 行：scope_key = tenant_id、
--     raw_model_name = 出站模型名；internal_person 行：scope_key =
--     len(tenant_id) + ':' + tenant_id + ':' + person（person = end_user_id，
--     缺失回落 'person:'+person_hash，双缺 'unknown'；R65 起租户编码进键
--     ——四键 UNIQUE 不含 tenant_id 列，跨租户同名人员裸键会在 ON
--     CONFLICT 中互相覆盖。02da86168 根修弃 R65 原稿的 '\x00' 分隔——
--     PG TEXT 拒绝 NUL 字节，真库 INSERT 全量 22021）；其余行
--     raw_model_name = ''（非模型维度哨兵）。
--   - 唯一键支撑 worker 的 INSERT ... ON CONFLICT DO UPDATE 幂等回填。
--   - grain 行（759 新增，daily_grain / internal_grain）：raw_model_name
--     = 出站模型名，scope_key = grainScopeKey 的规范编码（provider /
--     credential / api_key / tenant / person 五个维度，长度前缀保证无
--     分隔符歧义），三维 id 同时冗余进 provider_id / credential_id /
--     api_key_id 列，person 冗余进 person 列 —— 冗余是为了让读面能
--     **按列过滤**（scope_key 是编码串，SQL 侧不好等值匹配）。两个
--     grain scope 的存在使读面「任意维度组合过滤 → 总计 = Σ 各分组」
--     恒成立（六个旧 scope 是边缘汇总，只能单维折叠）。读面按日期回落：
--     某日无 grain 行则该日走旧 scope，迁移当日不必回填历史即可出图。

CREATE TABLE public.report_snapshots (
    id                  BIGSERIAL PRIMARY KEY,
    scope               TEXT NOT NULL,
    scope_key           TEXT NOT NULL,
    report_date         DATE NOT NULL,
    raw_model_name      TEXT NOT NULL DEFAULT '',
    granularity         TEXT NOT NULL DEFAULT 'day',
    request_count       BIGINT NOT NULL DEFAULT 0,
    success_count       BIGINT NOT NULL DEFAULT 0,
    error_count         BIGINT NOT NULL DEFAULT 0,
    input_tokens        BIGINT NOT NULL DEFAULT 0,
    output_tokens       BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens   BIGINT NOT NULL DEFAULT 0,
    cache_write_tokens  BIGINT NOT NULL DEFAULT 0,
    error_kind_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb,
    cache_hit_ratio     NUMERIC(6,4)
                            CHECK (cache_hit_ratio IS NULL OR (cache_hit_ratio >= 0 AND cache_hit_ratio <= 1)),
    estimated_cost_cents BIGINT NOT NULL DEFAULT 0,
    currency            TEXT NOT NULL DEFAULT 'USD',
    price_snapshot      JSONB NOT NULL DEFAULT '{}'::jsonb,
    provider_id         BIGINT,
    canonical_id        BIGINT,
    tenant_id           TEXT,
    -- grain scopes（daily_grain / internal_grain）专用维度；旧 scope 行留 NULL。
    -- 759 增列，凭据/apikey/用户三类筛选条件在 schema 层从此可取。
    credential_id       BIGINT,
    api_key_id          BIGINT,
    person              TEXT,
    credits_charged     BIGINT NOT NULL DEFAULT 0,
    latency_p50_ms      BIGINT NOT NULL DEFAULT 0,
    latency_p95_ms      BIGINT NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT report_snapshots_scope_key_date_raw_model_key
        UNIQUE (scope, scope_key, report_date, raw_model_name)
);

CREATE INDEX idx_report_snapshots_scope_date
    ON report_snapshots (scope, report_date DESC);

-- grain 读面的**日期区间**扫描索引（759）。读面 SQL 的 WHERE 是
-- scope = ANY(ARRAY['daily_grain']) + report_date 区间，PG 对 ANY 数组等值的
-- 行数估算按 scope 列 distinct 值数摊分（8 个 scope 里 7 个与查询无关），
-- 估算量是真实匹配量的几十倍，规划器会因此选顺序扫描扫整张表——查询成本
-- 变成 O(表大小) 而非 O(区间内行数)。partial 索引的谓词是精确的。
CREATE INDEX idx_report_snapshots_grain_date
    ON report_snapshots (report_date)
    WHERE scope = 'daily_grain';

CREATE INDEX idx_report_snapshots_internal_grain_date
    ON report_snapshots (report_date)
    WHERE scope = 'internal_grain';

-- grain 读面的维度过滤裁剪索引（759）。idx_report_snapshots_scope_date 覆盖
-- 区间扫描，这里补的是「按高基数维度过滤后聚合」：credential_id / api_key_id
-- 单列索引让 PG 走 index-only scan 并提前丢弃非匹配行。
CREATE INDEX idx_report_snapshots_credential_date
    ON report_snapshots (credential_id, report_date DESC)
    WHERE scope IN ('daily_grain', 'internal_grain');

CREATE INDEX idx_report_snapshots_api_key_date
    ON report_snapshots (api_key_id, report_date DESC)
    WHERE scope IN ('daily_grain', 'internal_grain');

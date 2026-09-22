-- 736 (Wave 3 B1, 2026-09-22): 峰谷倍率体系 — 计费落倍率列 + 共享 SQL 取档函数。
--
-- 设计差距（§3 B1）：商务承诺的峰谷倍率（高峰 3x / 一般 1x / 低谷 0.7x）全无。
-- 时段表存 settings_kv 键 maas.rate_periods（可热更，免新表），Go 侧
-- maas.ResolveRateMultiplier 与本文件的 maas_resolve_rate_multiplier()
-- 实现同一取档规则（半开区间 [start,end)；跨午夜段 [s,24)∪[0,e)），
-- 配置同源防双写漂移。两侧规则矩阵由 Go 单测（rate_periods_test.go）
-- 精确钉住；SQL 函数与 Go 的行为对拍在 deploy 冒烟时以固定时刻验证。
--
-- 列：
--   usage_ledger.rate_multiplier / usage_ledger_hot.rate_multiplier
--     -- 该请求计费时命中的倍率（Go 计费链盖章；历史行 DEFAULT 1.0 语义
--     -- = 无倍率体系，回放一致）。
--   request_logs.credits_rate_multiplier / request_logs_hot.credits_rate_multiplier
--     -- 计费回填（credits_charged 同管道）盖章的倍率；NULL = 未计费。
--     -- credits_sql.go 的估算表达式 COALESCE(...,1.0) 乘入，保持 Go/SQL
--     -- 估算同系数。
--
-- 分区族：usage_ledger / request_logs 是分区表，ADD COLUMN 自动级联到
-- 全部现有分区（2026-07-13 multimodal-token-fields 迁移同款先例，见其
-- 头注）。_hot 系为独立 heap 表，单独 ALTER。
--
-- 幂等：ADD COLUMN IF NOT EXISTS / CREATE OR REPLACE FUNCTION，重跑 no-op。

ALTER TABLE usage_ledger
  ADD COLUMN IF NOT EXISTS rate_multiplier double precision NOT NULL DEFAULT 1.0;

ALTER TABLE usage_ledger_hot
  ADD COLUMN IF NOT EXISTS rate_multiplier double precision NOT NULL DEFAULT 1.0;

ALTER TABLE request_logs
  ADD COLUMN IF NOT EXISTS credits_rate_multiplier double precision;

ALTER TABLE request_logs_hot
  ADD COLUMN IF NOT EXISTS credits_rate_multiplier double precision;

-- maas_resolve_rate_multiplier(at_time timestamptz) → 该时刻的计费倍率。
-- 配置缺省/未启用/解析失败一律 1.0（fail-open，与 Go 侧一致）：时段配置
-- 是商务让利工具，配置事故不能把计费放大 3 倍或清零。
CREATE OR REPLACE FUNCTION maas_resolve_rate_multiplier(at_time timestamptz DEFAULT now())
RETURNS double PRECISION AS $$
DECLARE
    cfg      jsonb;
    raw      text;
    tz       text;
    enabled  boolean;
    p        jsonb;
    m        double precision;
    s        time;
    e        time;
    t_local  time;
BEGIN
    SELECT value::text INTO raw FROM settings_kv WHERE key = 'maas.rate_periods';
    IF raw IS NULL THEN
        RETURN 1.0;
    END IF;
    cfg := raw::jsonb;
    -- 管理端 TypeString 通道可能存成 JSON 字符串化形态（与 B5① 词典键
    -- 同通道约束），解一层引号。
    IF jsonb_typeof(cfg) = 'object' THEN
        NULL;
    ELSIF jsonb_typeof(cfg) = 'string' THEN
        cfg := (cfg #>> '{}')::jsonb;
        IF jsonb_typeof(cfg) <> 'object' THEN
            RETURN 1.0;
        END IF;
    ELSE
        RETURN 1.0;
    END IF;

    enabled := COALESCE((cfg->>'enabled')::boolean, false);
    IF NOT enabled THEN
        RETURN 1.0;
    END IF;

    tz := COALESCE(NULLIF(cfg->>'timezone', ''), 'Asia/Shanghai');
    BEGIN
        t_local := (at_time AT TIME ZONE tz)::time;
    EXCEPTION WHEN OTHERS THEN
        t_local := (at_time AT TIME ZONE 'Asia/Shanghai')::time;
    END;

    IF jsonb_typeof(cfg->'periods') <> 'array' THEN
        RETURN 1.0;
    END IF;
    FOR p IN SELECT jsonb_array_elements(cfg->'periods')
    LOOP
        CONTINUE WHEN p->>'start' IS NULL OR p->>'end' IS NULL;
        BEGIN
            s := (p->>'start')::time;
            e := (p->>'end')::time;
        EXCEPTION WHEN OTHERS THEN
            CONTINUE;
        END;
        m := COALESCE((p->>'multiplier')::double precision, 1.0);
        IF m <= 0 THEN
            m := 1.0;
        END IF;
        IF s <= e THEN
            IF t_local >= s AND t_local < e THEN
                RETURN m;
            END IF;
        ELSE
            -- 跨午夜（如 23:00-07:00）：[s,24:00) ∪ [00:00,e)
            IF t_local >= s OR t_local < e THEN
                RETURN m;
            END IF;
        END IF;
    END LOOP;
    RETURN 1.0;
END;
$$ LANGUAGE plpgsql STABLE;

COMMENT ON FUNCTION maas_resolve_rate_multiplier(timestamptz) IS
'峰谷计费倍率取档（Wave 3 B1）。配置：settings_kv 键 maas.rate_periods
（enabled/timezone/periods[{name,start,end,multiplier}]），与 Go 侧
maas.ResolveRateMultiplier 同规则；配置缺失/禁用/异常返回 1.0。';

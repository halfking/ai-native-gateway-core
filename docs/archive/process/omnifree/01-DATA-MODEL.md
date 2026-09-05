# 数据模型设计

> 详细 schema 定义，RLS 策略，索引优化

---

## 📊 核心表设计

### 1. `free_resource_catalog` — 免费资源目录

```sql
-- 免费 LLM 资源元数据目录
CREATE TABLE public.free_resource_catalog (
    id BIGSERIAL PRIMARY KEY,
    
    -- 关联信息
    provider_code TEXT NOT NULL REFERENCES provider_catalog(code) ON DELETE CASCADE,
    model_id TEXT NOT NULL,  -- 模型标识 (如 'gpt-3.5-turbo:free', 'glm-4-flash')
    display_name TEXT NOT NULL,
    display_name_en TEXT,
    
    -- 免费类型与配额
    free_type TEXT NOT NULL CHECK (free_type IN (
        'recurring-daily', 'recurring-monthly', 'one-time-initial',
        'recurring-credit', 'recurring-uncapped', 'keyless', 'discontinued'
    )),
    monthly_tokens BIGINT DEFAULT 0,      -- 每月 token 配额
    daily_tokens BIGINT DEFAULT 0,        -- 每日 token 配额
    credit_tokens BIGINT DEFAULT 0,       -- 充值解锁配额 (如 OpenRouter $10→1000 req/day)
    
    -- 共享配额池 (跨模型/账号去重)
    pool_key TEXT,  -- 如 'openrouter-free-pool', 'siliconflow-free'
    
    -- ToS 合规
    tos_verdict TEXT NOT NULL DEFAULT 'unknown' CHECK (tos_verdict IN (
        'ok', 'caution', 'ambiguous', 'avoid', 'unknown'
    )),
    tos_notes TEXT,  -- 合规说明 (如 "官方文档明确允许 API 网关使用")
    tos_reviewed_at TIMESTAMPTZ,
    tos_reviewed_by TEXT,
    
    -- 限制约束 (JSON)
    constraints_json JSONB DEFAULT '{}'::jsonb,
    -- 示例: {"rpm": 20, "rpd": 1000, "concurrency": 5, "window": "UTC-day"}
    
    -- 发现与验证
    discovery_method TEXT DEFAULT 'manual' CHECK (discovery_method IN (
        'manual', 'auto-scan', 'community', 'official-docs'
    )),
    verified_at TIMESTAMPTZ,              -- 最后验证时间
    last_probe_status TEXT,               -- 'ok' | 'failed' | 'unreachable'
    last_probe_error TEXT,
    
    -- 状态
    enabled BOOLEAN DEFAULT TRUE,
    disabled_at TIMESTAMPTZ,
    disabled_reason TEXT,
    
    -- 审计
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    tenant_id BIGINT NOT NULL DEFAULT current_setting('app.current_tenant_id', true)::bigint,
    
    UNIQUE(provider_code, model_id, tenant_id)
);

-- RLS 策略
ALTER TABLE public.free_resource_catalog ENABLE ROW LEVEL SECURITY;

CREATE POLICY free_resource_catalog_tenant_isolation ON public.free_resource_catalog
    USING (tenant_id = current_setting('app.current_tenant_id', true)::bigint);

-- 索引
CREATE INDEX idx_free_resource_catalog_provider ON free_resource_catalog(provider_code);
CREATE INDEX idx_free_resource_catalog_free_type ON free_resource_catalog(free_type) WHERE enabled = TRUE;
CREATE INDEX idx_free_resource_catalog_tos ON free_resource_catalog(tos_verdict) WHERE enabled = TRUE;
CREATE INDEX idx_free_resource_catalog_pool_key ON free_resource_catalog(pool_key) WHERE pool_key IS NOT NULL;
CREATE INDEX idx_free_resource_catalog_tenant ON free_resource_catalog(tenant_id);

-- 触发器: updated_at
CREATE TRIGGER free_resource_catalog_updated_at
    BEFORE UPDATE ON free_resource_catalog
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- 注释
COMMENT ON TABLE free_resource_catalog IS '免费 LLM 资源目录 - 维护全球免费提供商配额与合规元数据';
COMMENT ON COLUMN free_resource_catalog.pool_key IS '跨模型共享配额池标识，用于去重计算总配额';
COMMENT ON COLUMN free_resource_catalog.tos_verdict IS 'ToS 合规判定: ok=明确允许 | caution=灰色地带 | avoid=明确禁止代理';
```

### 2. `free_quota_tracker` — 本地配额追踪

```sql
-- 本地计量免费资源配额使用情况 (双窗口模型)
CREATE TABLE public.free_quota_tracker (
    id BIGSERIAL PRIMARY KEY,
    
    -- 关联
    credential_id BIGINT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    provider_code TEXT NOT NULL,
    model_id TEXT NOT NULL,
    
    -- 窗口类型
    window_type TEXT NOT NULL CHECK (window_type IN (
        'hour-5',      -- 5 小时滚动窗口 (短期 burst)
        'day-1',       -- UTC 日历日 (常见免费配额窗口)
        'day-7',       -- 7 日滚动窗口
        'month-1'      -- UTC 日历月
    )),
    window_start TIMESTAMPTZ NOT NULL,  -- 窗口起点 (UTC)
    window_end TIMESTAMPTZ NOT NULL,    -- 窗口终点 (UTC)
    
    -- 计数器
    request_count INT DEFAULT 0,
    token_count BIGINT DEFAULT 0,
    success_count INT DEFAULT 0,
    error_count INT DEFAULT 0,
    
    -- 429 校准 (从服务器响应头提取)
    last_429_at TIMESTAMPTZ,
    last_429_reset_after INT,           -- Retry-After 秒数
    last_429_limit_header TEXT,         -- X-RateLimit-Limit 原始值
    corrected_limit INT,                -- 校准后的实际限制
    
    -- 状态
    is_exhausted BOOLEAN DEFAULT FALSE,
    exhausted_at TIMESTAMPTZ,
    auto_reset_at TIMESTAMPTZ,          -- 自动重置时间 (基于窗口或 Retry-After)
    
    -- 审计
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    tenant_id BIGINT NOT NULL DEFAULT current_setting('app.current_tenant_id', true)::bigint,
    
    UNIQUE(credential_id, provider_code, model_id, window_type, window_start, tenant_id)
);

-- RLS 策略
ALTER TABLE public.free_quota_tracker ENABLE ROW LEVEL SECURITY;

CREATE POLICY free_quota_tracker_tenant_isolation ON public.free_quota_tracker
    USING (tenant_id = current_setting('app.current_tenant_id', true)::bigint);

-- 索引
CREATE INDEX idx_free_quota_tracker_credential ON free_quota_tracker(credential_id, window_type);
CREATE INDEX idx_free_quota_tracker_provider_model ON free_quota_tracker(provider_code, model_id);
CREATE INDEX idx_free_quota_tracker_exhausted ON free_quota_tracker(is_exhausted, auto_reset_at) 
    WHERE is_exhausted = TRUE;
CREATE INDEX idx_free_quota_tracker_window ON free_quota_tracker(window_start, window_end);
CREATE INDEX idx_free_quota_tracker_tenant ON free_quota_tracker(tenant_id);

-- 触发器
CREATE TRIGGER free_quota_tracker_updated_at
    BEFORE UPDATE ON free_quota_tracker
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

-- 自动重置过期窗口 (通过 cron job 定期清理)
CREATE INDEX idx_free_quota_tracker_cleanup ON free_quota_tracker(auto_reset_at) 
    WHERE auto_reset_at IS NOT NULL AND auto_reset_at < now();

COMMENT ON TABLE free_quota_tracker IS '本地配额追踪 - 因大多数免费提供商无 usage API，需本地计量并从 429 校准';
COMMENT ON COLUMN free_quota_tracker.window_type IS '窗口类型: hour-5=短期burst | day-1=UTC日 | day-7=滚动周 | month-1=UTC月';
COMMENT ON COLUMN free_quota_tracker.corrected_limit IS '从 429 响应头校准的实际限制 (覆盖文档声称值)';
```

### 3. `auto_combo_templates` — 虚拟路由模板

```sql
-- 虚拟 auto/* 组合模板配置
CREATE TABLE public.auto_combo_templates (
    id BIGSERIAL PRIMARY KEY,
    
    -- 标识
    combo_name TEXT NOT NULL,  -- 'auto/free', 'auto/best-free', 'auto/coding:free'
    display_name TEXT NOT NULL,
    description TEXT,
    
    -- 路由变体
    variant TEXT NOT NULL CHECK (variant IN (
        'cheap', 'fast', 'smart', 'coding', 'reasoning', 'creative', 'chaos'
    )),
    
    -- 过滤条件
    tier_filter TEXT[] DEFAULT ARRAY['free'],  -- 仅选 'free' billing_mode
    free_type_filter TEXT[],  -- 限制 free_type (如排除 'discontinued')
    tos_filter TEXT[] DEFAULT ARRAY['ok', 'caution'],  -- ToS 风险过滤
    
    provider_allowlist TEXT[],  -- 白名单 (空=全部)
    provider_denylist TEXT[],   -- 黑名单
    model_pattern TEXT,         -- 模型名正则 (如 '^gpt-.*-free$')
    
    -- 评分权重 (JSON)
    scoring_weights_json JSONB DEFAULT '{
        "health_score": 0.3,
        "latency_p95": 0.2,
        "quota_remaining": 0.25,
        "cost": 0.0,
        "task_fit": 0.15,
        "tier_affinity": 0.1
    }'::jsonb,
    
    -- 候选池配置
    max_candidates INT DEFAULT 50,
    exploration_rate FLOAT DEFAULT 0.05,  -- 5% bandit exploration
    
    -- 状态
    enabled BOOLEAN DEFAULT TRUE,
    priority INT DEFAULT 100,  -- 多模板匹配时优先级
    
    -- 审计
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    tenant_id BIGINT NOT NULL DEFAULT current_setting('app.current_tenant_id', true)::bigint,
    
    UNIQUE(combo_name, tenant_id)
);

-- RLS 策略
ALTER TABLE public.auto_combo_templates ENABLE ROW LEVEL SECURITY;

CREATE POLICY auto_combo_templates_tenant_isolation ON public.auto_combo_templates
    USING (tenant_id = current_setting('app.current_tenant_id', true)::bigint);

-- 索引
CREATE INDEX idx_auto_combo_templates_variant ON auto_combo_templates(variant) WHERE enabled = TRUE;
CREATE INDEX idx_auto_combo_templates_name ON auto_combo_templates(combo_name);
CREATE INDEX idx_auto_combo_templates_tenant ON auto_combo_templates(tenant_id);

-- 触发器
CREATE TRIGGER auto_combo_templates_updated_at
    BEFORE UPDATE ON auto_combo_templates
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

COMMENT ON TABLE auto_combo_templates IS '虚拟 auto/* 路由模板 - 零配置免费资源聚合';
COMMENT ON COLUMN auto_combo_templates.variant IS '路由变体: cheap=最低成本 | fast=低延迟 | smart=高质量 | coding=代码任务';
```

### 4. `keyless_providers` — 无认证提供商注册表

```sql
-- 无需 API Key 的免费提供商 (如 OpenCode, DuckDuckGo Chat)
CREATE TABLE public.keyless_providers (
    id BIGSERIAL PRIMARY KEY,
    
    -- 提供商信息
    provider_code TEXT NOT NULL UNIQUE REFERENCES provider_catalog(code) ON DELETE CASCADE,
    display_name TEXT NOT NULL,
    
    -- 认证机制 (虽然 keyless 但可能有特殊要求)
    auth_hint TEXT,  -- 如 "自动生成 JWT via 设备指纹"
    bootstrap_method TEXT,  -- 'none' | 'device-fingerprint' | 'embedded-browser'
    bootstrap_script TEXT,  -- 初始化脚本路径
    
    -- 限制
    rpm_limit INT DEFAULT 20,
    rpd_limit INT DEFAULT 1000,
    concurrent_limit INT DEFAULT 5,
    
    -- 可靠性
    reliability_score FLOAT DEFAULT 1.0,  -- 0-1, 探测成功率
    last_probe_at TIMESTAMPTZ,
    last_probe_status TEXT,
    consecutive_failures INT DEFAULT 0,
    
    -- 状态
    enabled BOOLEAN DEFAULT TRUE,
    allowlist_in_auto_combo BOOLEAN DEFAULT FALSE,  -- 是否加入 auto/* 候选池
    notes TEXT,
    
    -- 审计
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    tenant_id BIGINT NOT NULL DEFAULT current_setting('app.current_tenant_id', true)::bigint
);

-- RLS 策略
ALTER TABLE public.keyless_providers ENABLE ROW LEVEL SECURITY;

CREATE POLICY keyless_providers_tenant_isolation ON public.keyless_providers
    USING (tenant_id = current_setting('app.current_tenant_id', true)::bigint);

-- 索引
CREATE INDEX idx_keyless_providers_enabled ON keyless_providers(provider_code) WHERE enabled = TRUE;
CREATE INDEX idx_keyless_providers_auto_combo ON keyless_providers(provider_code) 
    WHERE enabled = TRUE AND allowlist_in_auto_combo = TRUE;
CREATE INDEX idx_keyless_providers_tenant ON keyless_providers(tenant_id);

COMMENT ON TABLE keyless_providers IS '无认证提供商注册表 - 零成本直接调用的免费 LLM';
COMMENT ON COLUMN keyless_providers.bootstrap_method IS 'none=直接调用 | device-fingerprint=生成指纹 | embedded-browser=模拟浏览器';
```

---

## 🔄 现有表扩展

### 扩展 `provider_catalog`

```sql
-- 添加免费资源标识列
ALTER TABLE public.provider_catalog
    ADD COLUMN IF NOT EXISTS has_free_tier BOOLEAN DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS free_tier_notes TEXT,
    ADD COLUMN IF NOT EXISTS official_free_docs_url TEXT;

-- 索引
CREATE INDEX idx_provider_catalog_free_tier ON provider_catalog(code) WHERE has_free_tier = TRUE;

COMMENT ON COLUMN provider_catalog.has_free_tier IS '是否提供免费 tier (快速过滤)';
```

### 扩展 `credentials`

```sql
-- 添加免费资源关联
ALTER TABLE public.credentials
    ADD COLUMN IF NOT EXISTS is_free_tier BOOLEAN DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS free_quota_window_type TEXT,  -- 关联 free_quota_tracker.window_type
    ADD COLUMN IF NOT EXISTS free_quota_limit INT;

-- 索引
CREATE INDEX idx_credentials_free_tier ON credentials(provider_code, is_free_tier) 
    WHERE is_free_tier = TRUE AND enabled = TRUE;

COMMENT ON COLUMN credentials.is_free_tier IS '是否为免费 tier 凭据 (用于优先级排序)';
```

### 扩展 `model_offers`

```sql
-- 标识免费模型
ALTER TABLE public.model_offers
    ADD COLUMN IF NOT EXISTS is_free_model BOOLEAN DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS free_resource_id BIGINT REFERENCES free_resource_catalog(id);

-- 索引
CREATE INDEX idx_model_offers_free ON model_offers(credential_id, model_id) 
    WHERE is_free_model = TRUE;

COMMENT ON COLUMN model_offers.is_free_model IS '是否为免费模型 (快速路由过滤)';
```

---

## 📈 视图与辅助函数

### 1. `v_free_resource_summary` — 免费资源汇总视图

```sql
CREATE OR REPLACE VIEW v_free_resource_summary AS
SELECT
    tenant_id,
    COUNT(*) AS total_resources,
    COUNT(*) FILTER (WHERE enabled = TRUE) AS enabled_resources,
    COUNT(DISTINCT provider_code) AS provider_count,
    SUM(monthly_tokens) FILTER (WHERE free_type IN ('recurring-monthly', 'keyless')) AS total_monthly_tokens,
    SUM(daily_tokens) FILTER (WHERE free_type = 'recurring-daily') AS total_daily_tokens,
    COUNT(*) FILTER (WHERE tos_verdict = 'ok') AS tos_ok_count,
    COUNT(*) FILTER (WHERE tos_verdict = 'avoid') AS tos_avoid_count,
    COUNT(*) FILTER (WHERE last_probe_status = 'ok') AS healthy_count,
    MAX(verified_at) AS last_verification
FROM free_resource_catalog
GROUP BY tenant_id;

COMMENT ON VIEW v_free_resource_summary IS '免费资源租户级汇总 - 仪表盘用';
```

### 2. `fn_compute_deduped_quota` — 池去重配额计算

```sql
CREATE OR REPLACE FUNCTION fn_compute_deduped_quota(
    p_tenant_id BIGINT,
    p_free_types TEXT[] DEFAULT ARRAY['recurring-monthly', 'recurring-daily', 'keyless']
) RETURNS TABLE (
    pool_key TEXT,
    max_monthly_tokens BIGINT,
    max_daily_tokens BIGINT,
    model_count INT
) AS $$
BEGIN
    RETURN QUERY
    SELECT
        COALESCE(frc.pool_key, frc.provider_code || ':' || frc.model_id) AS pool_key,
        MAX(frc.monthly_tokens) AS max_monthly_tokens,
        MAX(frc.daily_tokens) AS max_daily_tokens,
        COUNT(*)::INT AS model_count
    FROM free_resource_catalog frc
    WHERE frc.tenant_id = p_tenant_id
      AND frc.enabled = TRUE
      AND frc.free_type = ANY(p_free_types)
      AND frc.tos_verdict IN ('ok', 'caution')
    GROUP BY COALESCE(frc.pool_key, frc.provider_code || ':' || frc.model_id);
END;
$$ LANGUAGE plpgsql STABLE;

COMMENT ON FUNCTION fn_compute_deduped_quota IS '池去重配额计算 - 避免共享池重复计数';
```

### 3. `fn_quota_preflight_check` — 配额预检

```sql
CREATE OR REPLACE FUNCTION fn_quota_preflight_check(
    p_credential_id BIGINT,
    p_provider_code TEXT,
    p_model_id TEXT,
    p_min_remaining_pct FLOAT DEFAULT 0.1  -- 最少剩余 10%
) RETURNS BOOLEAN AS $$
DECLARE
    v_limit INT;
    v_used INT;
    v_remaining_pct FLOAT;
BEGIN
    -- 检查当前窗口使用情况
    SELECT 
        COALESCE(corrected_limit, 1000),
        request_count
    INTO v_limit, v_used
    FROM free_quota_tracker
    WHERE credential_id = p_credential_id
      AND provider_code = p_provider_code
      AND model_id = p_model_id
      AND window_type = 'day-1'
      AND window_start >= date_trunc('day', now())
      AND is_exhausted = FALSE;
    
    IF NOT FOUND THEN
        RETURN TRUE;  -- 无追踪记录，允许使用
    END IF;
    
    v_remaining_pct := (v_limit - v_used)::FLOAT / NULLIF(v_limit, 0);
    
    RETURN v_remaining_pct >= p_min_remaining_pct;
END;
$$ LANGUAGE plpgsql STABLE;

COMMENT ON FUNCTION fn_quota_preflight_check IS '配额预检 - 过滤近耗尽凭据';
```

---

## 🔐 安全与审计

### RLS 统一策略
所有免费资源相关表都启用 RLS，策略模板：

```sql
CREATE POLICY <table>_tenant_isolation ON <table>
    USING (tenant_id = current_setting('app.current_tenant_id', true)::bigint);
```

### 审计触发器
所有表自动记录 `updated_at`：

```sql
CREATE TRIGGER <table>_updated_at
    BEFORE UPDATE ON <table>
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
```

---

## 📊 数据生命周期

### `free_quota_tracker` 清理策略
保留窗口：
- `hour-5`: 保留 48h
- `day-1`: 保留 30 天
- `day-7`: 保留 90 天
- `month-1`: 保留 12 个月

清理 SQL (由 `bg/freequotacleanup` worker 执行):
```sql
DELETE FROM free_quota_tracker
WHERE (window_type = 'hour-5' AND window_end < now() - interval '48 hours')
   OR (window_type = 'day-1' AND window_end < now() - interval '30 days')
   OR (window_type = 'day-7' AND window_end < now() - interval '90 days')
   OR (window_type = 'month-1' AND window_end < now() - interval '12 months');
```

---

**下一步**: 阅读 `02-QUOTA-TRACKING.md` 了解本地配额追踪实现细节。

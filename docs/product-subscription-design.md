# 产品订阅与模块授权管理 - 详细设计

> 将License管理与功能模块门控整合，实现"购买License → 获得模块授权 → 功能自动开启"

---

## 一、设计背景与核心思路

### 现状分析

| 系统 | 现状 | 缺陷 |
|------|------|------|
| 模块开关 | 17个模块通过Settings KV控制启停 | 无授权控制，任何人可开启 |
| 订阅层级 | basic/pro/max 仅控制积分额度 | 不控制功能访问 |
| License | 纯设备绑定，无功能授权 | License不感知功能模块 |
| 安全模块 | 7个插件（1个实现+6个占位） | 无授权门槛 |
| VibeCoding | 不存在 | 需要从0设计 |

### 核心设计思路

```
License = 设备绑定 + 功能授权 + 订阅有效期

┌─────────────────────────────────────────────────────────┐
│                    License (授权凭证)                     │
├─────────────────────────────────────────────────────────┤
│  license_key: "GW-XXXX-XXXX-XXXX"                       │
│  max_devices: 2                                          │
│  expires_at: 2027-01-01                                  │
│  subscription_tier: "pro"                                │
│  modules: [                                              │
│    {key: "security",     enabled: true,  config: {...}},│
│    {key: "session_mgmt", enabled: true,  config: {...}},│
│    {key: "vibecoding",   enabled: true,  config: {...}},│
│    {key: "audit",        enabled: true,  config: {...}},│
│  ]                                                       │
└─────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────┐
│                 功能门控 (Feature Gate)                   │
├─────────────────────────────────────────────────────────┤
│  每个请求 → 检查License → 该模块是否授权 → 放行/拒绝      │
└─────────────────────────────────────────────────────────┘
```

---

## 二、产品模块定义

### 2.1 模块分类

```
产品模块 (Product Modules)
├── 📦 基础模块 (免费/必含)
│   ├── routing        - 智能路由
│   ├── authentication - 身份认证
│   └── audit          - 基础审计
│
├── 🔒 安全模块 (Security)
│   ├── prompt_injection   - 提示词注入防护
│   ├── output_compliance  - 输出合规检查
│   ├── sensitive_data_dlp - 敏感数据脱敏
│   ├── session_audit      - 会话审计
│   └── model_armor        - 模型防护
│
├── 💬 会话管理模块 (Session Management)
│   ├── session_management  - 会话生命周期管理
│   ├── session_analytics   - 会话分析仪表盘
│   ├── session_inspector   - 会话检查器
│   ├── session_context     - 会话上下文
│   └── handoff             - 会话交接
│
├── 💻 VibeCoding模块 (AI编程)
│   ├── vibe_coding_core    - VibeCoding核心引擎
│   ├── code_review         - 代码审查
│   ├── code_explain        - 代码解释
│   ├── code_refactor       - 代码重构
│   └── vibe_coding_agent   - 编程Agent
│
├── 🚀 高级模块 (Advanced)
│   ├── compression         - 提示词压缩
│   ├── cache               - 语义缓存
│   ├── disguise            - 请求伪装
│   ├── auto_routing        - 自动路由调优
│   └── goal                - 目标导向模式
│
└── 🔌 集成模块 (Integration)
    ├── feishu_bot          - 飞书机器人
    ├── dingtalk_bot        - 钉钉机器人
    ├── wechat_bot          - 企业微信机器人
    └── mcp_gateway         - MCP工具网关
```

### 2.2 订阅层级定义

| 层级 | 包含模块 | 价格 | 目标用户 |
|------|---------|------|---------|
| **Starter** | 基础模块 + 会话管理(基础) | ¥0 (免费) | 个人开发者 |
| **Pro** | Starter + 安全模块 + 会话管理(完整) + 基础集成 | ¥299/月 | 小团队 |
| **Enterprise** | Pro + VibeCoding + 高级模块 + 全部集成 + 自定义 | ¥999/月 | 企业客户 |
| **Custom** | 按需组合 | 按需定价 | 大型企业 |

---

## 三、数据库Schema设计

### 3.1 产品模块表 (迁移 `startup/371_product_modules.sql`)

```sql
-- 371_product_modules.sql

-- 产品模块定义表（全局，不按租户隔离）
CREATE TABLE IF NOT EXISTS product_modules (
    id              SERIAL PRIMARY KEY,
    key             TEXT NOT NULL UNIQUE,          -- 模块标识: security, session_mgmt, vibecoding
    name            TEXT NOT NULL,                 -- 显示名称
    description     TEXT NOT NULL DEFAULT '',
    category        TEXT NOT NULL,                 -- basic/security/session/vibe/advanced/integration
    icon            TEXT,                          -- 图标
    setting_key     TEXT,                          -- 对应的settings开关key
    is_base         BOOLEAN NOT NULL DEFAULT FALSE, -- 是否为基础免费模块
    sort_order      INT NOT NULL DEFAULT 0,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE, -- 是否在产品目录中可见
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pm_category ON product_modules (category);
CREATE INDEX IF NOT EXISTS idx_pm_setting ON product_modules (setting_key);

-- 产品模块包含的子功能（细粒度控制）
CREATE TABLE IF NOT EXISTS product_module_features (
    id              SERIAL PRIMARY KEY,
    module_key      TEXT NOT NULL REFERENCES product_modules(key) ON DELETE CASCADE,
    feature_key     TEXT NOT NULL,                 -- 功能标识: prompt_injection, output_compliance
    feature_name    TEXT NOT NULL,                 -- 显示名称
    description     TEXT NOT NULL DEFAULT '',
    setting_key     TEXT,                          -- 对应的settings开关key
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (module_key, feature_key)
);
CREATE INDEX IF NOT EXISTS idx_pmf_module ON product_module_features (module_key);

-- 订阅层级定义表
CREATE TABLE IF NOT EXISTS subscription_tiers (
    id              SERIAL PRIMARY KEY,
    code            TEXT NOT NULL UNIQUE,          -- starter, pro, enterprise, custom
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    price_cents     INT NOT NULL DEFAULT 0,       -- 月价格（分）
    sort_order      INT NOT NULL DEFAULT 0,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_st_code ON subscription_tiers (code);

-- 层级与模块的关联（哪些模块属于哪个层级）
CREATE TABLE IF NOT EXISTS tier_module_map (
    tier_code       TEXT NOT NULL REFERENCES subscription_tiers(code) ON DELETE CASCADE,
    module_key      TEXT NOT NULL REFERENCES product_modules(key) ON DELETE CASCADE,
    max_features    TEXT,                          -- NULL=全部功能, 或JSON数组指定功能子集
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tier_code, module_key)
);

-- RLS: 产品模块定义是全局系统级功能
-- 通过 bypass_rls 访问

-- ============================================================
-- 种子数据：产品模块
-- ============================================================

INSERT INTO product_modules (key, name, description, category, is_base, sort_order) VALUES
-- 基础模块
('routing',          '智能路由',       '自动选择最优模型和凭据',           'basic',        TRUE, 1),
('authentication',   '身份认证',       'API Key和JWT认证',                'basic',        TRUE, 2),
('audit',            '基础审计',       '请求日志和基础审计',               'basic',        TRUE, 3),

-- 安全模块
('prompt_injection', '提示词注入防护',  '检测和阻止提示词注入攻击',        'security',     FALSE, 10),
('output_compliance','输出合规检查',     '检测PII、毒性内容、偏见',         'security',     FALSE, 11),
('sensitive_data_dlp','敏感数据脱敏',   '输入输出敏感数据识别与脱敏',      'security',     FALSE, 12),
('session_audit',    '会话审计',        '深度会话行为审计与合规',          'security',     FALSE, 13),
('model_armor',      '模型防护',        '模型层面的安全防护',              'security',     FALSE, 14),

-- 会话管理模块
('session_management','会话生命周期管理','会话创建、保存、恢复、清理',     'session',      FALSE, 20),
('session_analytics', '会话分析仪表盘',  '会话数据统计与可视化',           'session',      FALSE, 21),
('session_inspector', '会话检查器',      '实时会话健康监控',               'session',      FALSE, 22),
('session_context',   '会话上下文',      '会话级上下文管理与注入',         'session',      FALSE, 23),
('handoff',           '会话交接',        '会话在设备/客户端间交接',        'session',      FALSE, 24),

-- VibeCoding模块
('vibe_coding_core',  'VibeCoding核心',  'AI辅助编程核心引擎',             'vibe',         FALSE, 30),
('code_review',       '代码审查',        'AI驱动的代码审查',               'vibe',         FALSE, 31),
('code_explain',      '代码解释',        '代码逻辑解释与文档生成',         'vibe',         FALSE, 32),
('code_refactor',     '代码重构',        '智能代码重构建议',               'vibe',         FALSE, 33),
('vibe_coding_agent', '编程Agent',       '多步骤编程任务Agent',            'vibe',         FALSE, 34),

-- 高级模块
('compression',       '提示词压缩',      '自动压缩长提示词降低成本',       'advanced',     FALSE, 40),
('cache',             '语义缓存',        '相似请求复用缓存响应',           'advanced',     FALSE, 41),
('disguise',          '请求伪装',        '请求特征伪装绕过风控',           'advanced',     FALSE, 42),
('auto_routing',      '自动路由调优',    '基于反馈的自动路由优化',         'advanced',     FALSE, 43),
('goal',              '目标导向模式',     '目标驱动的任务执行',             'advanced',     FALSE, 44),

-- 集成模块
('feishu_bot',        '飞书机器人',      '飞书消息通知与审批',             'integration',  FALSE, 50),
('dingtalk_bot',      '钉钉机器人',      '钉钉消息通知与审批',             'integration',  FALSE, 51),
('wechat_bot',        '企业微信机器人',  '企业微信消息通知',               'integration',  FALSE, 52),
('mcp_gateway',       'MCP工具网关',     'Model Context Protocol工具集成', 'integration',  FALSE, 53)
ON CONFLICT (key) DO NOTHING;

-- ============================================================
-- 种子数据：订阅层级
-- ============================================================

INSERT INTO subscription_tiers (code, name, description, price_cents, sort_order) VALUES
('starter',    'Starter版',    '适合个人开发者',                     0,    1),
('pro',        'Pro版',        '适合小团队',                         29900, 2),
('enterprise', 'Enterprise版', '适合企业客户',                       99900, 3),
('custom',     '定制版',       '按需定价',                           0,    4)
ON CONFLICT (code) DO NOTHING;

-- ============================================================
-- 种子数据：层级-模块关联
-- ============================================================

-- Starter: 基础模块 + 会话管理基础
INSERT INTO tier_module_map (tier_code, module_key) VALUES
('starter', 'routing'),
('starter', 'authentication'),
('starter', 'audit'),
('starter', 'session_management'),
('starter', 'session_analytics')
ON CONFLICT DO NOTHING;

-- Pro: Starter全部 + 安全模块 + 会话完整 + 基础集成
INSERT INTO tier_module_map (tier_code, module_key) VALUES
('pro', 'prompt_injection'),
('pro', 'output_compliance'),
('pro', 'sensitive_data_dlp'),
('pro', 'session_audit'),
('pro', 'session_inspector'),
('pro', 'session_context'),
('pro', 'handoff'),
('pro', 'compression'),
('pro', 'cache'),
('pro', 'feishu_bot'),
('pro', 'dingtalk_bot'),
('pro', 'wechat_bot')
ON CONFLICT DO NOTHING;

-- Enterprise: Pro全部 + VibeCoding + 高级模块 + 全部集成
INSERT INTO tier_module_map (tier_code, module_key) VALUES
('enterprise', 'model_armor'),
('enterprise', 'vibe_coding_core'),
('enterprise', 'code_review'),
('enterprise', 'code_explain'),
('enterprise', 'code_refactor'),
('enterprise', 'vibe_coding_agent'),
('enterprise', 'disguise'),
('enterprise', 'auto_routing'),
('enterprise', 'goal'),
('enterprise', 'mcp_gateway')
ON CONFLICT DO NOTHING;

-- Custom: 全部模块可选（不预设）
```

### 3.2 License模块授权表 (迁移 `startup/372_license_modules.sql`)

```sql
-- 372_license_modules.sql

-- License与模块的授权关联（覆盖层级默认值）
CREATE TABLE IF NOT EXISTS license_modules (
    id              BIGSERIAL PRIMARY KEY,
    license_id      BIGINT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    module_key      TEXT NOT NULL REFERENCES product_modules(key),
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,  -- 允许在License级别单独开关
    config          JSONB,                          -- 模块特定配置覆盖
    expires_at      TIMESTAMPTZ,                    -- 模块独立过期时间（可选）
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (license_id, module_key)
);
CREATE INDEX IF NOT EXISTS idx_lm_license ON license_modules (license_id);
CREATE INDEX IF NOT EXISTS idx_lm_module ON license_modules (module_key);

-- License授权审计日志
CREATE TABLE IF NOT EXISTS license_module_audit (
    id              BIGSERIAL PRIMARY KEY,
    license_key     TEXT NOT NULL,
    module_key      TEXT NOT NULL,
    action          TEXT NOT NULL,  -- grant, revoke, config_change
    old_value       JSONB,
    new_value       JSONB,
    actor           TEXT,           -- 操作人
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_lma_key ON license_module_audit (license_key, created_at DESC);
```

### 3.3 VibeCoding模块表 (迁移 `startup/373_vibecoding.sql`)

```sql
-- 373_vibecoding.sql

-- VibeCoding项目表
CREATE TABLE IF NOT EXISTS vibe_coding_projects (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       TEXT NOT NULL DEFAULT 'default',
    name            TEXT NOT NULL,
    description     TEXT,
    language        TEXT,                       -- 主要编程语言
    framework       TEXT,                       -- 使用的框架
    status          TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'archived', 'deleted')),
    settings        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS vcp_tenant ON vibe_coding_projects (tenant_id);
ALTER TABLE vibe_coding_projects ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_vcp ON vibe_coding_projects
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- VibeCoding会话表
CREATE TABLE IF NOT EXISTS vibe_coding_sessions (
    id              BIGSERIAL PRIMARY KEY,
    project_id      BIGINT REFERENCES vibe_coding_projects(id) ON DELETE SET NULL,
    tenant_id       TEXT NOT NULL DEFAULT 'default',
    session_id      TEXT NOT NULL,              -- 关联到gateway的session_id
    task_type       TEXT NOT NULL,              -- code_review, code_explain, code_refactor, chat
    status          TEXT NOT NULL DEFAULT 'active'
        CHECK (status IN ('active', 'completed', 'failed', 'cancelled')),
    messages        JSONB NOT NULL DEFAULT '[]'::jsonb,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS vcs_project ON vibe_coding_sessions (project_id);
CREATE INDEX IF NOT EXISTS vcs_session ON vibe_coding_sessions (session_id);
ALTER TABLE vibe_coding_sessions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_vcs ON vibe_coding_sessions
    USING ((tenant_id)::text = (public.get_current_tenant())::text);

-- VibeCoding代码审查记录
CREATE TABLE IF NOT EXISTS vibe_code_reviews (
    id              BIGSERIAL PRIMARY KEY,
    session_id      BIGINT REFERENCES vibe_coding_sessions(id) ON DELETE SET NULL,
    tenant_id       TEXT NOT NULL DEFAULT 'default',
    file_path       TEXT,
    language        TEXT,
    original_code   TEXT,
    review_result   JSONB,                      -- 审查结果：问题列表、建议
    score           NUMERIC(3,2),               -- 质量评分 0-1
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS vcr_session ON vibe_code_reviews (session_id);
ALTER TABLE vibe_code_reviews ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation_vcr ON vibe_code_reviews
    USING ((tenant_id)::text = (public.get_current_tenant())::text);
```

---

## 四、功能门控（Feature Gate）机制

### 4.1 门控架构

```
请求进入
    │
    ▼
┌─────────────────────┐
│ License验证器        │──▶ 验证签名、设备、有效期
│ (license/validator) │
└─────────┬───────────┘
          │ License有效
          ▼
┌─────────────────────┐
│ Feature Gate        │──▶ 检查该模块是否在License中授权
│ (license/gate.go)   │
└─────────┬───────────┘
          │ 模块已授权
          ▼
┌─────────────────────┐
│ 模块开关检查         │──▶ 检查Settings KV中的启用状态
│ (settings.Global)   │
└─────────┬───────────┘
          │ 模块已启用
          ▼
      正常处理请求
```

### 4.2 核心实现 (`license/gate.go`)

```go
package license

import (
    "context"
    "errors"
    "log/slog"
    "sync"
    "time"
)

var (
    ErrLicenseExpired   = errors.New("license expired")
    ErrLicenseRevoked   = errors.New("license revoked")
    ErrModuleNotLicensed = errors.New("module not licensed")
    ErrDeviceLimit      = errors.New("device limit exceeded")
)

// Gate 功能门控
type Gate struct {
    store     Store
    cache     *gateCache   // 内存缓存，避免每次请求查DB
}

type gateCache struct {
    mu    sync.RWMutex
    items map[string]*cachedLicense // key = license_key
}

type cachedLicense struct {
    license     *License
    modules     map[string]*LicenseModule // module_key -> config
    expiresAt   time.Time
    lastCheck   time.Time
}

func NewGate(store Store) *Gate {
    return &Gate{
        store: store,
        cache: &gateCache{
            items: make(map[string]*cachedLicense),
        },
    }
}

// CheckLicense 验证License有效性
func (g *Gate) CheckLicense(ctx context.Context, licenseKey string) (*License, error) {
    cached := g.cache.get(licenseKey)
    if cached != nil && time.Since(cached.lastCheck) < 5*time.Minute {
        if time.Now().After(cached.license.ExpiresAt) {
            return nil, ErrLicenseExpired
        }
        return cached.license, nil
    }

    // 从数据库查询
    lic, err := g.store.GetLicense(ctx, licenseKey)
    if err != nil {
        return nil, err
    }

    // 检查过期
    if time.Now().After(lic.ExpiresAt) {
        return nil, ErrLicenseExpired
    }

    // 检查是否被撤销
    if lic.RevokedAt != nil {
        return nil, ErrLicenseRevoked
    }

    // 更新缓存
    g.cache.put(licenseKey, lic)
    return lic, nil
}

// CheckModule 检查模块是否已授权
func (g *Gate) CheckModule(ctx context.Context, licenseKey, moduleKey string) error {
    // 1. 基础模块始终可用
    if isBaseModule(moduleKey) {
        return nil
    }

    // 2. 从缓存/DB获取模块列表
    cached := g.cache.get(licenseKey)
    if cached == nil {
        // 重新加载
        lic, err := g.store.GetLicense(ctx, licenseKey)
        if err != nil {
            return err
        }
        modules, err := g.store.GetLicenseModules(ctx, licenseKey)
        if err != nil {
            return err
        }
        cached = &cachedLicense{
            license:   lic,
            modules:   modules,
            expiresAt: lic.ExpiresAt,
            lastCheck: time.Now(),
        }
        g.cache.put(licenseKey, cached)
    }

    // 3. 检查模块是否授权
    mod, ok := cached.modules[moduleKey]
    if !ok || !mod.Enabled {
        return ErrModuleNotLicensed
    }

    // 4. 检查模块独立过期时间
    if mod.ExpiresAt != nil && time.Now().After(*mod.ExpiresAt) {
        return ErrModuleNotLicensed
    }

    return nil
}

// GetModuleConfig 获取模块配置（License覆盖 + 默认值）
func (g *Gate) GetModuleConfig(ctx context.Context, licenseKey, moduleKey string) map[string]any {
    cached := g.cache.get(licenseKey)
    if cached == nil {
        return nil
    }
    mod, ok := cached.modules[moduleKey]
    if !ok {
        return nil
    }
    return mod.Config
}

// ListAuthorizedModules 列出授权的模块
func (g *Gate) ListAuthorizedModules(ctx context.Context, licenseKey string) (map[string]bool, error) {
    modules, err := g.store.GetLicenseModules(ctx, licenseKey)
    if err != nil {
        return nil, err
    }
    result := make(map[string]bool)
    for key, mod := range modules {
        result[key] = mod.Enabled
    }
    return result, nil
}

// InvalidateCache 使缓存失效
func (g *Gate) InvalidateCache(licenseKey string) {
    g.cache.remove(licenseKey)
}

// isBaseModule 基础模块检查（始终免费可用）
func isBaseModule(key string) bool {
    baseModules := map[string]bool{
        "routing":        true,
        "authentication": true,
        "audit":          true,
    }
    return baseModules[key]
}

// 缓存操作
func (c *gateCache) get(key string) *cachedLicense {
    c.mu.RLock()
    defer c.mu.RUnlock()
    return c.items[key]
}

func (c *gateCache) put(key string, val *cachedLicense) {
    c.mu.Lock()
    defer c.mu.Unlock()
    c.items[key] = val
}

func (c *gateCache) remove(key string) {
    c.mu.Lock()
    defer c.mu.Unlock()
    delete(c.items, key)
}
```

### 4.3 Pipeline集成

在Pipeline的PhaseGovernance阶段，增加License模块检查Hook：

```go
// domains/security/license_hook.go

// LicenseModuleHook 在请求进入时检查License模块授权
type LicenseModuleHook struct {
    gate *license.Gate
}

func (h *LicenseModuleHook) Name() string { return "license_module_gate" }

func (h *LicenseModuleHook) Priority() int { return 50 } // 在SecurityHook(100)之前

func (h *LicenseModuleHook) Enabled(ctx context.Context, envelope *domain.PipelineRequest) bool {
    return true // 始终启用
}

func (h *LicenseModuleHook) Execute(ctx context.Context, envelope *domain.PipelineRequest) error {
    licenseKey := extractLicenseKey(envelope)
    if licenseKey == "" {
        return nil // 无License的请求走默认路径
    }

    // 检查License有效性
    if _, err := h.gate.CheckLicense(ctx, licenseKey); err != nil {
        return err
    }

    return nil
}

func (h *LicenseModuleHook) OnError(ctx context.Context, envelope *domain.PipelineRequest, err error) error {
    // License错误返回403
    if errors.Is(err, license.ErrModuleNotLicensed) {
        return &governance.Verdict{
            Allow:   false,
            Code:    "module_not_licensed",
            Message: "此功能模块未授权，请升级License",
        }
    }
    return err
}
```

### 4.4 Settings集成

扩展现有模块定义，增加License授权检查：

```go
// admin/modules.go 中的 resolveModuleEnabled 函数修改

func (h *Handler) resolveModuleEnabled(ctx context.Context, moduleKey string, tenantID string) ModuleStatus {
    // 1. 先检查License授权
    if h.licenseGate != nil {
        licenseKey := getLicenseKeyForTenant(tenantID)
        if licenseKey != "" {
            if err := h.licenseGate.CheckModule(ctx, licenseKey, moduleKey); err != nil {
                return ModuleStatus{
                    Enabled:          false,
                    Source:           "license",
                    CanToggleEnabled: false, // License未授权，不允许手动开启
                    BlockedReason:   "License未授权此模块",
                }
            }
        }
    }

    // 2. 再检查Settings开关
    val, source, _ := settings.Global.EffectiveValue("platform", moduleKey+".enabled", tenantID)
    enabled := val == "true" || val == "1" || val == "yes"

    return ModuleStatus{
        Enabled: enabled,
        Source:  source,
        CanToggleEnabled: true,
    }
}
```

---

## 五、License与模块授权整合

### 5.1 License创建流程（含模块授权）

```go
// admin/license_admin.go

// CreateLicenseRequest 创建License请求
type CreateLicenseRequest struct {
    CustomerName  string              `json:"customer_name"`
    CustomerEmail string              `json:"customer_email"`
    TierCode      string              `json:"tier_code"`      // starter/pro/enterprise/custom
    MaxDevices    int                 `json:"max_devices"`
    ExpiresAt     time.Time           `json:"expires_at"`
    Modules       []ModuleGrant       `json:"modules,omitempty"` // 自定义模块（仅custom层级）
    Notes         string              `json:"notes"`
}

type ModuleGrant struct {
    ModuleKey string         `json:"module_key"`
    Enabled   bool           `json:"enabled"`
    Config    map[string]any `json:"config,omitempty"`
    ExpiresAt *time.Time     `json:"expires_at,omitempty"` // 模块独立过期
}

// 创建License的完整流程
func (h *Handler) handleCreateLicense(ctx context.Context, req *CreateLicenseRequest) (*License, error) {
    // 1. 根据层级获取默认模块列表
    tierModules, err := h.store.GetTierModules(ctx, req.TierCode)
    if err != nil {
        return nil, err
    }

    // 2. 合并自定义模块
    finalModules := mergeModules(tierModules, req.Modules)

    // 3. 创建License
    lic := &License{
        LicenseKey:   generateLicenseKey(),
        CustomerName: req.CustomerName,
        CustomerEmail: req.CustomerEmail,
        MaxDevices:   req.MaxDevices,
        ExpiresAt:    req.ExpiresAt,
    }
    if err := h.store.CreateLicense(ctx, lic); err != nil {
        return nil, err
    }

    // 4. 写入模块授权
    for _, mod := range finalModules {
        if err := h.store.GrantModule(ctx, lic.ID, mod); err != nil {
            return nil, err
        }
    }

    // 5. 签名License（包含模块信息）
    signed, err := h.crypto.SignLicenseWithModules(lic, finalModules)
    if err != nil {
        return nil, err
    }
    h.store.UpdateSignedLicense(ctx, lic.LicenseKey, signed)

    // 6. 审计日志
    h.store.AuditModuleGrant(ctx, lic.LicenseKey, req.Actor, finalModules)

    return lic, nil
}
```

### 5.2 模块授权管理API

```
# 产品模块管理（超级管理员）
GET    /api/admin/product-modules              列出所有产品模块
POST   /api/admin/product-modules              创建产品模块
PUT    /api/admin/product-modules/:key         更新产品模块
DELETE /api/admin/product-modules/:key         删除产品模块

# 订阅层级管理
GET    /api/admin/subscription-tiers           列出所有层级
POST   /api/admin/subscription-tiers           创建层级
PUT    /api/admin/subscription-tiers/:code     更新层级
GET    /api/admin/subscription-tiers/:code/modules  层级包含的模块
PUT    /api/admin/subscription-tiers/:code/modules  更新层级模块

# License模块授权管理
GET    /api/admin/licenses/:key/modules        查看License授权的模块
POST   /api/admin/licenses/:key/modules        授权模块
PUT    /api/admin/licenses/:key/modules/:modKey  更新模块授权
DELETE /api/admin/licenses/:key/modules/:modKey  撤销模块授权

# 实例端 - License状态
GET    /api/license/modules                     当前License授权的模块列表
GET    /api/license/modules/:key/check          检查特定模块是否授权
```

### 5.3 VibeCoding模块专用API

```
# VibeCoding管理
GET    /api/vibe/projects                       列出项目
POST   /api/vibe/projects                       创建项目
GET    /api/vibe/projects/:id                   项目详情
PUT    /api/vibe/projects/:id                   更新项目
DELETE /api/vibe/projects/:id                   删除项目

POST   /api/vibe/sessions                       创建VibeCoding会话
GET    /api/vibe/sessions/:id                   会话详情
POST   /api/vibe/sessions/:id/messages          发送消息

POST   /api/vibe/code-review                    代码审查
POST   /api/vibe/code-explain                   代码解释
POST   /api/vibe/code-refactor                  代码重构
```

---

## 六、前端UI设计

### 6.1 产品模块管理页面

```
┌─────────────────────────────────────────────────────────────────────┐
│  管理中心 > 产品模块管理                                            │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  📦 产品模块目录                                                    │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │ 分类: [全部] [基础] [安全] [会话] [VibeCoding] [高级] [集成]  │  │
│  ├──────────────────────────────────────────────────────────────┤  │
│  │                                                              │  │
│  │ 🔒 安全模块                                                  │  │
│  │ ┌──────────┬──────────┬──────────┬──────────┬──────────┐   │  │
│  │ │提示词注入│输出合规  │数据脱敏  │会话审计  │模型防护  │   │  │
│  │ │  ✅ Pro+ │  ✅ Pro+ │  ✅ Pro+ │  ✅ Pro+ │  ✅ Ent+ │   │  │
│  │ └──────────┴──────────┴──────────┴──────────┴──────────┘   │  │
│  │                                                              │  │
│  │ 💬 会话管理                                                  │  │
│  │ ┌──────────┬──────────┬──────────┬──────────┬──────────┐   │  │
│  │ │生命周期  │分析仪表盘│检查器    │上下文    │交接      │   │  │
│  │ │  ✅ 免费 │  ✅ Pro+ │  ✅ Pro+ │  ✅ Pro+ │  ✅ Pro+ │   │  │
│  │ └──────────┴──────────┴──────────┴──────────┴──────────┘   │  │
│  │                                                              │  │
│  │ 💻 VibeCoding                                               │  │
│  │ ┌──────────┬──────────┬──────────┬──────────┬──────────┐   │  │
│  │ │核心引擎  │代码审查  │代码解释  │代码重构  │编程Agent │   │  │
│  │ │  ✅ Ent+ │  ✅ Ent+ │  ✅ Ent+ │  ✅ Ent+ │  ✅ Ent+ │   │  │
│  │ └──────────┴──────────┴──────────┴──────────┴──────────┘   │  │
│  │                                                              │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  📊 订阅层级配置                                                    │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │           │ Starter    │ Pro         │ Enterprise │ Custom   │  │
│  │           │ ¥0/月      │ ¥299/月     │ ¥999/月    │ 按需     │  │
│  │───────────┼────────────┼─────────────┼────────────┼──────────│  │
│  │ 基础模块   │ ✅ 3个     │ ✅ 3个      │ ✅ 3个     │ ✅ 按需  │  │
│  │ 安全模块   │ ❌ 0个     │ ✅ 4个      │ ✅ 5个     │ ✅ 按需  │  │
│  │ 会话管理   │ ✅ 2个     │ ✅ 5个      │ ✅ 5个     │ ✅ 按需  │  │
│  │ VibeCoding │ ❌ 0个     │ ❌ 0个      │ ✅ 5个     │ ✅ 按需  │  │
│  │ 高级模块   │ ❌ 0个     │ ✅ 2个      │ ✅ 5个     │ ✅ 按需  │  │
│  │ 集成模块   │ ❌ 0个     │ ✅ 3个      │ ✅ 4个     │ ✅ 按需  │  │
│  │───────────┼────────────┼─────────────┼────────────┼──────────│  │
│  │ 操作       │ [编辑]     │ [编辑]      │ [编辑]     │ [编辑]   │  │
│  └──────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

### 6.2 License授权管理页面

```
┌─────────────────────────────────────────────────────────────────────┐
│  管理中心 > License管理 > GW-XXXX-XXXX-XXXX                        │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  License信息                                                        │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │ 客户: 张三    邮箱: zhang@example.com                        │  │
│  │ 层级: Pro版   设备限制: 2/2   过期: 2027-01-01              │  │
│  │ 状态: ✅ 活跃                                                │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  已授权模块 (8/26)                                                   │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │ ✅ 基础模块 (3/3)                                            │  │
│  │    ✅ 智能路由  ✅ 身份认证  ✅ 基础审计                     │  │
│  │                                                              │  │
│  │ ✅ 安全模块 (4/5)                        [全部授权] [取消]   │  │
│  │    ✅ 提示词注入防护  ✅ 输出合规检查                        │  │
│  │    ✅ 敏感数据脱敏    ✅ 会话审计                            │  │
│  │    ❌ 模型防护                             [授权此模块]       │  │
│  │                                                              │  │
│  │ ✅ 会话管理 (3/5)                                            │  │
│  │    ✅ 会话生命周期  ✅ 会话分析  ✅ 会话检查器               │  │
│  │    ❌ 会话上下文     ❌ 会话交接                              │  │
│  │                                                              │  │
│  │ ❌ VibeCoding (0/5)                                          │  │
│  │    全部未授权                            [授权全部]           │  │
│  │                                                              │  │
│  │ ❌ 高级模块 (0/5)                                            │  │
│  │ ❌ 集成模块 (0/4)                                            │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  [授权更多模块]  [导出License]  [撤销License]                       │
└─────────────────────────────────────────────────────────────────────┘
```

### 6.3 网关侧 - 模块授权状态页面

```
┌─────────────────────────────────────────────────────────────────────┐
│  网关管理 > 功能模块                                                │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  当前License: GW-XXXX-XXXX-XXXX (Pro版, 有效期至 2027-01-01)       │
│                                                                      │
│  已授权模块                                                         │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │ ✅ 智能路由    [已启用]   │  ✅ 提示词注入  [已启用]          │  │
│  │ ✅ 身份认证    [已启用]   │  ✅ 输出合规    [已启用]          │  │
│  │ ✅ 基础审计    [已启用]   │  ✅ 数据脱敏    [已启用]          │  │
│  │ ✅ 会话管理    [已启用]   │  ✅ 会话分析    [已启用]          │  │
│  │ ✅ 会话检查    [已启用]   │  ✅ 压缩        [已启用]          │  │
│  │ ✅ 语义缓存    [已启用]   │  ✅ 飞书机器人  [已启用]          │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  未授权模块 (可申请)                                                │
│  ┌──────────────────────────────────────────────────────────────┐  │
│  │ 🔒 模型防护        [申请授权]                                │  │
│  │ 🔒 会话上下文      [申请授权]                                │  │
│  │ 🔒 VibeCoding核心  [申请授权]                                │  │
│  │ 🔒 代码审查        [申请授权]                                │  │
│  │ 🔒 请求伪装        [申请授权]                                │  │
│  └──────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  💡 要使用更多功能，请联系管理员升级License                         │
└─────────────────────────────────────────────────────────────────────┘
```

---

## 七、VibeCoding模块详细设计

### 7.1 目录结构

```
vibe/                             # 新增顶层包
├── types.go                      # 核心类型定义
├── service.go                    # 核心服务逻辑
├── code_review.go                # 代码审查
├── code_explain.go               # 代码解释
├── code_refactor.go              # 代码重构
├── agent.go                      # 多步骤编程Agent
├── project_store.go              # 项目存储
├── session_store.go              # 会话存储
├── prompt_templates.go           # Prompt模板
└── admin_api.go                  # Admin API
```

### 7.2 核心类型

```go
package vibe

import "time"

// Project VibeCoding项目
type Project struct {
    ID          int64              `json:"id"`
    TenantID    string             `json:"tenant_id"`
    Name        string             `json:"name"`
    Description string             `json:"description"`
    Language    string             `json:"language"`
    Framework   string             `json:"framework"`
    Status      string             `json:"status"`
    Settings    map[string]any     `json:"settings"`
    CreatedAt   time.Time          `json:"created_at"`
}

// TaskType 任务类型
type TaskType string

const (
    TaskCodeReview  TaskType = "code_review"
    TaskCodeExplain TaskType = "code_explain"
    TaskCodeRefactor TaskType = "code_refactor"
    TaskChat        TaskType = "chat"
)

// ReviewResult 代码审查结果
type ReviewResult struct {
    Issues      []ReviewIssue `json:"issues"`
    Suggestions []string      `json:"suggestions"`
    Score       float64       `json:"score"`
    Summary     string        `json:"summary"`
}

type ReviewIssue struct {
    Severity  string `json:"severity"`  // critical, major, minor, info
    Line      int    `json:"line"`
    Column    int    `json:"column"`
    Code      string `json:"code"`      // 问题代码
    Message   string `json:"message"`
    Suggestion string `json:"suggestion"`
}

// ExplainResult 代码解释结果
type ExplainResult struct {
    Summary     string            `json:"summary"`
    Functions   []FunctionDoc     `json:"functions"`
    Dependencies []string         `json:"dependencies"`
    Examples    []string          `json:"examples"`
}

type FunctionDoc struct {
    Name        string `json:"name"`
    Signature   string `json:"signature"`
    Description string `json:"description"`
    Parameters  []ParamDoc `json:"parameters"`
    Returns     string `json:"returns"`
}

type ParamDoc struct {
    Name        string `json:"name"`
    Type        string `json:"type"`
    Description string `json:"description"`
}

// RefactorResult 代码重构结果
type RefactorResult struct {
    OriginalCode string         `json:"original_code"`
    RefactoredCode string       `json:"refactored_code"`
    Changes      []RefactorChange `json:"changes"`
    Explanation  string         `json:"explanation"`
}

type RefactorChange struct {
    Type    string `json:"type"`  // rename, extract, simplify, optimize
    From    string `json:"from"`
    To      string `json:"to"`
    Reason  string `json:"reason"`
}
```

---

## 八、完整模块关系图

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        产品订阅与授权管理 架构                           │
├─────────────────────────────────────────────────────────────────────────┤
│                                                                          │
│  ┌─────────────┐    ┌─────────────┐    ┌─────────────┐                 │
│  │product_     │───▶│tier_        │───▶│license_     │                 │
│  │modules      │    │module_map   │    │modules      │                 │
│  │(模块目录)   │    │(层级映射)   │    │(授权记录)   │                 │
│  └─────────────┘    └─────────────┘    └──────┬──────┘                 │
│                                                │                        │
│                                                ▼                        │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                    Feature Gate (功能门控)                       │   │
│  │                                                                 │   │
│  │  请求 → License验证 → 模块授权检查 → Settings开关 → 放行/拒绝   │   │
│  └─────────────────────────────────────────────────────────────────┘   │
│          │                          │                          │        │
│          ▼                          ▼                          ▼        │
│  ┌──────────────┐    ┌──────────────────┐    ┌──────────────────┐    │
│  │ 安全模块     │    │ 会话管理模块     │    │ VibeCoding模块   │    │
│  │              │    │                  │    │                  │    │
│  │ • 提示词注入 │    │ • 会话生命周期   │    │ • 核心引擎       │    │
│  │ • 输出合规   │    │ • 会话分析       │    │ • 代码审查       │    │
│  │ • 数据脱敏   │    │ • 会话检查器     │    │ • 代码解释       │    │
│  │ • 会话审计   │    │ • 会话上下文     │    │ • 代码重构       │    │
│  │ • 模型防护   │    │ • 会话交接       │    │ • 编程Agent      │    │
│  └──────────────┘    └──────────────────┘    └──────────────────┘    │
│          │                          │                          │        │
│          └──────────────────────────┼──────────────────────────┘        │
│                                     ▼                                    │
│  ┌─────────────────────────────────────────────────────────────────┐   │
│  │                    现有 Gateway Pipeline                         │   │
│  │  PhaseGovernance → PhaseTransform → PhaseUpstream → ...        │   │
│  └─────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────┘
```

---

## 九、实施计划（更新版）

### 新增任务

| # | 任务 | 预估 | Phase |
|---|------|------|-------|
| 1.9 | SQL迁移 371-373 | 1d | Phase 1 |
| 2.9 | `license/gate.go` 功能门控 | 1.5d | Phase 2 |
| 2.10 | License模块授权管理API | 2d | Phase 2 |
| 3.10 | `domains/security/license_hook.go` Pipeline集成 | 0.5d | Phase 3 |
| 7.1 | VibeCoding核心 (`vibe/`) | 3d | Phase 7 |
| 7.2 | VibeCoding Admin API | 1d | Phase 7 |
| 8.1 | 产品模块管理页面 | 2d | Phase 8 |
| 8.2 | License授权管理页面 | 1.5d | Phase 8 |
| 8.3 | 网关侧模块授权页面 | 1d | Phase 8 |
| 8.4 | VibeCoding管理页面 | 2d | Phase 8 |

### 更新后的完整时间线

```
Phase 1: 基础框架 (6天)         - 原5天 + SQL迁移1天
Phase 2: License模块 (10天)     - 原8天 + 功能门控+授权API 2天
Phase 3: 故障自愈 (7天)         - 不变
Phase 4: 自动升级 (5天)         - 不变
Phase 5: 中心运维 (7天)         - 不变
Phase 6: 前端界面 (8天)         - 不变
Phase 7: VibeCoding (4天)       - 新增
Phase 8: 订阅管理UI (6.5天)     - 新增
────────────────────────────────────────────
总工期: 约 53.5 工作日
```

---

## 十、关键设计决策

| 决策 | 选择 | 理由 |
|------|------|------|
| 模块授权存储 | License内嵌 + DB冗余 | 离线可用，中心可管理 |
| 门控检查位置 | Pipeline PhaseGovernance | 与安全检查同层，一次检查 |
| 缓存策略 | 5分钟TTL + 事件失效 | 性能与一致性平衡 |
| 基础模块 | 始终可用，不检查License | 确保网关基础功能不受影响 |
| 层级→模块映射 | DB表存储，支持动态调整 | 灵活定价策略 |
| VibeCoding | 独立包+Pipeline Hook | 与现有安全模块解耦 |
| 自定义层级 | 支持，模块完全可配 | 满足大客户需求 |

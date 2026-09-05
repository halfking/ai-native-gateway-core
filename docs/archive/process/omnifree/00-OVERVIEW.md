# OmniFree: 免费 LLM 资源自动配置系统

> **让企业零成本接入全球 1.5B+ 免费 Token/月** —— 参考 OmniRoute 架构，为 SI-LLM-Gateway 构建免费资源智能聚合与自动配置能力。

---

## 📋 目标

从 [OmniRoute](~/workspace/ai/omniroute) 学习其 **免费 Token 供应渠道** 和 **自动快速配置** 能力，结合 SI-LLM-Gateway 现有架构，实现：

1. **免费资源目录** — 维护 90+ 免费 LLM 提供商的配额、ToS、可用性元数据
2. **自动发现与注册** — 自动探测、验证、注册免费渠道到 `provider_catalog`
3. **零配置路由** — 支持 `auto/free`、`auto/best-free` 等虚拟模型 ID，无需手工配置即可调用免费池
4. **配额追踪与轮换** — 本地计量免费 tier 配额，自动切换账号/凭据
5. **ToS 合规过滤** — 根据服务条款风险等级过滤不可用于代理的免费资源

---

## 🎯 核心能力对比

| 能力 | OmniRoute | SI-LLM-Gateway (现状) | OmniFree (目标) |
|------|-----------|----------------------|----------------|
| **免费资源目录** | ✅ 516 模型 × 43 池 | ❌ 无结构化目录 | ✅ Go catalog + JSON |
| **配额分类** | ✅ 6 种 `freeType` | ❌ 仅 `billing_mode='free'` | ✅ 移植分类体系 |
| **自动路由** | ✅ `auto/best-free` | ⚠️ 需手工配置凭据 | ✅ 虚拟 combo |
| **本地配额计量** | ✅ 双窗口 + 429 校准 | ❌ 仅依赖上游 | ✅ 本地 metering |
| **ToS 合规** | ✅ ok/caution/avoid | ❌ 无 ToS 元数据 | ✅ 合规层 |
| **无认证提供商** | ✅ 8 个 keyless | ❌ 未支持 | ✅ 支持 keyless |
| **多账号轮换** | ✅ OAuth token rotation | ⚠️ 基础凭据池 | ✅ 增强轮换 |

---

## 🏗️ 架构设计

### 1. 数据模型

```
provider_catalog (现有)
├── billing_mode: 'free' | 'token_plan' | 'per_token'
└── 新增列 →

free_resource_catalog (新表)
├── id: bigserial
├── provider_code: text (FK → provider_catalog.code)
├── model_id: text
├── display_name: text
├── free_type: text  -- recurring-daily | recurring-monthly | one-time-initial | recurring-uncapped | keyless | discontinued
├── monthly_tokens: bigint
├── daily_tokens: bigint
├── credit_tokens: bigint  -- 首次充值解锁的额外配额
├── pool_key: text  -- 跨提供商共享配额池标识（如 openrouter-free）
├── tos_verdict: text  -- ok | caution | ambiguous | avoid | unknown
├── tos_notes: text
├── constraints_json: jsonb  -- RPM/RPD/并发/窗口限制
├── discovery_method: text  -- manual | auto-scan | community
├── verified_at: timestamptz
├── disabled_at: timestamptz
├── created_at / updated_at / tenant_id (RLS)
└── UNIQUE(provider_code, model_id)

free_quota_tracker (新表) -- 本地配额计量
├── id: bigserial
├── credential_id: bigint (FK)
├── provider_code / model_id
├── window_type: text  -- hour-5 | day-1 | day-7 | month-1
├── window_start: timestamptz
├── request_count: int
├── token_count: bigint
├── last_429_at: timestamptz
├── last_429_reset_after: int  -- 从 Retry-After header 提取
├── created_at / updated_at / tenant_id
└── UNIQUE(credential_id, provider_code, model_id, window_type, window_start)

auto_combo_templates (新表) -- 虚拟路由模板
├── id: bigserial
├── combo_name: text  -- 'auto/free', 'auto/best-free', 'auto/coding:free'
├── variant: text  -- 'cheap' | 'fast' | 'smart' | 'coding'
├── tier_filter: text[]  -- ['free', 'trial']
├── provider_allowlist: text[]
├── provider_denylist: text[]
├── tos_filter: text[]  -- ['ok', 'caution']
├── scoring_weights_json: jsonb
├── enabled: boolean
├── created_at / updated_at / tenant_id
└── UNIQUE(combo_name)
```

### 2. 服务架构

```
domains/freeresource/
├── catalog.go          -- 免费资源目录 CRUD
├── types.go            -- FreeResourceEntry, FreeType, ToSVerdict
├── discovery.go        -- 自动发现 + 验证免费渠道
├── quota_tracker.go    -- 本地配额计量 (双窗口 metering)
├── pool_dedup.go       -- 池去重逻辑 (避免重复计数)
├── tos_compliance.go   -- ToS 风险评估
└── seed_catalog.json   -- 初始免费资源种子数据

domains/autocombo/
├── virtual_factory.go  -- 动态生成虚拟 combo (类似 OmniRoute virtualFactory)
├── builtin_catalog.go  -- 内置 auto/* 模板目录
├── engine.go           -- ScoreTierRotator + 候选池轮换
├── scoring.go          -- 免费资源评分权重 (延迟/健康/配额剩余)
└── resolver.go         -- 解析 auto/xxx 到候选池

bg/freequotasync/
├── worker.go           -- 后台同步免费资源配额状态
├── preflight.go        -- 请求前配额预检 (跳过耗尽账号)
└── rotation.go         -- 账号/凭据自动轮换

provider/keyless/
├── registry.go         -- 无认证提供商注册表
├── opencode.go         -- 示例: OpenCode AI
├── duckduckgo.go       -- 示例: DuckDuckGo Chat
└── felo.go             -- 示例: Felo Search
```

### 3. 请求流程

```
用户请求 → auto/best-free
    ↓
AutoComboResolver.Resolve()
    ↓
根据 tenant + combo_name 查找 auto_combo_templates
    ↓
VirtualFactory.Build()
    ├─ 查询 free_resource_catalog (filter by tos_verdict, free_type)
    ├─ 查询已连接凭据 (JOIN credentials WHERE billing_mode='free')
    ├─ 查询 keyless providers
    ├─ QuotaPreflight: 过滤配额耗尽的凭据
    └─ 构建候选池 CandidatePool[]
    ↓
Engine.SelectCandidate()
    ├─ 计算评分 (health, latency, quota_remaining, cost=0)
    ├─ 分层 Tier (top/mid/rest)
    └─ 轮换选择 (SWRR within tier)
    ↓
执行请求 → upstream provider
    ↓
QuotaTracker.Record(tokens)  -- 本地计量
    ↓
429? → QuotaTracker.CorrectFromHeaders(Retry-After) → 标记耗尽
    ↓
失败? → 自动 fallback 下一候选
```

---

## 📊 OmniRoute 核心模式借鉴

### 1. `freeType` 分类体系
OmniRoute 区分 6 种免费类型（`freeModelCatalog.ts:5-12`）：

```typescript
type FreeType =
  | "recurring-daily"      // 每日刷新配额 (计入总数)
  | "recurring-monthly"    // 每月刷新配额 (计入总数)
  | "one-time-initial"     // 注册赠送 (仅首月, 不计入稳态)
  | "recurring-credit"     // 每月美元额度
  | "recurring-uncapped"   // 永久免费但无官方上限 (不计入总数避免夸大)
  | "keyless"              // 无需认证 (计入总数)
  | "discontinued"         // 已停止 (计为 0)
```

**移植到 Go**:
```go
type FreeType string

const (
    FreeTypeRecurringDaily   FreeType = "recurring-daily"
    FreeTypeRecurringMonthly FreeType = "recurring-monthly"
    FreeTypeOneTimeInitial   FreeType = "one-time-initial"
    FreeTypeRecurringCredit  FreeType = "recurring-credit"
    FreeTypeRecurringUncapped FreeType = "recurring-uncapped"
    FreeTypeKeyless          FreeType = "keyless"
    FreeTypeDiscontinued     FreeType = "discontinued"
)
```

### 2. Pool 去重 (`freeModelCatalog.ts:80`)
避免跨提供商共享配额的重复计数（如 OpenRouter 的 `:free` 后缀模型共享日配额池）：

```typescript
function dedupedSum(budgets: FreeModelBudget[]): bigint {
  const poolMap = new Map<string, bigint>();
  for (const b of budgets) {
    const key = b.poolKey || `${b.provider}:${b.modelId}`;
    const current = poolMap.get(key) || 0n;
    poolMap.set(key, max(current, b.monthlyTokens));
  }
  return Array.from(poolMap.values()).reduce((a, b) => a + b, 0n);
}
```

**Go 实现**:
```go
func ComputeDedupedTotal(entries []FreeResourceEntry) int64 {
    poolMap := make(map[string]int64)
    for _, e := range entries {
        key := e.PoolKey
        if key == "" {
            key = fmt.Sprintf("%s:%s", e.ProviderCode, e.ModelID)
        }
        if e.MonthlyTokens > poolMap[key] {
            poolMap[key] = e.MonthlyTokens
        }
    }
    var total int64
    for _, v := range poolMap {
        total += v
    }
    return total
}
```

### 3. ToS 合规过滤 (`freeTierCatalog.ts:41`)
OmniRoute 维护 ToS 判定表，标记禁止代理使用的提供商：

```typescript
const FREE_TIER_TOS: Record<string, "ok" | "caution" | "ambiguous" | "avoid"> = {
  "openai": "avoid",           // ToS 明确禁止中继
  "anthropic": "avoid",
  "mistral": "ok",
  "groq": "caution",           // 官方未确认但社区可用
  "openrouter": "ok",
  "siliconflow": "ok",
  // ...
}
```

**Go 实现**:
```go
type ToSVerdict string

const (
    ToSOK        ToSVerdict = "ok"        // ToS 明确允许或无限制
    ToSCaution   ToSVerdict = "caution"   // 灰色地带
    ToSAmbiguous ToSVerdict = "ambiguous" // 未找到明确条款
    ToSAvoid     ToSVerdict = "avoid"     // ToS 明确禁止
    ToSUnknown   ToSVerdict = "unknown"   // 未审查
)
```

### 4. 虚拟 Auto Combo (`virtualFactory.ts:150`)
OmniRoute 的 `auto/best-free` 无需预配置，运行时从所有可用凭据 + keyless 提供商动态构建候选池：

```typescript
function createVirtualAutoCombo(variant: string): VirtualAutoCombo {
  const candidates: Candidate[] = [];
  
  // 1. 从已连接的凭据构建候选
  for (const conn of getConnections()) {
    if (hasUsableCredential(conn) && isFreeModel(conn.model)) {
      candidates.push({
        provider: conn.provider,
        model: conn.model,
        costPer1M: 0,
        connectionId: conn.id,
      });
    }
  }
  
  // 2. 添加 keyless 提供商
  for (const [id, meta] of NOAUTH_PROVIDERS) {
    if (AUTO_COMBO_NOAUTH_ALLOWLIST.has(id)) {
      candidates.push({
        provider: id,
        model: meta.defaultModel,
        costPer1M: 0,
        connectionId: SYNTHETIC_NOAUTH_CONNECTION_ID,
      });
    }
  }
  
  return { variant, candidatePool: candidates };
}
```

**Go 适配**: 见 `domains/autocombo/virtual_factory.go`

### 5. 本地配额计量 + 429 校准 (`openrouterFreeWindow.ts`)
大部分免费提供商无 usage API，OmniRoute 本地计数并从 429 响应校准：

```typescript
class OpenRouterFreeWindow {
  private dailyCount = 0;
  private windowStart = startOfDay(new Date());
  
  async beforeRequest() {
    if (this.dailyCount >= this.getDailyLimit()) {
      throw new QuotaExhaustedError();
    }
    this.dailyCount++;
  }
  
  async on429(response: Response) {
    const retryAfter = parseInt(response.headers.get("Retry-After") || "0");
    if (retryAfter > 0) {
      this.windowStart = new Date(Date.now() + retryAfter * 1000);
      this.dailyCount = this.getDailyLimit(); // 标记为耗尽
    }
  }
}
```

**Go 实现**: 见 `domains/freeresource/quota_tracker.go`

---

## 🚀 实施路线图

### Phase 1: 数据模型与种子数据 (P0)
- [ ] 创建 `free_resource_catalog` / `free_quota_tracker` / `auto_combo_templates` 表
- [ ] 从 OmniRoute `freeModelCatalog.data.ts` 提取前 20 个高价值免费资源
- [ ] 生成 Go 种子数据 `domains/freeresource/seed_catalog.json`
- [ ] 实现 `domains/freeresource/catalog.go` 基础 CRUD

### Phase 2: 本地配额追踪 (P0)
- [ ] 实现 `QuotaTracker` (双窗口: 5h + 7d)
- [ ] Hook 到 `domains/streaming/executors` 记录 token 消耗
- [ ] 实现 429 响应头解析 (`Retry-After`, `X-RateLimit-Reset`)
- [ ] 实现 `QuotaPreflight` 预检 (过滤耗尽凭据)

### Phase 3: 虚拟 Auto Combo (P1)
- [ ] 实现 `AutoComboResolver` 解析 `auto/*` 路由
- [ ] 实现 `VirtualFactory.Build()` 动态构建候选池
- [ ] 集成现有 `domains/streaming/executors/router_scoring.go`
- [ ] 支持 `auto/free`, `auto/best-free`, `auto/coding:free` 3 个内置模板

### Phase 4: Keyless 提供商 (P1)
- [ ] 实现 `provider/keyless/registry.go`
- [ ] 适配 3 个示例 keyless 提供商 (opencode, duckduckgo-web, felo)
- [ ] 实现合成凭据 `SYNTHETIC_KEYLESS_CREDENTIAL_ID`
- [ ] 集成到虚拟 combo 候选池

### Phase 5: ToS 合规与过滤 (P2)
- [ ] 从 OmniRoute `FREE_TIER_TOS` 移植 ToS 判定
- [ ] 实现 `domains/freeresource/tos_compliance.go`
- [ ] Admin UI: 免费资源审查面板 (显示 ToS 风险)

### Phase 6: 自动发现 (P3)
- [ ] 实现 `domains/freeresource/discovery.go` (定期扫描)
- [ ] 集成 OmniRoute 的 provider 探测逻辑
- [ ] 后台 worker: 每日验证免费资源可用性

---

## 📁 文档结构

```
docs/omnifree/
├── 00-OVERVIEW.md                    (本文档)
├── 01-DATA-MODEL.md                  (数据库 schema 详细设计)
├── 02-QUOTA-TRACKING.md              (本地配额追踪设计)
├── 03-AUTO-COMBO.md                  (虚拟路由设计)
├── 04-KEYLESS-PROVIDERS.md           (无认证提供商接入)
├── 05-TOS-COMPLIANCE.md              (ToS 合规过滤)
├── 06-DISCOVERY.md                   (自动发现机制)
├── 07-MIGRATION-GUIDE.md             (从现有系统迁移)
├── 08-TESTING-PLAN.md                (测试方案)
├── 09-DEPLOYMENT.md                  (部署与灰度)
├── 10-AUDIT-CHECKLIST.md             (审计清单)
└── seed/
    ├── free_resource_catalog.json    (初始免费资源数据)
    ├── auto_combo_templates.json     (内置 auto/* 模板)
    └── keyless_providers.json        (无认证提供商列表)
```

---

## 🎯 成功指标

1. **覆盖率**: 接入 ≥50 个免费 LLM 提供商 (OmniRoute 的 90+ 中筛选高价值)
2. **配额总量**: 聚合 ≥500M tokens/月 免费配额
3. **ToS 合规**: 100% 免费资源标注 ToS 风险等级
4. **零配置体验**: 用户无需手工添加凭据即可调用 `auto/free`
5. **配额准确性**: 本地计量误差 <5% (通过 429 校准)
6. **自动轮换**: 配额耗尽时自动切换账号，成功率 >95%

---

## 📚 参考资料

- [OmniRoute 源码](~/workspace/ai/omniroute)
  - `open-sse/config/freeModelCatalog.data.ts` — 516 免费模型目录
  - `open-sse/services/autoCombo/virtualFactory.ts` — 虚拟 combo 工厂
  - `open-sse/services/quotaPreflight.ts` — 配额预检
  - `src/shared/constants/providers/noauth.ts` — keyless 提供商
- [OmniRoute 文档](~/workspace/ai/omniroute/docs/reference/FREE_TIERS.md)
- [SI-LLM-Gateway 架构](./docs/architecture/ARCHITECTURE.md)

---

**下一步**: 阅读 `01-DATA-MODEL.md` 了解详细数据库设计。

# 供应商凭据余额查询 - 现状分析与优化方案

**编制时间**: 2026-09-17（2026-09-18 实施审计修正）  
**编制人**: AI Assistant  
**审核状态**: 已实施（迁移 721）

---

## ⚠️ 审计修正记录（2026-09-18）

提交前对本文档做了逐条代码核查，修正以下失实/过时内容：

1. **"建议新增 `balance_checked_at` / `balance_currency` 字段"是重复建议**。
   两个字段早已存在（`01-schema.sql` L6869-6870：`balance_currency text DEFAULT 'USD'`、
   `balance_last_checked_at timestamptz`），问题只是 listCredentials SQL 未
   SELECT、前端未展示。真正缺失的是 `balance_source` 与 `balance_error`
   两列（迁移 721 新增）。
2. **实际支持的余额/配额探测厂商为 6 家，不是更多**：
   - 货币余额 API（providercap）：`openai`、`deepseek`、`siliconflow`、`openrouter`
     （openrouter 走 /api/v1/key 专用 fetcher，仅 quotafetcher 请求路径；后台
     floor guard 只覆盖 openai/deepseek/siliconflow）。
   - 订阅套餐窗口（balance_floor_guard）：`zhipu`、`minimax`（5h/7d 窗口百分比
     + zhipu 绝对 token 余量）。
   - **Anthropic、Gemini、Moonshot、阿里云百炼、腾讯混元等无公开余额 API，
     不在探测范围内**。会话早期总结中"已支持 8+ 供应商"的说法是错误的，
     以本节为准。
3. **后台刷新节奏澄清**：floor guard 默认 5 分钟 sweep，但 Pass A 只刷新
   配置了 `balance_floor_usd` 的凭据（15 分钟新鲜度门控）；
   probe_v2 cycleAll 每小时跑一次、仅对探测成功且厂商有余额 API 的行写
   `balance_usd`。"所有凭据每 5 分钟刷新"的说法不准确。
4. **"手工输入余额可能被自动覆盖"风险确认属实但范围有限**：仅影响配置了
   `balance_floor_usd` 的凭据（5 分钟内被 Pass A 覆盖）与 openai/deepseek/
   siliconflow 凭据（1 小时内被 cycleAll 覆盖）。本次已实施 24h 手工保护窗
   （见 §2.2 修订版）。

---

## 1. 现状分析

### 1.1 现有实现架构

#### 数据库层
- **表**: `credentials`
- **余额相关字段**:
  - `balance_usd` (numeric): 当前余额（美元）
  - `balance_currency` (text, DEFAULT 'USD'): 货币单位（**已存在**）
  - `balance_last_checked_at` (timestamptz): 余额最后检查时间（**已存在**）
  - `balance_check_endpoint` (text): 余额查询端点模板（**已存在**）
  - `balance_source` (text): 余额来源 manual/api（**迁移 721 新增**）
  - `balance_error` (text): 最近一次探测失败摘要（**迁移 721 新增**）
  - `balance_floor_usd` (numeric): 余额下限阈值
  - `quota_floor_tokens` (bigint): 套餐token下限
  - `quota_floor_percent` (numeric): 套餐使用百分比下限
  - `plan_quota_kind` (text): 套餐类型标识
  - `plan_quota_windows` (jsonb): 套餐窗口详情
  - `plan_quota_remaining_tokens` (bigint): 剩余token
  - `plan_quota_used_percent` (float): 已用百分比
  - `plan_quota_checked_at` (timestamp): 套餐最后检查时间

#### 后端实现

**API接口** (`admin/provider_credential.go`):
- `GET /api/providers/{id}/credentials` - 返回凭据列表（含 balance_usd）
- `PATCH /api/providers/{id}/credentials/{cid}` - 更新凭据（含 balance_usd）

**后台任务** (`bg/balance_floor_guard.go`):
- **目标**: 主动余额守卫，防止原厂凭据额度打光
- **支持厂商**:
  - **货币余额**: OpenAI, DeepSeek, SiliconFlow (通过 providercap)
  - **套餐查询**: Zhipu GLM, MiniMax
- **运行周期**: 5分钟（可配置 LLM_GATEWAY_BALANCE_FLOOR_INTERVAL）
- **刷新策略**: 仅刷新配置了下限的凭据（15分钟新鲜度）
- **失败策略**: Fail-open（保留上次状态，不用过期数据做摘出决策）

**供应商能力层** (`internal/providercap/`):
- 封装了各厂商的余额查询逻辑
- 当前未在代码中找到完整实现文件（可能在其他模块）

#### 前端实现

**使用场景**:
1. **ProvidersView.vue** (L500):
   - 凭据管理弹窗中的手工输入字段
   - 保存时传递给后端 `balance_usd`

2. **CredsTab.vue** (凭据详情抽屉):
   - 显示和编辑余额
   - 使用 `money()` 格式化函数

3. **PricingManagementView.vue** (L364-365):
   - 显示模型报价的余额信息
   - 格式: `余额: {balance_usd} {balance_currency || 'USD'}`

4. **ErrorDetailTab.vue** (L132):
   - 显示凭据错误详情时的余额信息

**显示格式**:
```typescript
function money(v: number | string | null | undefined) {
  if (v == null) return '—'
  const n = typeof v === 'string' ? Number(v) : v
  return Number.isNaN(n) ? '—' : `$${n.toFixed(4)}`
}
```

### 1.2 现有问题

#### 问题 1: 缺少余额元数据
**严重程度**: 🔴 高

**问题描述**:
- 数据库中没有 `balance_checked_at` 字段记录最后查询时间
- 没有 `balance_source` 字段标识数据来源（手工/API自动）
- 操作员无法判断余额数据的时效性和可信度

**影响**:
- 手工输入的余额可能被后台任务静默覆盖
- 无法区分是最近API查询结果还是几天前的旧数据
- 查询失败时前端无感知

**实际场景**:
```
操作员在 10:00 手工输入余额 $100
后台任务在 10:05 查询到 API 余额 $95，覆盖了手工输入
操作员在 10:10 重新打开看到 $95，不知道为什么变了
```

#### 问题 2: 货币单位不统一
**严重程度**: 🟡 中

**问题描述**:
- 部分视图使用 `balance_currency` 字段，但数据库 schema 中未找到此字段
- 默认假设为 USD，但某些厂商可能返回其他货币（如 CNY）
- 前端显示时货币符号硬编码为 `$`

**影响**:
- 混合货币时数值比较失真
- 人民币余额被当作美元显示

#### 问题 3: 查询能力未充分暴露
**严重程度**: 🟡 中

**问题描述**:
- `balance_floor_guard.go` 已实现余额查询，但仅用于守卫摘出
- 前端没有"立即刷新余额"按钮
- 不支持按需查询单个凭据余额

**影响**:
- 操作员必须等待后台任务周期（5分钟）
- 无法主动验证配置的 API Key 余额

#### 问题 4: 套餐查询与货币余额混在一起
**严重程度**: 🟢 低

**问题描述**:
- `balance_usd` (货币) 和 `plan_quota_*` (套餐) 是两种不同的概念
- 前端仅显示 `balance_usd`，对套餐类凭据显示为空
- Zhipu/MiniMax 的套餐信息未在凭据列表中直观展示

**影响**:
- 套餐类凭据看起来没有余额信息
- 操作员需要去专门的套餐查询界面查看

#### 问题 5: 批量查询效率低
**严重程度**: 🟢 低

**问题描述**:
- 后台任务逐个查询每个凭据，没有批量优化
- 某些厂商 API 支持批量查询但未利用

**影响**:
- 凭据数量多时查询耗时长
- API 调用次数多，可能触发限流

---

## 2. 优化方案

### 2.1 方案概览

| 优先级 | 优化项 | 工作量 | 影响范围 |
|--------|--------|--------|----------|
| P0 | 添加余额元数据字段 | 2h | DB + Backend + Frontend |
| P0 | 前端显示查询时间和来源 | 1h | Frontend |
| P1 | 提供"立即刷新"按钮 | 3h | Backend + Frontend |
| P1 | 区分手工输入和自动查询 | 2h | Backend + Frontend |
| P2 | 统一货币单位处理 | 2h | DB + Backend + Frontend |
| P2 | 暴露套餐信息到凭据列表 | 2h | Frontend |
| P3 | 支持更多厂商余额查询 | 8h+ | Backend (providercap) |

### 2.2 P0 优化：添加余额元数据 —— ✅ 已实施（迁移 721，2026-09-18）

> 审计修订：原方案的 4 个 ADD COLUMN 中 `balance_checked_at` / `balance_currency`
> 是重复建议（两列早已存在），且原编号 `036` 与仓库现行编号体系（7xx startup
> 迁移）不符。实际落地为 `sql/migrations/startup/721_credential_balance_source_and_error.sql`
> （+ installer embeddata 同步），只新增真正缺失的两列：
>
> **实际迁移内容**:
-- ```sql
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS balance_source text;
ALTER TABLE credentials ADD COLUMN IF NOT EXISTS balance_error text;
-- CHECK 约束 credentials_balance_source_chk: NULL | 'manual' | 'api'
-- ```
>
> `balance_currency` / `balance_last_checked_at` 复用既有列；原方案中的排序
> 索引未建（凭据表量级 ≤ 1e4，全表扫描足够，避免热表冗余索引）。

#### 数据库迁移（已验证：本地 PG 事务内建表+回滚，列/约束创建成功）
COMMENT ON COLUMN credentials.balance_currency IS '余额货币单位，ISO 4217 代码';
COMMENT ON COLUMN credentials.balance_error IS '最后一次余额查询的错误信息（NULL=成功）';
```

#### 后端 API 扩展
```go
// admin/provider_credential.go

type CredentialResponse struct {
    // ... 现有字段 ...
    
    BalanceUSD         *float64   `json:"balance_usd"`
    BalanceCheckedAt   *time.Time `json:"balance_checked_at"`
    BalanceSource      *string    `json:"balance_source"`
    BalanceCurrency    string     `json:"balance_currency"`
    BalanceError       *string    `json:"balance_error"`
}

// listCredentials 中添加新字段查询
func (h *Handler) listCredentials(w http.ResponseWriter, r *http.Request, providerID int) {
    // ... 现有代码 ...
    rows, err := h.db.Query(ctx, `
        SELECT c.id, c.provider_id, ...,
               c.balance_usd::float8,
               c.balance_checked_at,
               c.balance_source,
               COALESCE(c.balance_currency, 'USD'),
               c.balance_error,
               ...
        FROM credentials c
        WHERE c.provider_id = $1 AND c.status <> 'deleted'
        ORDER BY c.id
    `, providerID)
    // ...
}

// updateCredential 中处理手工输入
func (h *Handler) updateCredential(w http.ResponseWriter, r *http.Request, ...) {
    // ...
    if req.BalanceUSD != nil {
        sets = append(sets, "balance_usd = "+arg(*req.BalanceUSD))
        // 手工输入时标记来源和时间
        sets = append(sets, "balance_source = 'manual'")
        sets = append(sets, "balance_checked_at = NOW()")
        sets = append(sets, "balance_error = NULL")
    }
    // ...
}
```

#### 后台任务更新
```go
// bg/balance_floor_guard.go

// probeBalance 探测成功后更新元数据
func (g *BalanceFloorGuard) updateBalanceWithMetadata(
    ctx context.Context, 
    credID int64, 
    balance float64, 
    currency string,
) error {
    _, err := g.db.Exec(ctx, `
        UPDATE credentials 
        SET balance_usd = $1,
            balance_currency = $2,
            balance_checked_at = NOW(),
            balance_source = 'api',
            balance_error = NULL
        WHERE id = $3
    `, balance, currency, credID)
    return err
}

// probeBalance 失败时仅更新错误信息
func (g *BalanceFloorGuard) updateBalanceError(
    ctx context.Context, 
    credID int64, 
    errMsg string,
) error {
    _, err := g.db.Exec(ctx, `
        UPDATE credentials 
        SET balance_error = $1,
            balance_checked_at = NOW()
        WHERE id = $2
    `, errMsg, credID)
    return err
}
```

### 2.3 P0 优化：前端显示元数据

#### TypeScript 类型扩展
```typescript
// web/src/api/providers.ts

export interface ProviderCredential {
  // ... 现有字段 ...
  
  balance_usd: number | null
  balance_checked_at: string | null  // ISO 8601
  balance_source: 'manual' | 'api' | 'estimate' | null
  balance_currency: string  // default 'USD'
  balance_error: string | null
}
```

#### 凭据详情显示优化
```vue
<!-- web/src/views/provider-detail/CredsTab.vue -->

<template>
  <!-- 余额字段组 -->
  <div class="form-row">
    <label>{{ pd('creds.balance') }}</label>
    <div class="balance-group">
      <input 
        v-model.number="selected.balance_usd" 
        type="number" 
        step="0.01"
        :disabled="!canManageCreds"
        class="balance-input"
      />
      <span class="currency">{{ selected.balance_currency || 'USD' }}</span>
      
      <!-- 元数据提示 -->
      <div v-if="selected.balance_checked_at" class="balance-meta">
        <span class="meta-icon" :class="balanceSourceIcon(selected.balance_source)">
          {{ balanceSourceLabel(selected.balance_source) }}
        </span>
        <span class="meta-time" :title="fmtDateTime(selected.balance_checked_at)">
          {{ timeAgo(selected.balance_checked_at) }}
        </span>
      </div>
      
      <!-- 错误提示 -->
      <div v-if="selected.balance_error" class="balance-error">
        ⚠️ {{ selected.balance_error }}
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
function balanceSourceIcon(source: string | null): string {
  if (source === 'api') return '🔄'
  if (source === 'manual') return '✏️'
  if (source === 'estimate') return '📊'
  return '❓'
}

function balanceSourceLabel(source: string | null): string {
  const labels = {
    api: pd('creds.balanceSource.api'),      // 'API自动'
    manual: pd('creds.balanceSource.manual'), // '手工输入'
    estimate: pd('creds.balanceSource.estimate'), // '估算'
  }
  return labels[source as keyof typeof labels] || pd('creds.balanceSource.unknown')
}

function timeAgo(iso: string | null): string {
  if (!iso) return ''
  const diff = (Date.now() - new Date(iso).getTime()) / 1000
  if (diff < 60) return pd('time.justNow')
  if (diff < 3600) return `${Math.floor(diff / 60)}${pd('time.minutesAgo')}`
  if (diff < 86400) return `${Math.floor(diff / 3600)}${pd('time.hoursAgo')}`
  return `${Math.floor(diff / 86400)}${pd('time.daysAgo')}`
}
</script>

<style scoped>
.balance-group {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.balance-input {
  width: 120px;
  display: inline-block;
}

.currency {
  margin-left: 8px;
  color: var(--text-secondary);
  font-size: 0.9em;
}

.balance-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 0.85em;
  color: var(--text-secondary);
}

.meta-icon::before {
  content: attr(data-icon);
  margin-right: 4px;
}

.balance-error {
  color: var(--danger);
  font-size: 0.85em;
  padding: 4px 8px;
  background: var(--danger-bg);
  border-radius: 4px;
}
</style>
```

### 2.4 P1 优化：立即刷新余额

#### 后端 API 新增端点
```go
// admin/provider_credential.go

// POST /api/providers/{provider_id}/credentials/{credential_id}/refresh-balance
func (h *Handler) refreshCredentialBalance(w http.ResponseWriter, r *http.Request) {
    providerID, _ := strconv.Atoi(chi.URLParam(r, "provider_id"))
    credentialID, _ := strconv.Atoi(chi.URLParam(r, "credential_id"))
    
    ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
    defer cancel()
    
    // 1. 查询凭据信息
    var (
        apiKey     string
        baseURL    string
        vendorCode string
    )
    err := h.db.QueryRow(ctx, `
        SELECT 
            pgp_sym_decrypt(c.secret_ciphertext, $1) AS api_key,
            p.base_url,
            p.code AS vendor_code
        FROM credentials c
        JOIN providers p ON c.provider_id = p.id
        WHERE c.id = $2 AND c.provider_id = $3
    `, secret.GetKey(), credentialID, providerID).Scan(&apiKey, &baseURL, &vendorCode)
    
    if err != nil {
        writeError(w, http.StatusNotFound, "credential not found")
        return
    }
    
    // 2. 调用 providercap 查询余额
    balance, currency, err := providercap.QueryBalance(ctx, vendorCode, baseURL, apiKey)
    
    if err != nil {
        // 记录错误但返回 200（fail-open）
        _ = h.updateBalanceError(ctx, int64(credentialID), err.Error())
        writeJSON(w, map[string]any{
            "success": false,
            "error": err.Error(),
            "balance_usd": nil,
        })
        return
    }
    
    // 3. 更新数据库
    _, err = h.db.Exec(ctx, `
        UPDATE credentials
        SET balance_usd = $1,
            balance_currency = $2,
            balance_checked_at = NOW(),
            balance_source = 'api',
            balance_error = NULL
        WHERE id = $3
    `, balance, currency, credentialID)
    
    if err != nil {
        writeError(w, http.StatusInternalServerError, "update failed")
        return
    }
    
    // 4. 返回最新余额
    writeJSON(w, map[string]any{
        "success": true,
        "balance_usd": balance,
        "balance_currency": currency,
        "balance_checked_at": time.Now().Format(time.RFC3339),
        "balance_source": "api",
    })
}

// 注册路由
func (h *Handler) RegisterRoutes(r chi.Router) {
    // ... 现有路由 ...
    r.Post("/providers/{provider_id}/credentials/{credential_id}/refresh-balance", h.refreshCredentialBalance)
}
```

#### 前端刷新按钮
```vue
<!-- web/src/views/provider-detail/CredsTab.vue -->

<template>
  <div class="form-row">
    <label>{{ pd('creds.balance') }}</label>
    <div class="balance-group">
      <div class="balance-input-group">
        <input 
          v-model.number="selected.balance_usd" 
          type="number" 
          step="0.01"
          :disabled="!canManageCreds || refreshingBalance"
          class="balance-input"
        />
        <span class="currency">{{ selected.balance_currency || 'USD' }}</span>
        
        <!-- 刷新按钮 -->
        <button
          v-if="canManageCreds"
          type="button"
          class="btn btn-sm btn-ghost"
          :disabled="refreshingBalance"
          @click="refreshBalance"
          :title="pd('creds.refreshBalanceHint')"
        >
          <span v-if="refreshingBalance" class="spinner">⏳</span>
          <span v-else>🔄</span>
          {{ pd('creds.refreshBalance') }}
        </button>
      </div>
      
      <!-- ... 元数据显示 ... -->
    </div>
  </div>
</template>

<script setup lang="ts">
import { refreshCredentialBalance } from '../../api/providers'

const refreshingBalance = ref(false)

async function refreshBalance() {
  if (!selected.value || refreshingBalance.value) return
  
  refreshingBalance.value = true
  saveMsg.value = ''
  
  try {
    const result = await refreshCredentialBalance(
      props.provider.id,
      selected.value.id
    )
    
    if (result.success) {
      // 更新本地状态
      selected.value.balance_usd = result.balance_usd
      selected.value.balance_currency = result.balance_currency
      selected.value.balance_checked_at = result.balance_checked_at
      selected.value.balance_source = result.balance_source
      selected.value.balance_error = null
      
      saveMsg.value = pd('creds.balanceRefreshed')
      saveMsgKind.value = 'success'
      
      // 触发父组件静默刷新
      emit('silentRefresh')
    } else {
      // API 调用成功但余额查询失败
      selected.value.balance_error = result.error
      saveMsg.value = pd('creds.balanceRefreshFailed', { error: result.error })
      saveMsgKind.value = 'error'
    }
  } catch (e: unknown) {
    saveMsg.value = e instanceof Error ? e.message : pd('creds.balanceRefreshError')
    saveMsgKind.value = 'error'
  } finally {
    refreshingBalance.value = false
  }
}
</script>
```

#### API 客户端函数
```typescript
// web/src/api/providers.ts

export interface RefreshBalanceResult {
  success: boolean
  balance_usd: number | null
  balance_currency: string
  balance_checked_at: string
  balance_source: 'api'
  error?: string
}

export async function refreshCredentialBalance(
  providerId: number,
  credentialId: number
): Promise<RefreshBalanceResult> {
  const res = await req<RefreshBalanceResult>(
    `/api/providers/${providerId}/credentials/${credentialId}/refresh-balance`,
    { method: 'POST' }
  )
  return res
}
```

### 2.5 P1 优化：区分手工输入和自动查询

#### 保护手工输入的余额
```go
// admin/provider_credential.go

// updateCredential: 手工输入时设置标记，后台任务跳过
func (h *Handler) updateCredential(w http.ResponseWriter, r *http.Request, ...) {
    // ...
    if req.BalanceUSD != nil {
        sets = append(sets, "balance_usd = "+arg(*req.BalanceUSD))
        sets = append(sets, "balance_source = 'manual'")
        sets = append(sets, "balance_checked_at = NOW()")
        sets = append(sets, "balance_error = NULL")
        
        // 可选：添加过期时间，手工输入24小时后允许自动覆盖
        // sets = append(sets, "balance_manual_expires_at = NOW() + INTERVAL '24 hours'")
    }
    // ...
}
```

```go
// bg/balance_floor_guard.go

// sweep: 跳过手工输入的余额（可选：或仅跳过24小时内的）
func (g *BalanceFloorGuard) selectBalanceCredentials(ctx context.Context) ([]balanceCredential, error) {
    rows, err := g.db.Query(ctx, `
        SELECT c.id, c.provider_id, ...
        FROM credentials c
        JOIN providers p ON c.provider_id = p.id
        WHERE c.balance_floor_usd IS NOT NULL
          AND c.status NOT IN ('deleted', 'disabled')
          -- 跳过手工输入的余额（或仅跳过未过期的）
          AND (c.balance_source IS NULL 
               OR c.balance_source != 'manual'
               -- 可选：OR c.balance_checked_at < NOW() - INTERVAL '24 hours'
              )
          -- 15分钟新鲜度：上次查询距今 > 15min 才重查
          AND (c.balance_checked_at IS NULL 
               OR c.balance_checked_at < NOW() - INTERVAL '15 minutes')
        ORDER BY COALESCE(c.balance_checked_at, '1970-01-01') ASC
        LIMIT 100
    `)
    // ...
}
```

### 2.6 P2 优化：统一货币处理

#### 货币转换工具
```go
// internal/currency/converter.go

package currency

import (
    "context"
    "fmt"
    "sync"
    "time"
)

// 汇率缓存（实际生产应从外部API获取）
var (
    rates = map[string]float64{
        "USD": 1.0,
        "CNY": 0.14,  // 1 CNY ≈ 0.14 USD
        "EUR": 1.08,
        "GBP": 1.26,
    }
    ratesMu sync.RWMutex
    ratesUpdatedAt time.Time
)

// ToUSD converts amount in given currency to USD
func ToUSD(amount float64, currency string) (float64, error) {
    if currency == "" || currency == "USD" {
        return amount, nil
    }
    
    ratesMu.RLock()
    rate, ok := rates[currency]
    ratesMu.RUnlock()
    
    if !ok {
        return 0, fmt.Errorf("unsupported currency: %s", currency)
    }
    
    return amount * rate, nil
}

// Format formats amount with currency symbol
func Format(amount float64, currency string) string {
    symbols := map[string]string{
        "USD": "$",
        "CNY": "¥",
        "EUR": "€",
        "GBP": "£",
    }
    
    symbol, ok := symbols[currency]
    if !ok {
        symbol = currency + " "
    }
    
    return fmt.Sprintf("%s%.2f", symbol, amount)
}
```

#### 前端货币显示
```typescript
// web/src/utils/currency.ts

export function formatBalance(
  amount: number | null,
  currency: string = 'USD'
): string {
  if (amount == null) return '—'
  
  const symbols: Record<string, string> = {
    'USD': '$',
    'CNY': '¥',
    'EUR': '€',
    'GBP': '£',
  }
  
  const symbol = symbols[currency] || `${currency} `
  return `${symbol}${amount.toFixed(2)}`
}

// 凭据列表统一显示为 USD 等值
export function normalizeToUSD(
  amount: number,
  currency: string
): number {
  const rates: Record<string, number> = {
    'USD': 1.0,
    'CNY': 0.14,
    'EUR': 1.08,
    'GBP': 1.26,
  }
  
  return amount * (rates[currency] || 1.0)
}
```

### 2.7 P2 优化：暴露套餐信息

#### 前端统一余额显示组件
```vue
<!-- web/src/components/CredentialBalance.vue -->

<template>
  <div class="credential-balance">
    <!-- 货币余额 -->
    <div v-if="credential.balance_usd != null" class="balance-item">
      <span class="label">{{ t('balance') }}:</span>
      <span class="value">{{ formatBalance(credential.balance_usd, credential.balance_currency) }}</span>
      <span v-if="credential.balance_checked_at" class="meta">
        ({{ timeAgo(credential.balance_checked_at) }})
      </span>
    </div>
    
    <!-- 套餐余额 -->
    <div v-if="hasPlanQuota" class="balance-item">
      <span class="label">{{ t('planQuota') }}:</span>
      
      <!-- Token 余额 -->
      <span v-if="credential.plan_quota_remaining_tokens != null" class="value">
        {{ formatTokens(credential.plan_quota_remaining_tokens) }} tokens
      </span>
      
      <!-- 使用百分比 -->
      <span v-if="credential.plan_quota_used_percent != null" class="value">
        {{ credential.plan_quota_used_percent.toFixed(1) }}% {{ t('used') }}
      </span>
      
      <!-- 套餐类型 -->
      <span class="meta">
        ({{ planKindLabel(credential.plan_quota_kind) }})
      </span>
      
      <span v-if="credential.plan_quota_checked_at" class="meta">
        {{ timeAgo(credential.plan_quota_checked_at) }}
      </span>
    </div>
    
    <!-- 余额错误 -->
    <div v-if="credential.balance_error" class="balance-error">
      ⚠️ {{ credential.balance_error }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { ProviderCredential } from '../api/providers'

const props = defineProps<{
  credential: ProviderCredential
}>()

const hasPlanQuota = computed(() => 
  props.credential.plan_quota_kind != null &&
  (props.credential.plan_quota_remaining_tokens != null ||
   props.credential.plan_quota_used_percent != null)
)

function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

function planKindLabel(kind: string | null): string {
  const labels: Record<string, string> = {
    'zhipu_plan': 'Zhipu 套餐',
    'minimax_plan': 'MiniMax 套餐',
  }
  return kind ? (labels[kind] || kind) : ''
}
</script>
```

### 2.8 P3 优化：扩展厂商支持

#### providercap 接口标准化
```go
// internal/providercap/balance.go (新文件)

package providercap

import (
    "context"
    "fmt"
)

// BalanceResult represents the result of a balance query
type BalanceResult struct {
    Amount   float64
    Currency string  // ISO 4217 code
    Source   string  // API endpoint or method used
}

// BalanceQuerier defines the interface for vendor balance queries
type BalanceQuerier interface {
    // QueryBalance queries the current balance for a credential
    QueryBalance(ctx context.Context, baseURL, apiKey string) (*BalanceResult, error)
    
    // SupportsBalanceQuery reports whether this vendor supports balance queries
    SupportsBalanceQuery() bool
}

// Registry of balance queriers by vendor code
var balanceQueriers = map[string]BalanceQuerier{
    "openai":       &OpenAIBalanceQuerier{},
    "deepseek":     &DeepSeekBalanceQuerier{},
    "siliconflow":  &SiliconFlowBalanceQuerier{},
    "zhipu":        &ZhipuBalanceQuerier{},
    "minimax":      &MinimaxBalanceQuerier{},
    // 待扩展:
    // "anthropic":    &AnthropicBalanceQuerier{},
    // "cohere":       &CohereBalanceQuerier{},
}

// QueryBalance is the unified entry point for balance queries
func QueryBalance(ctx context.Context, vendorCode, baseURL, apiKey string) (float64, string, error) {
    querier, ok := balanceQueriers[vendorCode]
    if !ok {
        return 0, "", fmt.Errorf("vendor %s does not support balance queries", vendorCode)
    }
    
    if !querier.SupportsBalanceQuery() {
        return 0, "", fmt.Errorf("vendor %s balance query not implemented", vendorCode)
    }
    
    result, err := querier.QueryBalance(ctx, baseURL, apiKey)
    if err != nil {
        return 0, "", err
    }
    
    return result.Amount, result.Currency, nil
}

// ListSupportedVendors returns vendor codes that support balance queries
func ListSupportedVendors() []string {
    var vendors []string
    for code, querier := range balanceQueriers {
        if querier.SupportsBalanceQuery() {
            vendors = append(vendors, code)
        }
    }
    return vendors
}
```

#### 具体厂商实现示例
```go
// internal/providercap/openai_balance.go

package providercap

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
)

type OpenAIBalanceQuerier struct{}

func (q *OpenAIBalanceQuerier) SupportsBalanceQuery() bool {
    return true
}

func (q *OpenAIBalanceQuerier) QueryBalance(ctx context.Context, baseURL, apiKey string) (*BalanceResult, error) {
    // OpenAI 余额查询端点
    // 注意：OpenAI 官方 API 不直接提供余额查询，需使用 Dashboard API
    // 或第三方余额接口（如 https://api.openai.com/dashboard/billing/credit_grants）
    
    endpoint := fmt.Sprintf("%s/dashboard/billing/credit_grants", baseURL)
    
    req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
    if err != nil {
        return nil, err
    }
    
    req.Header.Set("Authorization", "Bearer "+apiKey)
    req.Header.Set("User-Agent", "llm-gateway-balance-checker/1.0")
    
    client := &http.Client{Timeout: 10 * time.Second}
    resp, err := client.Do(req)
    if err != nil {
        return nil, fmt.Errorf("request failed: %w", err)
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
    }
    
    var result struct {
        TotalGranted float64 `json:"total_granted"`
        TotalUsed    float64 `json:"total_used"`
        TotalAvailable float64 `json:"total_available"`
    }
    
    if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, fmt.Errorf("parse response failed: %w", err)
    }
    
    return &BalanceResult{
        Amount:   result.TotalAvailable,
        Currency: "USD",
        Source:   "openai-dashboard-api",
    }, nil
}
```

---

## 3. 实施计划

> **2026-09-18 实施状态**（逐项对照代码验证，非声明式打勾）：
>
> **已完成（本提交）**:
> - [x] 迁移 `721_credential_balance_source_and_error.sql`（+installer embeddata 同步；本地 PG 事务内验证后回滚）
> - [x] `listCredentials` 返回 balance_currency / balance_last_checked_at / balance_source / balance_error（admin/provider_credential.go）
> - [x] `updateCredential` 手工写入时标记 `balance_source='manual'` + 刷新 checked_at + 清空 error
> - [x] `bg/balance_floor_guard.go` Pass A 候选 SELECT 跳过 manual 24h 保护窗；refreshBalance 成功写 source='api'
> - [x] `bg/credential_probe_v2.go` cycleAll 余额 UPDATE 加同样的 manual 保护 WHERE
> - [x] 新端点 `POST /api/providers/{pid}/credentials/{cid}/refresh-balance`（admin/provider_credential_balance.go，路由挂 providerConsole）
> - [x] 前端凭据抽屉：⟳ 立即刷新按钮 + 来源/时间/错误展示（ProvidersView.vue，Manage Credentials 抽屉 usage 列）
> - [x] `web/src/api/providers.ts` 类型 + refreshCredentialBalance()；zh-CN/en-US i18n key（其余语言 fallback 到 en）
> - [x] 单测 TestTruncateBalanceError；go build/vet、bg Balance 测试、providercap 测试、vue-tsc、vite build 全绿
>
> **未做（如实记录，非本次范围或刻意不做）**:
> - [ ] refresh-balance handler 的 DB 依赖端到端单测（admin 包现有测试无 sqlmock 基建，仅 truncate 纯函数有单测）
> - [ ] balance_floor_guard 探测**失败**路径写 balance_error（刻意不做：失败路径已有 warnGate 限流 + #12a 退避，避免与既有退避语义纠缠）
> - [ ] CredsTab.vue（供应商详情页凭据抽屉）的同款 UI —— 只做了 /providers 列表页抽屉，详情页可复用同 API 后续接入
> - [ ] §2.7/§2.8 的套餐展示组件与更多厂商接入（Anthropic/Gemini/Moonshot 等无公开余额 API，维持不支持）

### 3.1 阶段 1: P0 基础设施（已完成，见上方状态块）
   - [ ] 样式调整

### 3.2 阶段 2: P1 交互增强（第 2 周）

**目标**: 提供立即刷新功能

1. **Day 1-2: 后端 API**
   - [ ] 实现 `refreshCredentialBalance` 端点
   - [ ] 集成 providercap 查询逻辑
   - [ ] 错误处理和限流
   - [ ] API 集成测试

2. **Day 3-4: 前端刷新按钮**
   - [ ] 添加刷新按钮 UI
   - [ ] 实现刷新逻辑和加载状态
   - [ ] 错误提示和成功反馈
   - [ ] 用户体验测试

3. **Day 5: 手工输入保护**
   - [ ] 后台任务跳过手工输入逻辑
   - [ ] 前端提示手工输入会被保护
   - [ ] 集成测试

### 3.3 阶段 3: P2 体验优化（第 3 周）

**目标**: 货币统一和套餐显示

1. **Day 1-2: 货币转换**
   - [ ] 实现货币转换工具
   - [ ] 前端统一显示逻辑
   - [ ] 汇率缓存机制

2. **Day 3-4: 套餐信息显示**
   - [ ] 创建 `CredentialBalance.vue` 组件
   - [ ] 集成到凭据列表和详情
   - [ ] 样式和布局优化

3. **Day 5: 回归测试**
   - [ ] 端到端测试
   - [ ] 性能测试
   - [ ] 文档更新

### 3.4 阶段 4: P3 长期扩展（持续）

**目标**: 扩展更多厂商支持

- [ ] 标准化 providercap 接口
- [ ] 实现 Anthropic 余额查询
- [ ] 实现 Cohere 余额查询
- [ ] 实现 Azure OpenAI 余额查询
- [ ] 批量查询优化

---

## 4. 风险评估

### 4.1 技术风险

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| 厂商 API 变更 | 🟡 中 | 🔴 高 | 版本检测 + fallback 逻辑 |
| 查询限流 | 🟡 中 | 🟡 中 | 退避重试 + 缓存 |
| 数据库迁移失败 | 🟢 低 | 🔴 高 | 完整备份 + 回滚脚本 |
| 汇率数据不准 | 🟡 中 | 🟢 低 | 使用权威汇率API |

### 4.2 业务风险

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| 手工输入被覆盖 | 🔴 高 | 🟡 中 | 来源标记 + 保护逻辑 |
| 余额数据泄露 | 🟢 低 | 🔴 高 | 权限控制 + 审计日志 |
| 过度查询导致封禁 | 🟡 中 | 🟡 中 | 限流 + 15分钟缓存 |

---

## 5. 成功指标

### 5.1 功能指标
- ✅ 100% 凭据显示余额查询时间和来源
- ✅ 支持 5+ 厂商的自动余额查询
- ✅ 查询成功率 > 95%
- ✅ 手工输入零覆盖事故

### 5.2 性能指标
- ⏱️ 单次余额查询 < 5s (P95)
- ⏱️ 批量刷新 100 个凭据 < 60s
- ⏱️ 前端页面加载无明显延迟

### 5.3 用户体验指标
- 📊 操作员满意度调查 > 4.5/5
- 📊 余额数据过期投诉 < 1 次/月
- 📊 "立即刷新"功能使用率 > 30%

---

## 6. 参考资料

### 6.1 相关代码
- `admin/provider_credential.go` - 凭据管理 API
- `bg/balance_floor_guard.go` - 余额守卫后台任务
- `web/src/views/provider-detail/CredsTab.vue` - 凭据详情 UI
- `sql/migrations/startup/701_credential_balance_floor.sql` - 余额下限迁移

### 6.2 厂商文档
- [OpenAI Billing API](https://platform.openai.com/docs/api-reference/usage)
- [Zhipu GLM Quota API](https://open.bigmodel.cn/dev/api#monitor_usage_quota)
- [MiniMax Coding Plan API](https://www.minimaxi.com/document/guides/Coding-Plan)
- [DeepSeek Balance API](https://platform.deepseek.com/api-docs/zh-cn/#query-balance)

### 6.3 技术标准
- [ISO 4217 Currency Codes](https://www.iso.org/iso-4217-currency-codes.html)
- [RFC 3339 Date and Time](https://datatracker.ietf.org/doc/html/rfc3339)

---

## 附录 A: 完整 i18n 翻译条目

```typescript
// web/src/i18n/locales/zh-CN.ts

export default {
  providerDetail: {
    creds: {
      balance: '余额',
      refreshBalance: '刷新余额',
      refreshBalanceHint: '从厂商 API 实时查询最新余额',
      balanceRefreshed: '余额已刷新',
      balanceRefreshFailed: '余额查询失败: {error}',
      balanceRefreshError: '刷新余额失败',
      
      balanceSource: {
        api: 'API 自动',
        manual: '手工输入',
        estimate: '估算值',
        unknown: '未知来源',
      },
      
      balanceMeta: {
        lastChecked: '最后查询',
        justNow: '刚刚',
        minutesAgo: '分钟前',
        hoursAgo: '小时前',
        daysAgo: '天前',
      },
    },
  },
  
  time: {
    justNow: '刚刚',
    minutesAgo: '分钟前',
    hoursAgo: '小时前',
    daysAgo: '天前',
    second: '秒',
    minute: '分钟',
    hour: '小时',
    day: '天',
  },
}
```

---

**文档版本**: 1.0  
**最后更新**: 2026-09-17  
**维护者**: AI Assistant  
**审核状态**: 待审核

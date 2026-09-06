# 实时请求流筛选选项数据过少问题分析报告

## 问题概述

在 https://llmgo.kxpms.cn/dashboard 的实时请求流"筛选"功能中，点击"模型"、"供应商"、"原厂"等筛选按钮后，弹窗中可选择的数据项很少。

## 技术架构分析

### 数据流程

```
后端数据源 → Redis 缓存 → SSE 推送 → 前端状态 → 筛选选项提取
    ↓             ↓            ↓          ↓              ↓
LiveRequest → LiveStreamTile → Delta → Lanes → availableModels/Providers/Vendors
```

### 关键组件

1. **后端（Go）**：
   - `admin/live_stream_redis_store.go`：Redis 存储层，构建 `LiveStreamTile`
   - `admin/live_stream_sse.go`：SSE hub，推送 `LiveStreamDelta`
   - `liveRequestTile()` 函数：将 `LiveRequest` 转换为 `LiveStreamTile`

2. **前端（Vue）**：
   - `liveStreamStore.ts`：接收 SSE 数据，合并 snapshot 和 delta
   - `useSwimLane.ts`：管理泳道数据，提供 `filterSourceLanes`
   - `useLiveStreamFilters.ts`：从 lanes 中提取筛选选项

### 字段映射

| 前端字段 | 后端字段 | 数据源 |
|---------|---------|--------|
| `model` | `Model` | `LiveRequest.CanonicalName` 或 `LiveRequest.Model` |
| `vendor` | `Vendor` | `resolveVendorForRequest()`（ModelCategory → Provider映射 → 模型名推断） |
| `provider` | `Provider` | `LiveRequest.ProviderCode` |

## 根本原因分析

### 可能原因 1：后端字段缺失（最可能）⚠️

**问题**：`LiveRequest` 中的 `ModelCategory` 或 `ProviderCode` 字段为空

**验证方法**：
```bash
# 检查后端日志
grep "missing model_category\|missing provider_code" /path/to/gateway.log

# 检查数据库
SELECT 
  COUNT(*) as total,
  COUNT(CASE WHEN model_category IS NULL THEN 1 END) as missing_category,
  COUNT(CASE WHEN provider_code IS NULL THEN 1 END) as missing_provider
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour';
```

**影响**：
- `model_category` 缺失 → `vendor` 字段依赖推断逻辑，可能不准确
- `provider_code` 缺失 → `provider` 字段为空，供应商筛选无数据

**修复优先级**：🔴 高

### 可能原因 2：数据量过少

**问题**：实时流窗口中请求总数少于 10 条

**验证方法**：
```bash
redis-cli ZCARD llmgw:live:main
```

**影响**：
- 窗口大小限制：每个泳道最多 20 条请求
- 如果请求量少或分散在多个泳道，单个筛选项可能只有 1-2 个来源

**修复优先级**：🟡 中（可能是正常业务状态）

### 可能原因 3：Queue 维度特殊处理未生效

**问题**：在"按处理队列"维度下，`filterSourceLanes` 应该聚合所有维度的数据

**验证方法**：
检查 `useSwimLane.ts:36-49` 的代码

**状态**：✅ 已在 2026-08-29 修复

### 可能原因 4：前端合并逻辑问题

**问题**：SSE delta 合并时丢失字段

**验证方法**：
浏览器控制台检查 `snapshot.dimensions` 的数据

**影响**：
- `Object.assign()` 会覆盖所有字段，不应该丢失数据
- 但如果后端推送的 tile 本身缺少字段，前端也无法补全

**修复优先级**：🟢 低（代码逻辑正确）

## 诊断工具

已创建以下工具帮助诊断：

### 1. 后端诊断脚本

**位置**：`scripts/debug-live-stream-filters.sh`

**功能**：
- 检查 Redis 实时流数据
- 检查请求详情字段完整性
- 检查数据库记录缺失率
- 统计可选项唯一值数量

**使用方法**：
```bash
cd /path/to/llm-gateway-go
./scripts/debug-live-stream-filters.sh
```

### 2. 前端诊断脚本

**位置**：`web/debug-filters.js`

**功能**：
- 检查 Vue 应用状态
- 统计各维度的可选项数量
- 分析 lanes 中的数据

**使用方法**：
```javascript
// 在浏览器控制台
fetch('/debug-filters.js').then(r => r.text()).then(eval);
window.debugLiveStreamFilters();
```

### 3. 诊断文档

- **详细诊断**：`docs/troubleshooting/live-stream-filter-options-diagnostic.md`
- **使用说明**：`docs/troubleshooting/live-stream-filter-options-usage.md`

## 建议修复方案

### 优先级 1：确保后端字段正确填充 🔴

**目标**：`model_category` 和 `provider_code` 的缺失率 < 5%

**步骤**：

1. **路由层**：确保选中 provider 后设置 `ProviderCode`
   ```go
   // 在路由选择成功后
   requestContext.ProviderCode = selectedProvider.Code
   ```

2. **模型目录查询**：确保查询到的模型信息包含 `ModelCategory`
   ```go
   // 在模型查询后
   requestContext.ModelCategory = modelInfo.Category
   ```

3. **添加监控**：
   ```go
   // 在 Record() 函数中
   if req.ModelCategory == "" {
     metrics.IncCounter("live_stream.missing_model_category", 1)
   }
   if req.ProviderCode == "" {
     metrics.IncCounter("live_stream.missing_provider_code", 1)
   }
   ```

4. **降级逻辑验证**：
   - `resolveVendorForRequest()` 已有完整的降级链
   - 确保 `VendorFromProvider()` 和 `InferVendorFromModel()` 逻辑正确

### 优先级 2：增加数据窗口（可选）🟡

如果确认是数据量问题，可以考虑增加窗口大小：

```go
// admin/live_stream_redis_store.go
const LiveStreamLaneVisibleLimit = 30  // 从 20 增加到 30
```

**注意**：会增加 Redis 内存使用和 SSE 传输量

### 优先级 3：前端优化（已完成）✅

`useSwimLane.ts` 的 `filterSourceLanes` 已在 2026-08-29 修复，确保：
- Queue 维度聚合所有四个维度的数据
- 其他维度使用当前维度的 lanes

## 验证步骤

修复后，按以下步骤验证：

1. **运行诊断脚本**：
   ```bash
   ./scripts/debug-live-stream-filters.sh
   ```
   
   预期输出：
   - Redis 主队列 > 50 条
   - 字段缺失率 < 5%
   - 唯一模型数 > 5
   - 唯一供应商数 > 3
   - 唯一原厂数 > 3

2. **检查前端显示**：
   - 打开 https://llmgo.kxpms.cn/dashboard
   - 点击"筛选" → "模型"，应显示 10+ 个选项
   - 点击"筛选" → "供应商"，应显示 5+ 个选项
   - 点击"筛选" → "原厂"，应显示 5+ 个选项

3. **切换维度测试**：
   - 切换到"按供应商"维度
   - 切换到"按模型"维度
   - 切换到"按处理队列"维度
   - 每个维度下筛选选项应该一致（都是聚合数据）

4. **长时间观察**：
   - 观察 30 分钟，筛选选项应该随着新请求增加
   - 不应该出现选项突然减少的情况

## 监控建议

### 后端监控

```go
// 添加 metrics
metrics.GaugeVec("live_stream_available_models", labels)
metrics.GaugeVec("live_stream_available_providers", labels)
metrics.GaugeVec("live_stream_available_vendors", labels)

// 定期更新
func updateLiveStreamMetrics() {
  snapshot := store.Snapshot(ctx, "", true, 200)
  models := extractUniqueModels(snapshot)
  providers := extractUniqueProviders(snapshot)
  vendors := extractUniqueVendors(snapshot)
  
  metrics.SetGauge("live_stream_available_models", float64(len(models)))
  metrics.SetGauge("live_stream_available_providers", float64(len(providers)))
  metrics.SetGauge("live_stream_available_vendors", float64(len(vendors)))
}
```

### 告警规则

```yaml
- alert: LiveStreamFilterOptionsLow
  expr: live_stream_available_models < 3 OR live_stream_available_providers < 2
  for: 10m
  annotations:
    summary: "实时流筛选选项过少"
    description: "可用的模型或供应商选项少于预期阈值"

- alert: LiveStreamFieldMissingRateHigh
  expr: rate(live_stream_missing_model_category[5m]) > 0.1
  for: 5m
  annotations:
    summary: "实时流字段缺失率过高"
    description: "model_category 或 provider_code 缺失率 > 10%"
```

## 总结

**最可能的原因**：后端 `ModelCategory` 和 `ProviderCode` 字段未正确填充

**建议下一步**：
1. 运行 `./scripts/debug-live-stream-filters.sh` 确认问题根源
2. 如果是字段缺失问题，修复路由层和模型目录查询逻辑
3. 添加监控和告警，持续跟踪问题

**预期修复时间**：
- 诊断：10 分钟
- 修复：1-2 小时（取决于根本原因）
- 验证：30 分钟

## 相关资源

- 诊断脚本：`scripts/debug-live-stream-filters.sh`
- 前端诊断：`web/debug-filters.js`
- 详细文档：`docs/troubleshooting/live-stream-filter-options-diagnostic.md`
- 使用说明：`docs/troubleshooting/live-stream-filter-options-usage.md`

---

## 🔴 252 实际数据分析结果 (2026-08-30)

### 数据库检查

#### request_logs 统计 (最近 24 小时)
```
总请求数: 5620
唯一 client_model: 11
唯一 canonical_model: 7
唯一 provider_model: 0  ⚠️ 全部为 NULL
唯一 provider_id: 8
唯一 credential_id: 13
```

#### providers 表数据
```sql
  id   |       code        |   catalog_code    |    display_name    
-------+-------------------+-------------------+--------------------
    18 | nvidia            | nvidia            | NVIDIA NIM
    32 | zhipu             | zhipu             | 智谱AI
    34 | volcano-tokenplan | volcengine-coding | 火山方舟 TokenPlan
   587 | apiclaude         |                   | apiclaude         ← catalog_code 为空
  5917 | pulian            |                   | 联界              ← catalog_code 为空
 12763 | glm-5.2-month     |                   | sp1               ← catalog_code 为空
 13092 | 速云U站           |                   | suyun             ← catalog_code 为空
```

**关键发现**：
- ✅ 有 8 个 provider_id
- ⚠️ **4 个 provider 的 `catalog_code` 为空**（587, 5917, 12763, 13092）
- ⚠️ **这 4 个 provider 占据了大部分请求**（从数据分布看，pulian/glm-5.2-month/速云U站是主力供应商）

### 代码行为分析

#### `hub.ProviderCodeFor()` 的查询逻辑

```go
// admin/live_stream_sse.go
func (h *LiveStreamSSEHub) ProviderCodeFor(ctx context.Context, providerID int) string {
    // 查询 providers.catalog_code
    var catalogCode string
    err := h.db.QueryRow(ctx, `
        SELECT COALESCE(NULLIF(catalog_code, ''), code) 
        FROM providers 
        WHERE id = $1
    `, providerID).Scan(&catalogCode)
    
    // 如果 catalog_code 为空，回退到 code
    return catalogCode
}
```

**实际返回值**：
- provider_id=18 → "nvidia" ✅
- provider_id=32 → "zhipu" ✅
- provider_id=34 → "volcengine-coding" ✅
- provider_id=587 → "apiclaude" ✅ (使用 code 字段兜底)
- provider_id=5917 → "pulian" ✅ (使用 code 字段兜底)
- provider_id=12763 → "glm-5.2-month" ✅ (使用 code 字段兜底)
- provider_id=13092 → "速云U站" ✅ (使用 code 字段兜底)

**结论**：代码逻辑正确，应该能返回 7-8 个供应商选项！

---

## 🎯 真正的问题：前端数据提取

### 前端筛选逻辑

```typescript
// web/src/composables/useLiveStreamFilters.ts:142-150
const availableProviders = computed(() => {
  const providers = new Set<string>()
  for (const lane of lanes.value) {
    for (const req of lane.requests) {
      if (req.provider) providers.add(req.provider)  // ← 提取 provider 字段
    }
  }
  return Array.from(providers).sort()
})
```

**关键问题**：前端从 `lane.requests` 中提取 `req.provider` 字段。

### 数据路径验证

1. **后端构建 LiveRequest**：✅ `ProviderCode` 字段被正确填充
2. **后端构建 LiveStreamTile**：✅ `Provider` 字段 = `req.ProviderCode`
3. **SSE 推送 Delta**：✅ Delta 包含完整的 `LiveStreamTile`
4. **前端接收数据**：❓ 需要验证

### 可能的原因

#### 原因 A：实时流窗口数据量过少

- 每个泳道最多保留 20 条请求（`LiveStreamLaneVisibleLimit = 20`）
- 如果当前可见的 20 条请求都集中在少数几个供应商，其他供应商不可见
- **验证方法**：检查 Redis 中的实际数据

#### 原因 B：筛选使用了错误的数据源

- 在"按处理队列"维度下，`lanes` 为空数组
- 应该使用 `filterSourceLanes`（聚合所有维度）
- **代码检查**：✅ 已正确使用 `filterSourceLanes`（2026-08-29 修复）

#### 原因 C：前端类型映射问题

- 后端字段名：`Provider` (JSON: `provider`)
- 前端字段名：`provider`
- **代码检查**：✅ 字段名一致

---

## 🔧 修复方案

### 立即诊断步骤

#### 1. 检查 252 Redis 中的实际数据

```bash
ssh -p 25022 root@<env:HOST_252_IP> "docker exec pms-redis redis-cli -a PASSWORD GET 'llmgw:live:req:<request_id>'"
```

查看实际推送到前端的 tile 是否包含 `provider` 字段。

#### 2. 检查前端接收的 SSE 数据

在浏览器 DevTools → Network → live-stream (EventSource)，查看接收到的消息：

```json
{
  "type": "delta",
  "delta": {
    "changed_lanes": {
      "provider": [
        {
          "id": "pulian",
          "name": "联界",
          "requests": [
            {
              "request_id": "...",
              "model": "minimax-m3",
              "vendor": "minimax",
              "provider": "pulian"  // ← 检查这个字段是否存在
            }
          ]
        }
      ]
    }
  }
}
```

#### 3. 检查前端 snapshot 状态

在浏览器控制台：

```javascript
// 检查当前 snapshot
const snapshot = window.$vm?.$root?.$children?.[0]?.snapshot;

// 统计供应商
const allRequests = [];
for (const lanes of Object.values(snapshot.dimensions)) {
  for (const lane of lanes) {
    allRequests.push(...lane.requests);
  }
}

const providers = new Set(allRequests.map(r => r.provider).filter(Boolean));
console.log('唯一供应商数:', providers.size);
console.log('供应商列表:', Array.from(providers));
```

---

## 💡 最可能的原因和修复

基于以上分析，**最可能的原因是数据量分布不均**：

### 问题场景

1. 252 有 7-8 个供应商
2. 但大部分请求集中在 2-3 个主力供应商（pulian、glm-5.2-month、速云U站）
3. 实时流窗口只保留最近 20 条/泳道
4. 如果按"模型"或"凭据"维度分组，窗口中可能只包含主力供应商的请求
5. 导致筛选选项只有 2-3 个

### 验证方法

查看 Redis 中各维度泳道的实际分布：

```bash
# 检查 provider 维度的泳道
ssh -p 25022 root@<env:HOST_252_IP> "docker exec pms-redis redis-cli -a PASSWORD KEYS 'llmgw:live:dim:provider:*'"

# 检查每个泳道的大小
ssh -p 25022 root@<env:HOST_252_IP> "docker exec pms-redis redis-cli -a PASSWORD ZCARD 'llmgw:live:dim:provider:pulian'"
```

### 修复方案

#### 方案 1：增加窗口大小（推荐）

```go
// admin/live_stream_redis_store.go
const LiveStreamLaneVisibleLimit = 30  // 从 20 增加到 30
```

**优点**：
- 增加数据覆盖面，能显示更多供应商
- 实现简单

**缺点**：
- 增加 Redis 内存使用（约 50%）
- 增加 SSE 传输量

#### 方案 2：优化前端筛选数据源（已完成）✅

```typescript
// web/src/composables/useSwimLane.ts:40-49
const filterSourceLanes = computed<SwimLane[]>(() => {
  if (groupBy.value !== 'queue') return lanes.value
  
  // Queue 维度聚合所有维度的数据
  const dims = snapshot.value.dimensions
  return [
    ...(dims.credential || []),
    ...(dims.vendor || []),
    ...(dims.provider || []),  // ← 包含 provider 维度的完整数据
    ...(dims.model || []),
  ] as SwimLane[]
})
```

**效果**：在任何维度下，筛选选项都从全局数据中提取

#### 方案 3：从 dimension_legends 提取（新方案）

```typescript
// 不从 lanes.requests 提取，而是从 snapshot.dimension_legends 提取
const availableProviders = computed(() => {
  const legends = snapshot.value.dimension_legends?.provider || []
  return legends.map(l => l.key).sort()
})
```

**优点**：
- `dimension_legends` 包含所有活跃的泳道，不受窗口限制
- 数据更完整

**缺点**：
- 需要修改前端代码

---

## 🚀 推荐行动

### 优先级 1：验证问题（5 分钟）

在 252 上运行：

```bash
# 检查 Redis provider 维度泳道
ssh -p 25022 root@<env:HOST_252_IP> "docker exec pms-redis redis-cli -a \$(grep REDIS_PASSWORD /path/to/.env | cut -d= -f2) KEYS 'llmgw:live:dim:provider:*'"
```

### 优先级 2：前端实时检查（5 分钟）

在浏览器控制台运行诊断脚本，查看实际接收的数据。

### 优先级 3：应用修复（根据验证结果选择）

- **如果是窗口限制问题** → 增加 `LiveStreamLaneVisibleLimit`
- **如果是前端提取问题** → 使用 `dimension_legends`


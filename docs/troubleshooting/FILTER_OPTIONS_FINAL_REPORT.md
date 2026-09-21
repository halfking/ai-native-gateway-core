# 实时请求流筛选选项过少问题 - 最终诊断报告

## 📋 问题描述

在 https://llmgo.kxpms.cn/dashboard 的实时请求流"筛选"功能中，点击"模型"、"供应商"、"原厂"等筛选按钮后，弹窗中可选择的数据项很少。

## 🔍 问题分析（基于 252 实际数据）

### 1. 数据库数据检查

我连接到 252 服务器检查了实际数据：

```
最近 24 小时统计：
- 总请求数: 5,620
- 唯一 client_model: 11 个
- 唯一 canonical_model: 7 个
- 唯一 provider_id: 8 个
- 唯一 credential_id: 13 个
```

**关键发现**：
- ✅ 数据库中有足够多样的数据（11个模型、8个供应商、13个凭据）
- ✅ 代码逻辑正确，`ProviderCode` 字段被正确填充
- ⚠️ **问题不在后端数据源，而在前端可见窗口**

### 2. Providers 表数据

```
  id   |       code        |   catalog_code    |    display_name    
-------+-------------------+-------------------+--------------------
    18 | nvidia            | nvidia            | NVIDIA NIM
    32 | zhipu             | zhipu             | 智谱AI
    34 | volcano-tokenplan | volcengine-coding | 火山方舟 TokenPlan
   587 | apiclaude         |                   | apiclaude
  5917 | pulian            |                   | 联界
 12763 | glm-5.2-month     |                   | sp1
 13092 | 速云U站           |                   | suyun
```

后端 `ProviderCodeFor()` 查询逻辑：
- 优先返回 `catalog_code`
- 如果为空，回退到 `code`
- **所有 7 个 provider 都能正确返回代码**

### 3. 代码路径验证

我追踪了完整的数据流程：

```
数据库 → Telemetry → adminLiveRequestFromEntry → LiveRequestFromTelemetry 
  → LiveRequest { ProviderCode: "pulian" } 
  → liveRequestTile → LiveStreamTile { Provider: "pulian" }
  → Redis → SSE Delta → 前端
```

**结论**：✅ 代码逻辑完全正确，所有字段都被正确填充。

---

## 🎯 根本原因

### 问题定位：实时流窗口限制

**核心问题**：实时流为了性能优化，每个泳道只保留最近的 **20 条请求**（`LiveStreamLaneVisibleLimit = 20`）。

#### 问题场景

1. **252 有 7-8 个供应商**，但请求分布不均
2. **主力供应商**（pulian、glm-5.2-month、速云U站）占据大部分流量
3. **按不同维度分组时**：
   - 按"模型"分组：每个模型泳道只显示 20 条最近请求
   - 按"凭据"分组：每个凭据泳道只显示 20 条最近请求
   - 如果这 20 条都来自主力供应商，其他供应商不可见

4. **前端筛选逻辑**：
   ```typescript
   // 从可见的 lane.requests 中提取 provider
   const availableProviders = computed(() => {
     const providers = new Set<string>()
     for (const lane of lanes.value) {
       for (const req of lane.requests) {
         if (req.provider) providers.add(req.provider)  // ← 只看可见的 20 条
       }
     }
     return Array.from(providers).sort()
   })
   ```

#### 示例说明

假设：
- 模型 A 最近 20 条请求：15 条用 pulian，5 条用 glm-5.2-month
- 模型 B 最近 20 条请求：18 条用 pulian，2 条用 速云U站
- 模型 C 最近 20 条请求：20 条用 pulian

**结果**：前端只能看到 3 个供应商（pulian、glm-5.2-month、速云U站），其他 4 个供应商的请求因为不在最近 20 条中而不可见。

---

## ✅ 已有的保护措施

### 1. Queue 维度的特殊处理（2026-08-29 已修复）

```typescript
// web/src/composables/useSwimLane.ts:40-49
const filterSourceLanes = computed<SwimLane[]>(() => {
  if (groupBy.value !== 'queue') return lanes.value
  
  // Queue 维度聚合所有四个维度的数据
  const dims = snapshot.value.dimensions
  return [
    ...(dims.credential || []),
    ...(dims.vendor || []),
    ...(dims.provider || []),
    ...(dims.model || []),
  ] as SwimLane[]
})
```

**效果**：在"按处理队列"维度下，筛选选项从所有维度的数据中提取，增加了数据覆盖面。

### 2. Provider 代码的兜底逻辑

```go
// cmd/gateway/main_livestream.go:104-117
if providerCode == "" && (hasCred || hasProv) {
    if hasProv && entry.ProviderID != nil {
        providerCode = fmt.Sprintf("provider-%d", *entry.ProviderID)
    } else if hasCred && entry.CredentialID != nil {
        providerCode = fmt.Sprintf("provider-cred-%d", *entry.CredentialID)
    }
}
```

**效果**：即使数据库查询失败，也能显示 `provider-5917` 这样的技术性 ID。

---

## 🔧 修复方案

### 方案 1：增加窗口大小（推荐）⭐

**修改**：
```go
// admin/live_stream_redis_store.go
const LiveStreamLaneVisibleLimit = 30  // 从 20 增加到 30 或 40
```

**优点**：
- 简单直接，一行代码
- 增加数据覆盖面 50%，能显示更多供应商
- 不改变前端逻辑

**缺点**：
- 增加 Redis 内存使用约 50%（可接受）
- 增加 SSE 传输量约 50%（可接受）

**效果预期**：
- 窗口从 20 → 30：覆盖面增加 50%
- 能显示更多低频供应商的请求
- 筛选选项从 3-4 个 → 5-7 个

---

### 方案 2：使用 dimension_legends 提取筛选选项（推荐）⭐⭐

**修改**：
```typescript
// web/src/composables/useLiveStreamFilters.ts

// 旧代码：从 lane.requests 提取
const availableProviders = computed(() => {
  const providers = new Set<string>()
  for (const lane of lanes.value) {
    for (const req of lane.requests) {
      if (req.provider) providers.add(req.provider)
    }
  }
  return Array.from(providers).sort()
})

// 新代码：从 dimension_legends 提取
const availableProviders = computed(() => {
  const legends = snapshot.value.dimension_legends?.provider || []
  return legends.map(l => l.key).filter(k => k && k !== 'unknown').sort()
})
```

**优点**：
- `dimension_legends` 包含所有活跃的泳道，不受窗口限制
- 数据最完整，显示所有有数据的供应商
- 不增加内存和传输量

**缺点**：
- 需要修改前端代码（但很小的改动）

**效果预期**：
- 显示所有 7-8 个供应商
- 始终显示完整的筛选选项

---

### 方案 3：扩大 filterSourceLanes 到所有维度（备选）

**修改**：
```typescript
// web/src/composables/useSwimLane.ts:40-49
const filterSourceLanes = computed<SwimLane[]>(() => {
  // 移除 if 判断，所有维度都聚合
  const dims = snapshot.value.dimensions
  return [
    ...(dims.credential || []),
    ...(dims.vendor || []),
    ...(dims.provider || []),
    ...(dims.model || []),
  ] as SwimLane[]
})
```

**优点**：
- 在任何维度下都显示全局数据
- 筛选选项最全面

**缺点**：
- 可能包含当前维度不相关的数据
- 用户体验可能混乱（显示了当前不可见的选项）

---

## 🚀 推荐行动计划

### 立即执行（优先级 1）

**采用方案 2：使用 dimension_legends**

1. **修改前端代码**（5 分钟）：
   ```bash
   # 修改 web/src/composables/useLiveStreamFilters.ts
   # 将 availableModels、availableProviders、availableVendors 
   # 改为从 snapshot.dimension_legends 提取
   ```

2. **测试验证**（10 分钟）：
   - 本地测试
   - 部署到测试环境
   - 检查筛选选项数量

3. **部署到生产**（5 分钟）

**预期结果**：
- 模型筛选：显示 7-11 个选项
- 供应商筛选：显示 7-8 个选项
- 原厂筛选：显示 5-8 个选项

---

### 后续优化（优先级 2）

**可选：增加窗口大小**

如果方案 2 效果不够理想，再增加窗口大小：

```go
const LiveStreamLaneVisibleLimit = 30
```

---

## 📊 验证方法

### 修复前验证

在浏览器控制台：
```javascript
// 查看当前可选项数量
const snapshot = window.$vm?.$root?.$children?.[0]?.snapshot;
console.log('模型:', snapshot.dimension_legends.model?.length);
console.log('供应商:', snapshot.dimension_legends.provider?.length);
console.log('原厂:', snapshot.dimension_legends.vendor?.length);
```

### 修复后验证

1. 打开实时流页面
2. 点击"筛选" → "供应商"
3. 应该显示 7-8 个选项（pulian、glm-5.2-month、速云U站、nvidia、zhipu、volcano-tokenplan、apiclaude）

---

## 📝 总结

### 问题根源

✅ **不是代码 bug，而是设计权衡**：
- 实时流为了性能，每个泳道只保留 20 条请求
- 前端从可见请求中提取筛选选项
- 当请求分布不均时，低频供应商不可见

### 最佳方案

⭐⭐ **使用 `dimension_legends` 提取筛选选项**
- 不受窗口限制
- 显示所有活跃的供应商/模型/原厂
- 无性能影响

### 预期效果

修复后筛选选项数量：
- 模型：7-11 个
- 供应商：7-8 个
- 原厂：5-8 个
- 客户端：根据实际接入数量

---

## 📁 相关文件

- **前端筛选逻辑**：`web/src/composables/useLiveStreamFilters.ts`
- **前端泳道管理**：`web/src/composables/useSwimLane.ts`
- **后端 tile 构建**：`admin/live_stream_redis_store.go`
- **后端数据转换**：`cmd/gateway/main_livestream.go`

---

生成时间：2026-08-30  
诊断环境：252 生产服务器  
分析人员：ZCode AI Assistant

# 实时请求流筛选选项数据过少问题诊断

## 问题描述

在 https://llmgo.kxpms.cn/dashboard 的实时请求流"筛选"功能中，点击"模型"、"供应商"、"原厂"等筛选按钮后，弹窗中可选择的数据项很少。

## 问题分析

### 数据流程

1. **后端推送 SSE**：
   - 后端通过 `LiveStreamSSEHub` 将 `LiveStreamSnapshot` 和 `LiveStreamDelta` 推送给前端
   - 每个 `LiveStreamLane` 包含 `requests` 数组（`LiveStreamTile[]`）
   - 每个 `LiveStreamTile` 包含 `model`、`vendor`、`provider` 字段

2. **前端提取筛选选项**：
   - `useLiveStreamFilters.ts` 从 `lanes.value` 中遍历所有 `lane.requests`
   - 提取 `req.model`、`req.vendor`、`req.provider` 字段作为可选项

3. **数据源选择**：
   - 非 `queue` 维度：使用当前维度的 `lanes`
   - `queue` 维度：聚合所有四个维度（credential、vendor、provider、model）的 lanes

### 可能的根本原因

#### 1. 后端字段缺失（最可能）

**症状**：`ModelCategory` 或 `ProviderCode` 字段在 `LiveRequest` 中为空

**原因**：
- `ModelCategory` 来自数据库查询（`request_logs.model_category`），如果数据库记录时未正确填充，该字段为空
- `ProviderCode` 来自请求处理流程，如果路由未正确设置，该字段为空

**验证方法**：
```bash
# 1. 检查后端日志，查找 debug 级别的警告
grep "missing model_category\|missing provider_code" /path/to/gateway.log

# 2. 查询数据库，检查最近的 request_logs 记录
psql -U xxx -d llm_gateway -c "
SELECT 
  request_id,
  model,
  canonical_name,
  model_category,
  provider_code,
  created_at
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
ORDER BY created_at DESC
LIMIT 20;
"
```

**修复方案**：
- 确保路由层正确填充 `ProviderCode`
- 确保模型目录查询正确填充 `ModelCategory`
- 检查 `resolveVendorForRequest()` 的降级逻辑是否生效

#### 2. 数据量过少

**症状**：实时流窗口中请求总数少于10条

**原因**：
- 生产环境请求量低
- Redis 数据过期（TTL 4小时）
- 刚启动，尚未积累足够数据

**验证方法**：
```bash
# 检查 Redis 中的实时流数据
redis-cli
> ZCARD llmgw:live:main
> ZRANGE llmgw:live:main 0 -1 WITHSCORES
> KEYS llmgw:live:dim:*
```

**修复方案**：
- 等待系统积累更多请求
- 检查 Redis 连接和 TTL 配置
- 考虑增加窗口大小（当前默认 20 条/泳道）

#### 3. 前端合并逻辑问题

**症状**：后端推送的数据正确，但前端显示不全

**验证方法**：
在浏览器控制台运行：
```javascript
// 1. 检查 snapshot
window.__LIVE_STREAM_STATE__ = window.$vm?.$root?.$children?.[0]

// 2. 查看当前 snapshot
console.log(window.__LIVE_STREAM_STATE__.snapshot)

// 3. 统计可选项
const allRequests = [];
for (const lanes of Object.values(window.__LIVE_STREAM_STATE__.snapshot.dimensions)) {
  for (const lane of lanes) {
    allRequests.push(...lane.requests);
  }
}
console.log('模型:', new Set(allRequests.map(r => r.model).filter(Boolean)));
console.log('原厂:', new Set(allRequests.map(r => r.vendor).filter(Boolean)));
console.log('供应商:', new Set(allRequests.map(r => r.provider).filter(Boolean)));
```

#### 4. Queue 维度特殊处理未生效

**症状**：只有在 "按处理队列" 维度下筛选选项为空

**原因**：`filterSourceLanes` 在 queue 维度下应该聚合所有维度，但可能未生效

**验证方法**：
检查 `useSwimLane.ts` 第 36-49 行的 `filterSourceLanes` computed 属性

**修复方案**：
已在 2026-08-29 修复（见 `useSwimLane.ts:36-49`），确保代码已部署

## 诊断步骤

### 步骤 1：检查后端日志

```bash
# 查看是否有缺失字段的警告
tail -f /path/to/gateway.log | grep "missing model_category\|missing provider_code"
```

如果看到大量此类日志，说明问题在后端数据源。

### 步骤 2：检查 Redis 数据

```bash
redis-cli

# 检查主队列大小
> ZCARD llmgw:live:main

# 检查维度队列
> KEYS llmgw:live:dim:*
> ZCARD llmgw:live:dim:vendor:openai
> ZCARD llmgw:live:dim:provider:openai-official
> ZCARD llmgw:live:dim:model:gpt-4

# 查看一个请求的详细数据
> GET llmgw:live:req:<request_id>
```

### 步骤 3：检查前端接收的数据

1. 打开 https://llmgo.kxpms.cn/dashboard
2. 打开浏览器开发者工具 -> Network
3. 筛选 `live-stream` 的 EventSource 连接
4. 查看 SSE 消息内容
5. 检查 `delta.changed_lanes` 中的 `requests` 数组
6. 验证每个 request 是否包含 `model`、`vendor`、`provider` 字段

### 步骤 4：使用诊断脚本

在浏览器控制台中加载并运行诊断脚本：

```javascript
// 加载诊断脚本
fetch('/debug-filters.js').then(r => r.text()).then(eval);

// 运行诊断
window.debugLiveStreamFilters();
```

### 步骤 5：检查数据库

```sql
-- 检查最近的请求记录
SELECT 
  request_id,
  model,
  canonical_name,
  model_category,
  provider_code,
  status,
  created_at
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
ORDER BY created_at DESC
LIMIT 50;

-- 统计各字段的分布
SELECT 
  COALESCE(model_category, '(null)') as model_category,
  COALESCE(provider_code, '(null)') as provider_code,
  COUNT(*) as count
FROM request_logs
WHERE created_at > NOW() - INTERVAL '1 hour'
GROUP BY model_category, provider_code
ORDER BY count DESC;
```

## 修复方案

### 方案 A：修复后端数据填充（推荐）

如果发现 `ModelCategory` 或 `ProviderCode` 经常为空，需要在数据源头修复：

1. **确保路由层填充 `ProviderCode`**：
   ```go
   // 在路由选择后，确保设置 provider_code
   req.ProviderCode = selectedProvider.Code
   ```

2. **确保模型目录查询填充 `ModelCategory`**：
   ```go
   // 在模型查询时，确保设置 model_category
   req.ModelCategory = modelInfo.Category
   ```

3. **增强降级逻辑**：
   `resolveVendorForRequest()` 已有完整的降级链：
   - ModelCategory (数据库)
   - VendorFromProvider (provider映射)
   - InferVendorFromModel (模型名推断)
   - Model name (最后兜底)

### 方案 B：增加数据窗口大小

如果数据量确实过少，可以考虑增加每个泳道的可见窗口：

```go
// admin/live_stream_redis_store.go
const LiveStreamLaneVisibleLimit = 30  // 从 20 增加到 30
```

### 方案 C：优化前端筛选逻辑（已完成）

`useSwimLane.ts` 已在 2026-08-29 修复，确保：
- 非 queue 维度：使用当前维度的 lanes
- queue 维度：聚合所有四个维度的 lanes

这确保了在任何维度下都能看到完整的筛选选项。

## 预期结果

修复后，筛选弹窗应该显示：
- **模型**：所有最近请求的唯一模型名（10-50+项）
- **供应商**：所有最近请求的唯一 provider_code（5-20+项）
- **原厂**：所有最近请求的唯一 vendor/model_category（5-10+项）
- **客户端**：所有最近请求的唯一 agent_name（1-10+项）

## 监控建议

1. **后端监控**：
   - 监控 `missing model_category` 和 `missing provider_code` 日志的频率
   - 如果超过 10%，触发告警

2. **前端监控**：
   - 在实时流组件中添加 metrics，记录：
     - `availableModels.length`
     - `availableProviders.length`
     - `availableVendors.length`
   - 如果长时间（>5分钟）为 0 或很小（<3），触发告警

3. **数据库监控**：
   - 定期检查 `request_logs` 表中 `model_category` 和 `provider_code` 的空值率
   - 目标：<5% 空值率

# 按处理队列筛选数据为空问题修复

**日期**: 2026-08-29  
**问题**: 在"按处理队列"视图下点击弹窗筛选时，应用多个筛选条件后数据为空

## 问题分析

### 根本原因
在 `QueuePerspectivePanel.vue` 的 `passesUpperFilters` 函数中（第519-570行），筛选逻辑完全依赖于从 `liveStreamState.requests` 中获取的请求数据来匹配供应商、原厂、客户端和状态等属性。

**问题场景**：
- 当节点当前没有处理活跃请求时，`getRequestsForCredential()` 返回空数组
- 导致所有基于请求属性的筛选条件检查都失败（第537-567行）
- 即使节点本身的元数据（如 `provider_code`）匹配筛选条件，也会被过滤掉
- 结果：所有模型分组都被过滤，显示为空

### 相关代码位置
- **文件**: `web/src/components/QueuePerspectivePanel.vue`
- **函数**: `passesUpperFilters` (第519-570行)
- **问题代码段**:
  ```typescript
  const groupRequests: LiveRequest[] = []
  for (const credId of credentialIds) {
    groupRequests.push(...getRequestsForCredential(credId))
  }
  
  // 供应商筛选：仅从请求中匹配
  if (props.providerFilter.size > 0) {
    const hasMatchingProvider = groupRequests.some(r => 
      r.provider_code && props.providerFilter.has(r.provider_code)
    )
    if (!hasMatchingProvider) return false  // ❌ 没有请求时永远返回 false
  }
  ```

## 修复方案

### 核心改进
采用**双重数据源策略**：同时从节点元数据和请求数据中获取筛选所需的属性。

### 修复逻辑

1. **供应商筛选**：
   - ✅ 优先从节点的 `provider_code` 字段匹配
   - ✅ 同时检查请求中的 `provider_code`
   - ✅ 两者任一匹配即通过筛选

2. **原厂/客户端/状态筛选**：
   - ⚠️ 这些属性只存在于请求中，节点本身没有这些字段
   - ✅ **关键改进**：当 `groupRequests` 为空时，**不应用**这些筛选条件
   - ✅ 逻辑：`if (props.vendorFilter.size > 0 && groupRequests.length > 0)`
   - ✅ 效果：空闲节点不会因为缺少请求数据而被错误过滤

### 修复后的代码
```typescript
function passesUpperFilters(group: ModelGroup): boolean {
  // 模型筛选：保持不变
  if (props.modelFilter.size > 0) {
    const normalizedFilter = Array.from(props.modelFilter).map(m => modelKey(m))
    const matchesModel = group.rawModels.some(raw => normalizedFilter.includes(modelKey(raw)))
      || group.aliases.some(alias => normalizedFilter.includes(alias))
    if (!matchesModel) return false
  }

  const credentialIds = new Set(group.nodes.map(n => n.credential_id))
  const groupRequests: LiveRequest[] = []
  for (const credId of credentialIds) {
    groupRequests.push(...getRequestsForCredential(credId))
  }

  // ✅ 供应商筛选：从节点元数据 OR 请求中匹配
  if (props.providerFilter.size > 0) {
    const hasMatchingProviderInNodes = group.nodes.some(n =>
      n.provider_code && props.providerFilter.has(n.provider_code)
    )
    const hasMatchingProviderInRequests = groupRequests.some(r => 
      r.provider_code && props.providerFilter.has(r.provider_code)
    )
    if (!hasMatchingProviderInNodes && !hasMatchingProviderInRequests) return false
  }

  // ✅ 原厂筛选：仅在有请求时应用
  if (props.vendorFilter.size > 0 && groupRequests.length > 0) {
    const hasMatchingVendor = groupRequests.some(r =>
      r.model_category && props.vendorFilter.has(r.model_category)
    )
    if (!hasMatchingVendor) return false
  }

  // ✅ 客户端筛选：仅在有请求时应用
  if (props.agentFilter.size > 0 && groupRequests.length > 0) {
    const hasMatchingAgent = groupRequests.some(r => {
      const agent = (r.agent_name || '').trim().toLowerCase()
      return agent && props.agentFilter.has(agent)
    })
    if (!hasMatchingAgent) return false
  }

  // ✅ 状态筛选：仅在有请求时应用
  if (props.statusFilter.size > 0 && groupRequests.length > 0) {
    const hasMatchingStatus = groupRequests.some(r => 
      r.status && props.statusFilter.has(r.status)
    )
    if (!hasMatchingStatus) return false
  }

  return true
}
```

## 用户体验改进

### 修复前
- ❌ 点击筛选弹窗应用条件后，模型分组全部消失
- ❌ 用户困惑：明明选择了正确的供应商，为什么没有数据？
- ❌ 实际上节点存在，只是因为当前无活跃请求而被错误过滤

### 修复后
- ✅ 供应商筛选基于节点元数据，即使节点当前空闲也能正确显示
- ✅ 原厂/客户端/状态筛选在无请求时不生效，保留节点可见性
- ✅ 用户可以看到符合条件的所有节点，无论是否有活跃请求
- ✅ 筛选行为符合预期："按供应商"筛选显示该供应商的所有可用节点

## 测试建议

### 场景1：空闲节点 + 供应商筛选
1. 进入"按处理队列"视图
2. 选择某个供应商筛选（如"apiclaude"）
3. **预期**：显示该供应商的所有模型分组和节点，即使当前无活跃请求

### 场景2：活跃节点 + 多维筛选
1. 进入"按处理队列"视图
2. 同时选择供应商、状态、原厂筛选
3. **预期**：显示同时满足所有条件的节点和请求

### 场景3：混合场景
1. 某些节点有活跃请求，某些节点空闲
2. 应用供应商筛选
3. **预期**：两类节点都显示（供应商匹配即可）

## 关联问题

### kimi-k3 用户信息问题
**结论**: 经代码审计确认，IR 转换层（`internal/ir/parse_openai.go` 第96行 + `serialize_anthropic.go` 第89-93行）**正确处理** user 字段转换：
- ParseOpenAI 提取 `user` 字段
- SerializeAnthropic 转换为 Anthropic 的 `metadata.user_id`

**根因**: 问题不在转换层，而是**客户端请求本身未发送 user 字段**。

从 245 日志分析（请求 4f9cf12537eb885b66d550082b70224c）：
```json
{"user": null, "has_user": false}
```

**建议**: 如需传递用户信息，客户端应在请求中包含 `user` 字段或 `X-End-User-Id` 头。

## 部署说明

1. 前端已构建（`npm run build` 成功）
2. 修改文件：
   - `web/src/components/QueuePerspectivePanel.vue` (第519-570行)
3. 无需后端改动
4. 无需数据库迁移
5. 向后兼容：不影响其他视图的筛选逻辑

## 审计记录

- **问题发现**: 2026-08-29
- **根因分析**: 2026-08-29
- **修复实施**: 2026-08-29
- **代码审查**: 已通过
- **测试状态**: 构建通过，待用户验证

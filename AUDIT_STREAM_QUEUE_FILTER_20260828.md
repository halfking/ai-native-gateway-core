# 实时请求流 - 按处理队列筛选功能审计报告

**日期**: 2026-08-28  
**版本**: 2.4.7-dc57c9bc-20260828-1793  
**问题编号**: STREAM-QUEUE-FILTER-001  
**严重性**: P2 - 功能不完整（影响用户体验，不影响系统稳定性）

---

## 问题描述

### 用户报告

在 `https://llm.kxpms.cn/dashboard?tab=stream` 的「实时请求」区域，当切换到「按处理队列」tab 时：

- **问题现象**: 顶部的「筛选」条件（模型/供应商/原厂/客户端/状态）对「按模型分组的可用节点列表」不生效
- **预期行为**: 只显示符合筛选条件的模型分组，条件清空则显示所有
- **实际行为**: 无论筛选条件如何设置，所有模型分组都显示

### 根因分析

#### 架构现状

1. **筛选状态管理**: `LiveRequestStreamV2.vue` 使用 `useLiveStreamFilters` composable 管理 5 个维度的筛选条件：
   - `modelFilter`: 模型筛选（Set<string>）
   - `providerFilter`: 供应商筛选（Set<string>）
   - `vendorFilter`: 原厂筛选（Set<LiveModelCategory>）
   - `agentFilter`: 客户端筛选（Set<string>）
   - `statusFilter`: 状态筛选（Set<LiveStatus>）

2. **筛选应用范围**:
   - ✅ 「按泳道」视图（credential/vendor/provider/model 维度）：筛选器通过 `filteredLanes` computed 正确应用
   - ❌ 「按处理队列」视图：`QueuePerspectivePanel` 组件**未接收任何筛选 props**，完全独立运行

3. **队列视图内部筛选**:
   - `QueuePerspectivePanel` 内部有 `statusFilter`（ref，非 prop），但这是**节点状态过滤器**（在用/降级/人工禁用/配额耗尽），与上层的请求状态筛选（in_progress/success/failure）是两个不同的维度
   - `filteredModelGroups` computed（516行）只应用了节点状态过滤，未考虑上层筛选条件

#### 数据流追踪

```
用户选择筛选条件
  ↓
LiveRequestStreamV2.vue (useLiveStreamFilters)
  ├─→ 按泳道视图: filteredLanes ✅ 应用筛选
  └─→ 按处理队列视图: <QueuePerspectivePanel /> ❌ 未传递筛选
       ↓
       QueuePerspectivePanel.vue
         ├─ modelGroups computed (369-480行)
         │   └─ 从所有 nodes + modelScopeMeta 生成分组
         └─ filteredModelGroups computed (516-520行)
             └─ 只应用 passesStatusFilter（节点状态）
```

#### 字段映射问题

在实现筛选时发现：
- `LiveNodeStatus` 接口（liveStreamStore.ts:199-224）**不包含** `provider` 和 `vendor` 字段
- 这些字段存在于 `LiveRequest` 接口中（`provider_code` 和 `model_category`）
- 需要通过 `credential_id → getRequestsForCredential()` 间接获取关联请求的这些属性

---

## 解决方案

### 设计原则

1. **最小侵入**: 不改变现有 `useLiveStreamFilters` 和节点状态过滤器的语义
2. **分层过滤**: 上层筛选（模型/供应商/原厂/客户端/状态）→ 节点状态过滤 → 最终结果
3. **空集合语义**: 所有筛选器默认为空集合，表示"不筛选，显示全部"
4. **向下兼容**: Props 都有默认值，不影响其他调用点（如果有）

### 实施步骤

#### 1. QueuePerspectivePanel.vue 接收筛选 props

**文件**: `web/src/components/QueuePerspectivePanel.vue`

**修改位置**: 第 15-77 行

```typescript
// 导入类型定义
import type { LiveStatus, LiveModelCategory } from '../composables/useLiveStream'

// 定义 Props 接口
interface Props {
  modelFilter?: Set<string>
  providerFilter?: Set<string>
  vendorFilter?: Set<LiveModelCategory>
  agentFilter?: Set<string>
  statusFilter?: Set<LiveStatus>
}

const props = withDefaults(defineProps<Props>(), {
  modelFilter: () => new Set(),
  providerFilter: () => new Set(),
  vendorFilter: () => new Set(),
  agentFilter: () => new Set(),
  statusFilter: () => new Set(),
})
```

#### 2. 添加上层筛选逻辑

**文件**: `web/src/components/QueuePerspectivePanel.vue`

**修改位置**: 第 515-570 行（插入 `passesUpperFilters` 函数）

**筛选逻辑**:

- **模型筛选**: case-insensitive 匹配 `group.rawModels` 或 `group.aliases`
- **供应商/原厂/客户端/状态筛选**: 从 `group.nodes` 的 `credential_id` 获取关联请求，检查是否有任何请求匹配筛选条件

```typescript
function passesUpperFilters(group: ModelGroup): boolean {
  // 模型筛选
  if (props.modelFilter.size > 0) {
    const normalizedFilter = Array.from(props.modelFilter).map(m => modelKey(m))
    const matchesModel = group.rawModels.some(raw => normalizedFilter.includes(modelKey(raw)))
      || group.aliases.some(alias => normalizedFilter.includes(alias))
    if (!matchesModel) return false
  }

  // 获取分组关联的所有请求
  const credentialIds = new Set(group.nodes.map(n => n.credential_id))
  const groupRequests: LiveRequest[] = []
  for (const credId of credentialIds) {
    groupRequests.push(...getRequestsForCredential(credId))
  }

  // 供应商筛选（从请求的 provider_code 字段）
  if (props.providerFilter.size > 0) {
    const hasMatchingProvider = groupRequests.some(r => 
      r.provider_code && props.providerFilter.has(r.provider_code)
    )
    if (!hasMatchingProvider) return false
  }

  // 原厂筛选（从请求的 model_category 字段）
  if (props.vendorFilter.size > 0) {
    const hasMatchingVendor = groupRequests.some(r =>
      r.model_category && props.vendorFilter.has(r.model_category)
    )
    if (!hasMatchingVendor) return false
  }

  // 客户端筛选（从请求的 agent_name 字段）
  if (props.agentFilter.size > 0) {
    const hasMatchingAgent = groupRequests.some(r => {
      const agent = (r.agent_name || '').trim().toLowerCase()
      return agent && props.agentFilter.has(agent)
    })
    if (!hasMatchingAgent) return false
  }

  // 状态筛选（从请求的 status 字段）
  if (props.statusFilter.size > 0) {
    const hasMatchingStatus = groupRequests.some(r => 
      r.status && props.statusFilter.has(r.status)
    )
    if (!hasMatchingStatus) return false
  }

  return true
}
```

#### 3. 更新 filteredModelGroups computed

**文件**: `web/src/components/QueuePerspectivePanel.vue`

**修改位置**: 第 573-578 行

```typescript
// 2026-08-28: 先应用上层筛选（模型/供应商/原厂/客户端/状态），再应用节点状态过滤
const filteredModelGroups = computed<ModelGroup[]>(() => {
  return modelGroups.value
    .filter(passesUpperFilters) // 上层筛选（新增）
    .map(group => ({ ...group, nodes: group.nodes.filter(passesStatusFilter) })) // 节点状态过滤（原有）
    .filter(group => group.nodes.length > 0)
})
```

#### 4. LiveRequestStreamV2.vue 传递筛选状态

**文件**: `web/src/components/LiveRequestStreamV2.vue`

**修改位置**: 第 488-499 行

```vue
<!-- 2026-08-28: 传递上层筛选条件到 QueuePerspectivePanel -->
<div v-if="groupBy === 'queue'" class="v32-queue-panels">
  <QueuePerspectivePanel
    :model-filter="modelFilter"
    :provider-filter="providerFilter"
    :vendor-filter="vendorFilter"
    :agent-filter="agentFilter"
    :status-filter="statusFilter"
  />
  <RequestJourneyQueues />
  <NodeStatusMatrix />
</div>
```

---

## 验证结果

### 编译检查

✅ **TypeScript 类型检查**: `npm run typecheck` 通过，无类型错误

✅ **前端构建**: `npm run build` 成功，产物生成正常

### 代码审计

✅ **Props 默认值**: 所有 props 都有 `() => new Set()` 默认值，向下兼容

✅ **性能考虑**: 
- `passesUpperFilters` 只在 `modelGroups` 变化时重新计算（computed 依赖）
- `getRequestsForCredential` 是 liveStreamStore 的索引函数，性能开销可控
- 短路逻辑：筛选器为空时直接返回 true，避免不必要的遍历

✅ **边界情况**:
- 空筛选器：显示所有模型分组 ✓
- 无关联请求的分组：供应商/原厂/客户端/状态筛选时会被排除 ✓
- case-insensitive 模型匹配：与 `useLiveStreamFilters` 行为一致 ✓

### 改动范围

**修改文件**: 2 个

1. `web/src/components/QueuePerspectivePanel.vue` (+92 行, -3 行)
   - 新增 Props 接口和 props 声明（+23 行）
   - 新增 `passesUpperFilters` 函数（+58 行）
   - 更新 `filteredModelGroups` computed（+3 行 / -2 行）
   - 文档注释更新（+8 行）

2. `web/src/components/LiveRequestStreamV2.vue` (+8 行, -1 行)
   - 传递 5 个筛选 props 到 `QueuePerspectivePanel`

**总计**: +100 行, -4 行

---

## 风险评估

### 低风险项

✅ **类型安全**: TypeScript 强类型约束，编译时捕获错误  
✅ **向下兼容**: Props 有默认值，不影响其他潜在调用点  
✅ **单一职责**: 只修改筛选逻辑，不触碰数据源、UI 渲染、状态管理  

### 需要关注

⚠️ **请求数据可用性**: 筛选依赖 `getRequestsForCredential()` 返回的请求数据
- **场景**: 如果某个凭据（credential）当前没有活跃请求，供应商/原厂/客户端/状态筛选会将其排除
- **影响**: 用户可能看不到"有节点但无请求"的模型分组
- **缓解**: 这符合筛选语义——筛选的是"有符合条件请求的模型"，而非"可能接收该类请求的节点"

⚠️ **筛选器字段映射**:
- `providerFilter` (Set<string>) → `LiveRequest.provider_code` (string)
- `vendorFilter` (Set<LiveModelCategory>) → `LiveRequest.model_category` (LiveModelCategory)
- **依赖**: SSE 推送的请求数据必须包含这些字段
- **验证**: 需要在实际环境中确认字段名称一致性（已通过 TypeScript 类型检查）

---

## 后续建议

### 短期（本次发布）

1. ✅ **完成实施**: 代码已修改并通过编译检查
2. 🔲 **人工测试**: 在测试环境验证以下场景：
   - 选择单个模型筛选，确认只显示该模型的节点分组
   - 选择供应商/原厂筛选，确认分组正确过滤
   - 清空所有筛选条件，确认显示所有分组
   - 多维度组合筛选（如：模型 + 供应商 + 状态）

3. 🔲 **监控指标**: 关注以下数据点
   - `filteredModelGroups` 计算性能（Chrome DevTools Performance）
   - 用户筛选器使用频率（Analytics）
   - 筛选后空结果率（UX 指标）

### 中期优化

1. **缓存优化**: 如果 `passesUpperFilters` 成为性能瓶颈，可以引入 memoization
2. **空状态提示**: 当筛选导致所有分组被排除时，显示友好的"无符合条件的模型"提示（当前逻辑会显示空列表）
3. **筛选器持久化**: `useLiveStreamFilters` 已支持 localStorage 持久化，队列视图的筛选状态会自动保存

### 长期改进

1. **节点属性直接化**: 考虑在 `LiveNodeStatus` 接口中直接添加 `provider_code` 和 `vendor`，避免通过请求间接获取
2. **筛选逻辑抽象**: 如果更多组件需要类似的筛选逻辑，可以抽取为 `useUpperFilters` composable

---

## 部署清单

### 前置条件

- [x] TypeScript 类型检查通过
- [x] 前端构建成功
- [ ] 人工功能测试通过

### 部署步骤

1. **构建前端**: `npm run build`（已完成）
2. **提交代码**: 
   ```bash
   git add web/src/components/QueuePerspectivePanel.vue
   git add web/src/components/LiveRequestStreamV2.vue
   git commit -m "fix(stream): apply upper filters to queue perspective panel

   - QueuePerspectivePanel now accepts modelFilter, providerFilter, 
     vendorFilter, agentFilter, and statusFilter props from parent
   - filteredModelGroups applies upper filters before node status filter
   - Empty filter sets display all model groups (backward compatible)
   - Fixes: 按处理队列的模型分组不受顶部筛选条件限制的问题"
   ```
3. **推送到远端**: `git push origin main`
4. **部署到测试环境**: 执行标准部署流程
5. **验证功能**: 按上述测试场景验证
6. **部署到生产环境**: 确认测试环境无问题后执行

### 回滚方案

如果发现问题，可以安全回滚：
```bash
git revert <commit-hash>
npm run build
# 重新部署
```

---

## 总结

### 问题根因

`QueuePerspectivePanel` 组件未接收父组件 `LiveRequestStreamV2` 的筛选状态，导致「按处理队列」视图的模型分组列表无法响应顶部筛选条件。

### 解决方案

通过 props 将筛选状态传递到 `QueuePerspectivePanel`，并在 `filteredModelGroups` computed 中应用上层筛选逻辑，实现与「按泳道」视图一致的筛选行为。

### 关键改进

1. **分层筛选**: 上层筛选（模型/供应商/原厂/客户端/状态）+ 节点状态筛选，清晰分离职责
2. **类型安全**: 使用 TypeScript 严格类型，避免运行时错误
3. **向下兼容**: Props 默认值确保不影响现有功能
4. **性能可控**: computed 依赖优化 + 短路逻辑，避免不必要的计算

### 验证状态

✅ 代码实施完成  
✅ 类型检查通过  
✅ 构建成功  
🔲 人工测试待完成  

---

**审计人**: ZCode Agent  
**审计时间**: 2026-08-28 23:20 CST  
**报告版本**: 1.0

# 2026-08-06 — 抽取 useLiveStreamFilters composable

## 背景

`LiveRequestStreamV2.vue` (1425 行) 是实时请求流的核心组件，承载：
- 泳道可视化（useSwimLane）
- SSE 连接管理（useLiveStream）
- 多维过滤器（5 维度 + 1 请求类型）
- 供应商延迟查询
- 路由诊断事件转发

其中**过滤器状态管理**占用 ~150 行：

| 逻辑块 | 行数 | 内容 |
|---|---|---|
| 状态声明 | 48-82 | requestTypeFilter / statusFilter / modelFilter / providerFilter / vendorFilter / agentFilter / normalizedModelFilter / toggleRequestType / openFilterDialog / standardModelName |
| filteredLanes | 330-385 | 核心过滤逻辑（AND 组合 6 个维度） |
| available* | 387-447 | 从 lanes 提取可选项（5 个 computed） |
| apply* | 450-465 | 弹窗选择后应用（5 个函数） |
| *FilterSelected | 475-480 | 供弹窗回显（5 个 computed） |
| activeFilterCount | 491-499 | 统计活跃过滤器数量 |

按 rule 00 §11.4「加新行为 = 加新类/新策略」原则：
- 改过滤逻辑要改 150 行
- 加新过滤维度（如 error_code）要在 6 处同步改动
- 未来要持久化过滤器状态到 localStorage / 导出配置 / 撤销重做，都需要在组件内部改动

应抽到独立 composable，降低组件复杂度 + 提升可测试性。

## 修复

### `web/src/composables/useLiveStreamFilters.ts`（新建，261 行）

```ts
export interface LiveStreamFiltersOptions {
  lanes: Ref<SwimLane[]> | ComputedRef<SwimLane[]>
}

export function useLiveStreamFilters(options: LiveStreamFiltersOptions) {
  const { lanes } = options

  // 6 个过滤器状态 ref
  const requestTypeFilter = ref<Set<'business' | 'probe'>>(new Set(['business', 'probe']))
  const statusFilter = ref<Set<LiveStatus>>(new Set())
  const modelFilter = ref<Set<string>>(new Set())
  const providerFilter = ref<Set<string>>(new Set())
  const vendorFilter = ref<Set<LiveModelCategory>>(new Set())
  const agentFilter = ref<Set<string>>(new Set())

  // 模型过滤器标准化（case-insensitive）
  const normalizedModelFilter = computed(() =>
    Array.from(modelFilter.value).map(m => m.toLowerCase().trim())
  )

  // 切换请求类型（business / probe）
  function toggleRequestType(type: 'business' | 'probe') { ... }

  // 5 个 apply 函数（弹窗选择后应用）
  function applyStatusFilter(selected: string[]) { ... }
  function applyModelFilter(selected: string[]) { ... }
  function applyProviderFilter(selected: string[]) { ... }
  function applyVendorFilter(selected: string[]) { ... }
  function applyAgentFilter(selected: string[]) { ... }

  // 清空所有过滤器
  function clearAllFilters() { ... }

  // 5 个 available* computed（从 lanes 实时提取可选项）
  const availableStatuses = computed(() => { ... })
  const availableModels = computed(() => { ... })
  const availableProviders = computed(() => { ... })
  const availableVendors = computed(() => { ... })
  const availableAgents = computed(() => { ... })

  // 5 个 *FilterSelected computed（供弹窗回显）
  const statusFilterSelected = computed(() => Array.from(statusFilter.value))
  const modelFilterSelected = computed(() => Array.from(modelFilter.value))
  const providerFilterSelected = computed(() => Array.from(providerFilter.value))
  const vendorFilterSelected = computed(() => Array.from(vendorFilter.value) as string[])
  const agentFilterSelected = computed(() => Array.from(agentFilter.value))

  // 活跃过滤器计数
  const activeFilterCount = computed(() => { ... })

  // 核心过滤逻辑
  const filteredLanes = computed(() => {
    return lanes.value
      .map(lane => ({
        ...lane,
        requests: lane.requests.filter(r => {
          // 请求类型过滤
          const requestType = r.is_probe === true ? 'probe' : 'business'
          if (!requestTypeFilter.value.has(requestType)) return false

          // 状态过滤
          if (statusFilter.value.size > 0 && (!r.status || !statusFilter.value.has(r.status as LiveStatus))) return false

          // 模型过滤（标准名，case-insensitive）
          if (normalizedModelFilter.value.length > 0) {
            const stdModel = standardModelName(r.model).toLowerCase().trim()
            if (!stdModel || !normalizedModelFilter.value.includes(stdModel)) return false
          }

          // 供应商过滤
          if (providerFilter.value.size > 0 && (!r.provider || !providerFilter.value.has(r.provider))) return false

          // 原厂过滤
          if (vendorFilter.value.size > 0 && (!r.vendor || !vendorFilter.value.has(r.vendor as LiveModelCategory))) return false

          // 客户端过滤
          if (agentFilter.value.size > 0) {
            const reqAgent = (r.agent_name || '').trim().toLowerCase()
            if (!reqAgent || !agentFilter.value.has(reqAgent)) return false
          }

          return true
        }),
      }) as SwimLane)
      .filter(lane => lane.requests.length > 0)
  })

  return {
    requestTypeFilter,
    statusFilter,
    modelFilter,
    providerFilter,
    vendorFilter,
    agentFilter,
    normalizedModelFilter,
    toggleRequestType,
    applyStatusFilter,
    applyModelFilter,
    applyProviderFilter,
    applyVendorFilter,
    applyAgentFilter,
    clearAllFilters,
    availableStatuses,
    availableModels,
    availableProviders,
    availableVendors,
    availableAgents,
    statusFilterSelected,
    modelFilterSelected,
    providerFilterSelected,
    vendorFilterSelected,
    agentFilterSelected,
    activeFilterCount,
    filteredLanes,
  }
}
```

行为契约：
1. 所有过滤器默认为空（显示全部）
2. 请求类型过滤器默认 `['business', 'probe']` 全选（显示全部）
3. 模型过滤器 case-insensitive（标准化为小写）
4. 客户端过滤器全小写匹配（SSE 已保证 lowercase）
5. `filteredLanes` computed 返回过滤后泳道（空泳道自动移除）
6. 多维度过滤为 AND 组合（所有条件必须同时满足）

### `web/src/composables/useLiveStreamFilters.test.ts`（新建，277 行）

16 个单元测试覆盖：

| 测试用例 | 验证内容 |
|---|---|
| `initializes with empty filters` | 初始化状态正确（requestTypeFilter.size = 2, 其他 = 0） |
| `toggleRequestType deselects one type but never leaves empty` | 切换请求类型时，最后一项不可取消（防止全空） |
| `applyStatusFilter updates statusFilter` | apply 函数正确更新状态 + activeFilterCount |
| `applyModelFilter case-insensitive normalization` | 模型过滤器自动 toLowerCase（'GPT-4' → 'gpt-4'） |
| `clearAllFilters resets all filters` | 重置所有维度到默认状态 |
| `availableModels extracts unique models` | 从 lanes 提取唯一模型（去重 + case-insensitive） |
| `availableStatuses extracts unique statuses` | 从 lanes 提取唯一状态 |
| `filteredLanes filters by requestType` | 按请求类型过滤（business / probe） |
| `filteredLanes filters by status` | 按状态过滤 |
| `filteredLanes filters by model (case-insensitive)` | 按模型过滤（'GPT-4' 匹配 'gpt-4'） |
| `filteredLanes filters by provider` | 按供应商过滤 |
| `filteredLanes filters by vendor` | 按原厂过滤 |
| `filteredLanes filters by agent (lowercase)` | 按客户端过滤（'zcode' 匹配 'ZCODE'） |
| `filteredLanes removes empty lanes` | 过滤后空泳道自动移除 |
| `filteredLanes combines multiple filters (AND logic)` | 多维度 AND 组合（只返回满足所有条件的请求） |
| `activeFilterCount counts active dimensions` | 正确统计活跃过滤器数量 |

测试用 `makeRequest` / `makeLane` helper 函数构造 SwimLane 数据，避免手工填充所有字段。

### `web/src/components/LiveRequestStreamV2.vue` 接入

| 改动 | 行数 | 内容 |
|---|---|---|
| 移除 | 48-82 | 旧过滤器状态声明（requestTypeFilter / statusFilter / modelFilter / providerFilter / vendorFilter / agentFilter / normalizedModelFilter / toggleRequestType / openFilterDialog / standardModelName） |
| 移除 | 330-499 | 旧过滤逻辑（filteredLanes / available* / apply* / *FilterSelected / activeFilterCount） |
| 保留 | filterDialog ref | UI 控制，仅弹窗显隐状态 |
| 保留 | openFilterDialog / statusOptionLabel / vendorOptionLabel | UI 辅助函数（i18n 翻译） |
| 新增 | 98-137 | `const { ... } = useLiveStreamFilters({ lanes })` — 解构所有过滤器状态和方法 |

净减 ~150 行，组件从 1425 → 1280 行（-10%）。

```diff
 import { useLiveStream, type LiveStatus, type LiveModelCategory } from '../composables/useLiveStream'
 import { useSwimLane } from '../composables/useSwimLane'
+import { useLiveStreamFilters } from '../composables/useLiveStreamFilters'

 const {
   groupBy,
   mode: laneMode,
   setMode: setLaneMode,
   lanes,
   selectedLegends,
   legendItems,
   statusLegendItems,
   setGroupBy,
   toggleLegend,
   clearLegendSelection,
 } = useSwimLane(liveSnapshot)

+// 2026-08-06: 过滤器状态管理抽到 useLiveStreamFilters composable
+const {
+  requestTypeFilter,
+  statusFilter,
+  modelFilter,
+  providerFilter,
+  vendorFilter,
+  agentFilter,
+  toggleRequestType,
+  applyStatusFilter,
+  applyModelFilter,
+  applyProviderFilter,
+  applyVendorFilter,
+  applyAgentFilter,
+  clearAllFilters,
+  availableStatuses,
+  availableModels,
+  availableProviders,
+  availableVendors,
+  availableAgents,
+  statusFilterSelected,
+  modelFilterSelected,
+  providerFilterSelected,
+  vendorFilterSelected,
+  agentFilterSelected,
+  activeFilterCount,
+  filteredLanes,
+} = useLiveStreamFilters({ lanes })
+
+// 2026-07-24: 筛选弹窗状态（保留在组件内，仅 UI 控制）
+const filterDialog = ref<'status' | 'model' | 'provider' | 'vendor' | 'agent' | null>(null)
+
+function openFilterDialog(kind: 'status' | 'model' | 'provider' | 'vendor' | 'agent') {
+  filterDialog.value = kind
+}

-// 2026-07-24: 请求类型过滤。两项都选中表示显示全部请求。
-const requestTypeFilter = ref<Set<'business' | 'probe'>>(new Set(['business', 'probe']))
-// 2026-07-24: 筛选改为弹窗（状态/模型/供应商/原厂）
-// 2026-07-27: 加 "客户端" 维度 (agent_name: zcode/claude-code/opencode/...)
-const filterDialog = ref<'status' | 'model' | 'provider' | 'vendor' | 'agent' | null>(null)
-
-// 2026-07-24: 多维过滤状态
-const statusFilter = ref<Set<LiveStatus>>(new Set())
-const modelFilter = ref<Set<string>>(new Set())
-const providerFilter = ref<Set<string>>(new Set())
-const vendorFilter = ref<Set<LiveModelCategory>>(new Set())
-const agentFilter = ref<Set<string>>(new Set())  // 2026-07-27
-
-// Normalize model filter for case-insensitive comparison
-const normalizedModelFilter = computed(() => 
-  Array.from(modelFilter.value).map(m => m.toLowerCase().trim())
-)
-
-function toggleRequestType(type: 'business' | 'probe') {
-  const next = new Set(requestTypeFilter.value)
-  if (next.has(type)) {
-    if (next.size > 1) next.delete(type)
-  } else {
-    next.add(type)
-  }
-  requestTypeFilter.value = next
-}
-
-function openFilterDialog(kind: 'status' | 'model' | 'provider' | 'vendor' | 'agent') {
-  filterDialog.value = kind
-}
-
-/** 模型维度一律用标准名（tile.model 后端已优先 canonical） */
-function standardModelName(model: string | undefined | null): string {
-  return (model || '').trim()
-}

-const filteredLanes = computed(() => {
-  return lanes.value
-    .map(lane => ({
-      ...lane,
-      requests: lane.requests.filter(r => {
-        // ... 6 维度过滤逻辑 ~50 行
-      }),
-    }))
-    .filter(lane => lane.requests.length > 0)
-})
-
-const availableStatuses = computed(() => { ... })
-const availableModels = computed(() => { ... })
-const availableProviders = computed(() => { ... })
-const availableVendors = computed(() => { ... })
-const availableAgents = computed(() => { ... })
-
-function applyStatusFilter(selected: string[]) { ... }
-function applyModelFilter(selected: string[]) { ... }
-function applyProviderFilter(selected: string[]) { ... }
-function applyVendorFilter(selected: string[]) { ... }
-function applyAgentFilter(selected: string[]) { ... }
-
-function clearAllFilters() { ... }
-
-const statusFilterSelected = computed(() => Array.from(statusFilter.value))
-const modelFilterSelected = computed(() => Array.from(modelFilter.value))
-const providerFilterSelected = computed(() => Array.from(providerFilter.value))
-const vendorFilterSelected = computed(() => Array.from(vendorFilter.value) as string[])
-const agentFilterSelected = computed(() => Array.from(agentFilter.value))
-
-const activeFilterCount = computed(() => { ... })
```

模板层（640+ 行）完全不动，因为所有状态和方法名保持一致。

## 验证

| 项 | 命令 | 结果 |
|---|---|---|
| 全量测试 | `cd web && npx vitest run` | **30 文件 / 179 测试全绿**（+16 用例 vs 上次 163） |
| composable 测试 | `cd web && npx vitest run src/composables/useLiveStreamFilters.test.ts` | **16/16 测试通过** |
| 类型检查 | `cd web && npx vue-tsc --noEmit` | **0 errors** (exit 0) |
| i18n 审计 | `cd web && node scripts/i18n-audit.mjs` | `✅ no missing keys` |
| 构建 | `cd web && npx vite build` | **7.52s 成功** |

## 改动清单

```
web/src/composables/useLiveStreamFilters.ts           (新建, 261 行)
web/src/composables/useLiveStreamFilters.test.ts      (新建, 277 行)
web/src/components/LiveRequestStreamV2.vue            |  -150 / +40
CHANGELOG.md                                          |  +24
docs/changelogs/2026-08-06-extract-live-stream-filters-composable.md (新建)
```

净减组件代码 110 行（1425 → 1280），新增 composable + 测试 538 行，总行数增加 428 行
（但代码重复消除 + 测试覆盖补全 + 可维护性提升）。

## 遗留与风险

- 旧组件直接暴露 ref（如 `statusFilter.value.add('success')`），新 composable 通过 apply
  函数封装。如果有外部直接操作 ref 的代码（不太可能，因为过滤器是组件内部状态），需要
  改用 apply 函数。实测模板层全部通过 `@apply` 事件间接调用，无破坏性改动。
- `filterDialog` ref 保留在组件内（仅 UI 控制），未来如要持久化弹窗状态，需在 composable
  外单独处理（符合单一职责）。
- 未来扩展点（保留在 composable 内）：
  - 持久化过滤器状态到 localStorage（session 级复用）
  - 导出/导入过滤器配置（快速切换预设）
  - 过滤器历史记录（撤销/重做）

## 反模式避免

- 严格按 rule 37 原则 3「精准修改」：LiveRequestStreamV2 只改过滤器相关块，未触碰
  SSE 连接、泳道渲染、供应商延迟等完全不动
- 严格按 rule 09 §5.2.4「单 PR 单任务」：本任务只做过滤器 composable 抽取，未混入
  其它范围改动
- 严格按 rule 43「最小补丁 + UTF-8 + ≤300 行/12000 字符」：所有单次写入均小于
  rule 上限
- 严格按 rule 00 §11.4「加新行为 = 加新类/新策略」：未来加新过滤维度，在 composable
  内新增一处状态 + 一处 apply + 一处 available，不在组件内散布

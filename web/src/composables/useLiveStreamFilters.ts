// useLiveStreamFilters.ts — 实时请求流多维过滤器状态管理
//
// 2026-08-06: 从 LiveRequestStreamV2.vue 抽取的过滤器逻辑（~150 行），集中管理
// 5 个维度（status / model / provider / vendor / agent）+ 1 个请求类型（business / probe）
// 过滤器，避免组件内部状态膨胀。
//
// 行为契约：
//  1. 所有过滤器默认为空（显示全部）
//  2. 请求类型过滤器默认 ['business', 'probe'] 全选（显示全部）
//  3. 模型过滤器 case-insensitive（标准化为小写）
//  4. 客户端过滤器全小写匹配（SSE 已保证 lowercase）
//  5. filteredLanes computed 返回过滤后泳道（空泳道自动移除）
//
// 未来扩展点：
//  - 持久化过滤器状态到 localStorage（session 级复用）
//  - 导出/导入过滤器配置（快速切换预设）
//  - 过滤器历史记录（撤销/重做）

import { ref, computed, type Ref, type ComputedRef } from 'vue'
import type { LiveStatus, LiveModelCategory } from './useLiveStream'
import type { SwimLane } from '../types/swimlane'

export interface LiveStreamFiltersOptions {
  /** 泳道数据源（来自 useSwimLane 的 lanes） */
  lanes: Ref<SwimLane[]> | ComputedRef<SwimLane[]>
}

export function useLiveStreamFilters(options: LiveStreamFiltersOptions) {
  const { lanes } = options

  // ========== 过滤器状态 ==========
  // 2026-07-24: 请求类型过滤。两项都选中表示显示全部请求。
  const requestTypeFilter = ref<Set<'business' | 'probe'>>(new Set(['business', 'probe']))

  // 2026-07-24: 多维过滤状态
  const statusFilter = ref<Set<LiveStatus>>(new Set())
  const modelFilter = ref<Set<string>>(new Set())
  const providerFilter = ref<Set<string>>(new Set())
  const vendorFilter = ref<Set<LiveModelCategory>>(new Set())
  const agentFilter = ref<Set<string>>(new Set())  // 2026-07-27: 客户端过滤

  // Normalize model filter for case-insensitive comparison
  const normalizedModelFilter = computed(() =>
    Array.from(modelFilter.value).map(m => m.toLowerCase().trim())
  )

  // ========== 切换请求类型 ==========
  function toggleRequestType(type: 'business' | 'probe') {
    const next = new Set(requestTypeFilter.value)
    if (next.has(type)) {
      if (next.size > 1) next.delete(type)
    } else {
      next.add(type)
    }
    requestTypeFilter.value = next
  }

  // ========== Apply 函数（弹窗选择后应用） ==========
  function applyStatusFilter(selected: string[]) {
    statusFilter.value = new Set(selected as LiveStatus[])
  }
  function applyModelFilter(selected: string[]) {
    modelFilter.value = new Set(selected)
  }
  function applyProviderFilter(selected: string[]) {
    providerFilter.value = new Set(selected)
  }
  function applyVendorFilter(selected: string[]) {
    vendorFilter.value = new Set(selected as LiveModelCategory[])
  }
  function applyAgentFilter(selected: string[]) {
    // agent_name is matched case-insensitively (see filteredLanes, which
    // lowercases r.agent_name). Normalize incoming selections to lowercase so
    // the stored set stays consistent with availableAgents (always lowercase)
    // and the filter dialog checkbox state (draft.has(opt)) stays in sync.
    agentFilter.value = new Set(selected.map(s => s.toLowerCase()))
  }

  // ========== 清空所有过滤器 ==========
  function clearAllFilters() {
    requestTypeFilter.value = new Set(['business', 'probe'])
    statusFilter.value.clear()
    modelFilter.value.clear()
    providerFilter.value.clear()
    vendorFilter.value.clear()
    agentFilter.value.clear()
  }

  // ========== 可选项列表（从 lanes 实时提取） ==========
  const availableStatuses = computed(() => {
    const statuses = new Set<LiveStatus>()
    for (const lane of lanes.value) {
      for (const req of lane.requests) {
        if (req.status) statuses.add(req.status as LiveStatus)
      }
    }
    return Array.from(statuses).sort()
  })

  const availableModels = computed(() => {
    const modelMap = new Map<string, string>() // lowercase key -> canonical display name
    for (const lane of lanes.value) {
      for (const req of lane.requests) {
        const name = standardModelName(req.model)
        if (name && name !== '[空闲]') {
          const key = name.toLowerCase().trim()
          // Keep first occurrence (prefer backend canonical name)
          if (!modelMap.has(key)) {
            modelMap.set(key, name)
          }
        }
      }
    }
    // Sort case-insensitively by the lowercase key
    return Array.from(modelMap.values()).sort((a, b) =>
      a.localeCompare(b, 'zh-CN', { sensitivity: 'base' })
    )
  })

  const availableProviders = computed(() => {
    const providers = new Set<string>()
    for (const lane of lanes.value) {
      for (const req of lane.requests) {
        if (req.provider) providers.add(req.provider)
      }
    }
    return Array.from(providers).sort()
  })

  const availableVendors = computed(() => {
    const vendors = new Set<LiveModelCategory>()
    for (const lane of lanes.value) {
      for (const req of lane.requests) {
        if (req.vendor) vendors.add(req.vendor as LiveModelCategory)
      }
    }
    return Array.from(vendors).sort()
  })

  // 2026-07-27: 客户端可选项 (从 SSE 实时收到的 agent_name 提取,全小写)
  const availableAgents = computed(() => {
    const agents = new Set<string>()
    for (const lane of lanes.value) {
      for (const req of lane.requests) {
        const a = (req.agent_name || '').trim().toLowerCase()
        if (a) agents.add(a)
      }
    }
    return Array.from(agents).sort()
  })

  // ========== 已选列表（供弹窗回显） ==========
  const statusFilterSelected = computed(() => Array.from(statusFilter.value))
  const modelFilterSelected = computed(() => Array.from(modelFilter.value))
  const providerFilterSelected = computed(() => Array.from(providerFilter.value))
  const vendorFilterSelected = computed(() => Array.from(vendorFilter.value) as string[])
  const agentFilterSelected = computed(() => Array.from(agentFilter.value))

  // ========== 活跃过滤器计数 ==========
  const activeFilterCount = computed(() => {
    let count = 0
    if (requestTypeFilter.value.size < 2) count++
    count += statusFilter.value.size
    count += modelFilter.value.size
    count += providerFilter.value.size
    count += vendorFilter.value.size
    count += agentFilter.value.size
    return count
  })

  // ========== 核心过滤逻辑 ==========
  /** 模型维度一律用标准名（tile.model 后端已优先 canonical） */
  function standardModelName(model: string | undefined | null): string {
    return (model || '').trim()
  }

  const filteredLanes = computed(() => {
    return lanes.value
      .map(lane => ({
        ...lane,
        requests: lane.requests.filter(r => {
          // 请求类型过滤
          const requestType = r.is_probe === true ? 'probe' : 'business'
          if (!requestTypeFilter.value.has(requestType)) {
            return false
          }

          // 状态过滤
          if (statusFilter.value.size > 0 && (!r.status || !statusFilter.value.has(r.status as LiveStatus))) {
            return false
          }

          // 模型过滤（标准名，case-insensitive）
          if (normalizedModelFilter.value.length > 0) {
            const stdModel = standardModelName(r.model).toLowerCase().trim()
            if (!stdModel || !normalizedModelFilter.value.includes(stdModel)) {
              return false
            }
          }

          // 供应商过滤（使用 provider 字段）
          if (providerFilter.value.size > 0 && (!r.provider || !providerFilter.value.has(r.provider))) {
            return false
          }

          // 原厂过滤（使用 vendor 字段）
          if (vendorFilter.value.size > 0 && (!r.vendor || !vendorFilter.value.has(r.vendor as LiveModelCategory))) {
            return false
          }

          // 2026-07-27: 客户端过滤 (使用 agent_name,全小写)
          if (agentFilter.value.size > 0) {
            const reqAgent = (r.agent_name || '').trim().toLowerCase()
            if (!reqAgent || !agentFilter.value.has(reqAgent)) {
              return false
            }
          }

          return true
        }),
      }) as SwimLane) // Preserve full SwimLane type (id, name, dimension, stats, etc.)
      .filter(lane => lane.requests.length > 0)
  })

  return {
    // 状态
    requestTypeFilter,
    statusFilter,
    modelFilter,
    providerFilter,
    vendorFilter,
    agentFilter,
    normalizedModelFilter,

    // 切换
    toggleRequestType,

    // 应用
    applyStatusFilter,
    applyModelFilter,
    applyProviderFilter,
    applyVendorFilter,
    applyAgentFilter,
    clearAllFilters,

    // 可选项
    availableStatuses,
    availableModels,
    availableProviders,
    availableVendors,
    availableAgents,

    // 已选
    statusFilterSelected,
    modelFilterSelected,
    providerFilterSelected,
    vendorFilterSelected,
    agentFilterSelected,

    // 统计
    activeFilterCount,

    // 过滤后结果
    filteredLanes,
  }
}

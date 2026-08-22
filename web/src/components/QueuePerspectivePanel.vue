<script setup lang="ts">
/**
 * QueuePerspectivePanel — 队列透视区（V3.2 FE-A1 → V3.3-OBS OBS-FE2 升级）
 *
 * 实时展示三层队列（pipeline/模型/节点）的深度、拥堵状态和最近请求轨迹，
 * 以及按节点的内联操作（测试 / 强制启用 / 手工禁用下拉）。
 * 数据源：liveStreamStore 的 queue_snapshot / node_update SSE 消息
 * （BE3 pipeline 层 + BE4 节点投影）。
 *
 * 设计约束：
 *   - 颜色必须 var(--kx-*)，禁止硬编码 hex（rule 12）
 *   - OBS-BE3 pipeline 字段缺省时整层隐藏，禁止零值冒充（13号门禁）
 *   - 三态：加载 Skeleton / 空 EmptyState / 错误 ErrorBanner
 *   - 动画只用 transform/opacity
 */
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getFeatured, resolveRouting, reorderCandidateBindings, type CandidateBindingReorderItem, type RoutingCandidate } from '../api/routing'
import { getRequestLogTopModels, type TopRequestModel } from '../api/logs'
import { getSlidingWindow, getSlidingWindowBatch } from '../api/credential-monitor'
import {
  queueRef,
  nodesRef,
  liveStreamState,
  getNodesForModel,
  getRequestsForCredential,
  type LiveNodeStatus,
  type LiveRequest,
} from '../composables/liveStreamStore'
import { isSuperAdmin } from '../store'
import { ApiError } from '../api/_core'
import { readLiveStreamPreferences, writeLiveStreamPreferences, type QueueStatusBucket } from '../composables/liveStreamPreferences'
import {
  WINDOW_MINUTES,
  STATS_REFRESH_MS,
  assignSpacedPriorities,
  cardWidthFromCapacity,
  credentialDisplayName,
  nodeCapacity,
} from '../utils/queueNodeCards'
import RequestProcessingTrail from './RequestProcessingTrail.vue'
import NodeDetailDrawer from './NodeDetailDrawer.vue'

const { t } = useI18n()
const queue = queueRef
const nodes = nodesRef

// OBS-BE3 pipeline 总览：字段缺省（dispatch 未启用/未接线）时整层隐藏。
const pipeline = computed(() => queue.value?.pipeline ?? null)

// 节点统计
const nodeStats = computed(() => {
  const total = nodes.value.length
  const ready = nodes.value.filter(n =>
    n.availability_state === 'ready' &&
    n.circuit_state === 'closed' &&
    n.quota_state === 'ok' &&
    !n.manual_disabled
  ).length
  const suspended = nodes.value.filter(n => n.availability_state === 'suspended').length
  const exhausted = nodes.value.filter(n => n.quota_state?.includes('exhausted')).length
  return { total, ready, suspended, exhausted }
})

// 总队列深度 = 所有模型队列深度之和（总队列是模型队列的上游）
const totalDepth = computed(() => {
  if (!queue.value) return 0
  return queue.value.models.reduce((sum, m) => sum + (m.depth || 0), 0)
})

// 模型队列 Top 5（按深度降序）
const topModels = computed(() => {
  if (!queue.value) return []
  return [...queue.value.models]
    .sort((a, b) => (b.depth || 0) - (a.depth || 0))
    .slice(0, 5)
})

// 节点队列 Top 5（按深度降序）
const topCredentials = computed(() => {
  if (!queue.value) return []
  return [...queue.value.credentials]
    .sort((a, b) => (b.depth || 0) - (a.depth || 0))
    .slice(0, 5)
})

// 拥堵判定：任一层深度超阈值
const CONGESTION_THRESHOLD = 10
const congested = computed(() => {
  if (!queue.value) return false
  return totalDepth.value > CONGESTION_THRESHOLD * 3 ||
    topModels.value.some(m => (m.depth || 0) > CONGESTION_THRESHOLD) ||
    topCredentials.value.some(c => (c.depth || 0) > CONGESTION_THRESHOLD)
})

// 拥堵根因提示：哪一层最堵
const congestionHint = computed(() => {
  if (!congested.value || !queue.value) return ''
  const maxModel = topModels.value[0]
  const maxCred = topCredentials.value[0]
  if (maxCred && (maxCred.depth || 0) >= (maxModel?.depth || 0)) {
    return `节点队列拥堵：credential ${maxCred.credential} 深度 ${maxCred.depth}`
  }
  if (maxModel) {
    return `模型队列拥堵：${maxModel.model} 深度 ${maxModel.depth}`
  }
  return '总队列拥堵'
})

const hasData = computed(() => queue.value !== null && queue.value.wired)
const isIdle = computed(() => hasData.value && totalDepth.value === 0)

// ── 队列深度分区：默认折叠，拥堵时自动展开 ─────────────────────────────────
// 队列深度是诊断信息，畅通时收起让模型分组和节点矩阵成为主内容；
// congested 翻转为 true 时自动展开引导排查，恢复后不自动收起。
const savedQueuePreferences = readLiveStreamPreferences().queue
const queueDepthOpen = ref(savedQueuePreferences.depthOpen ?? false)
const hasExplicitDepthPreference = ref(savedQueuePreferences.depthOpen !== undefined)
const expandedModels = ref<Set<string>>(new Set(savedQueuePreferences.expandedModels))
watch(congested, value => {
  // A congestion diagnosis may expand the panel only before the user selects a
  // preferred state. An explicit collapsed state must survive remounts.
  if (value && !hasExplicitDepthPreference.value) queueDepthOpen.value = true
}, { immediate: true })

function toggleQueueDepth() {
  queueDepthOpen.value = !queueDepthOpen.value
  hasExplicitDepthPreference.value = true
  writeLiveStreamPreferences({ queue: { depthOpen: queueDepthOpen.value } })
}

// ── OBS-UI：按模型分组的可用节点（2026-08-17） ─────────────────────────────
//
// 目标：回答"模型 X 现在有哪些可用节点 / 该节点下当前的请求"。
// - 节点来源：LiveNodeStatus.raw_models（后端投影 credential_model_bindings）
// - 请求来源：liveStreamState.requests ∩ 凭据匹配（requestCredential 索引）
//
// 只在 raw_models 真正上报时渲染该区块（缺省隐藏，禁零值冒充）。
// 任一节点的状态字段都缺省显示，不要为"零"渲染为虚假徽标。
interface ModelScopeMeta {
  key: string
  featured: boolean
  hotRequests: number
  aliases: Set<string>
}

interface ModelGroup {
  model: string
  nodes: LiveNodeStatus[]
  requestCount: number
  featured: boolean
  hotRequests: number
  aliases: string[]
  /** 仅当所有节点都属于同一个完整 raw binding 列表时可安全重排。 */
  reorderRawModel?: string
  /**
   * 服务端 reorder_revision 透传，缺失时表示当前分组不接受重排
   * （别名聚合 / 多 raw-model / 未在 resolve 列表中）。
   */
  reorderRevision?: string
}

const modelScopeMeta = ref<Map<string, ModelScopeMeta>>(new Map())
const modelScopeAliasIndex = ref<Map<string, string>>(new Map())
const modelCandidatesByRawModel = ref<Map<string, RoutingCandidate[]>>(new Map())
const reorderRevisionsByRawModel = ref<Map<string, string>>(new Map())
const modelScopeLoading = ref(true)
const modelScopeError = ref('')
const selectedNode = ref<LiveNodeStatus | null>(null)
const selectedNodeModel = ref('')
const drawerVisible = ref(false)
let modelScopeAbort: AbortController | null = null

function modelKey(model: string): string {
  return model.trim().toLowerCase()
}

// 点击模型分组中的节点卡片：把「模型 + 节点」一起传给详情抽屉。
// aliases 含分组 scope key 与该组的 raw 模型名，取该节点在此分组下的
// raw 绑定作为 scope 模型（与 monitor/sliding-window/history 的 raw 命名一致）。
function openNode(node: LiveNodeStatus, aliases: string[] = []) {
  selectedNode.value = node
  const aliasSet = aliases.map(modelKey)
  selectedNodeModel.value = node.raw_models?.find(model => aliasSet.includes(modelKey(model)))
    ?? node.raw_models?.[0]
    ?? ''
  drawerVisible.value = true
}

async function loadModelScope() {
  if (modelScopeAbort) modelScopeAbort.abort()
  const controller = new AbortController()
  modelScopeAbort = controller
  modelScopeLoading.value = true
  modelScopeError.value = ''
  const to = new Date()
  const from = new Date(to.getTime() - 72 * 60 * 60 * 1000)
  const [featured, hot] = await Promise.allSettled([
    getFeatured(),
    getRequestLogTopModels({ from: from.toISOString(), to: to.toISOString(), limit: 50 }),
  ])
  if (controller.signal.aborted) {
    modelScopeLoading.value = false
    return
  }
  const scope = new Map<string, ModelScopeMeta>()
  const addScope = (model: string, isFeatured: boolean, hotRequests: number) => {
    const key = modelKey(model)
    if (!key) return
    const current = scope.get(key) ?? { key, featured: false, hotRequests: 0, aliases: new Set<string>() }
    current.featured ||= isFeatured
    current.hotRequests = Math.max(current.hotRequests, hotRequests)
    current.aliases.add(key)
    scope.set(key, current)
  }
  if (featured.status === 'fulfilled') featured.value.featured_models.forEach(model => addScope(model, true, 0))
  if (hot.status === 'fulfilled') hot.value.items.forEach((model: TopRequestModel) => addScope(model.canonical_name || model.display_name, false, model.request_count))
  const aliases = new Map<string, string>()
  const candidatesByRawModel = new Map<string, RoutingCandidate[]>()
  const revisionsByRawModel = new Map<string, string>()
  const resolveOne = async (meta: ModelScopeMeta) => {
    const name = [...meta.aliases][0]
    try {
      const resolved = await resolveRouting(name, undefined, false, { signal: controller.signal })
      if (controller.signal.aborted) return
      const assignAlias = (raw: string) => {
        const key = modelKey(raw)
        if (!key) return
        const existing = aliases.get(key)
        if (!existing || existing === meta.key) aliases.set(key, meta.key)
        else aliases.delete(key)
      }
      for (const raw of resolved.raw_models) assignAlias(raw)
      for (const candidate of resolved.candidates) {
        assignAlias(candidate.model_name)
        const key = modelKey(candidate.model_name)
        const candidates = candidatesByRawModel.get(key) ?? []
        candidates.push(candidate)
        candidatesByRawModel.set(key, candidates)
      }
      // Only record a revision when the resolve hit a single raw_model so the
      // panel can safely submit a complete-set reorder against it.
      if (resolved.reorder_revision) {
        const firstName = resolved.candidates[0]?.model_name
        if (firstName && resolved.candidates.every(c => c.model_name === firstName)) {
          revisionsByRawModel.set(modelKey(firstName), resolved.reorder_revision)
        }
      }
    } catch {
      // Keep exact canonical/featured matches; ambiguous aliases stay hidden.
    }
  }
  // 8 并发分块并行解析，避免特色+热门最多 ~60 个模型的串行长尾。
  const scopeEntries = [...scope.values()]
  const RESOLVE_CONCURRENCY = 8
  for (let index = 0; index < scopeEntries.length; index += RESOLVE_CONCURRENCY) {
    if (controller.signal.aborted) break
    await Promise.all(scopeEntries.slice(index, index + RESOLVE_CONCURRENCY).map(resolveOne))
  }
  if (controller.signal.aborted) {
    modelScopeLoading.value = false
    return
  }
  modelScopeMeta.value = scope
  modelScopeAliasIndex.value = aliases
  modelCandidatesByRawModel.value = candidatesByRawModel
  reorderRevisionsByRawModel.value = revisionsByRawModel
  if (featured.status === 'rejected' && hot.status === 'rejected') modelScopeError.value = '模型范围暂不可用，未展示模型节点。'
  modelScopeLoading.value = false
}

onMounted(() => {
  void loadModelScope()
  startStatsPoll()
})
onUnmounted(() => {
  modelScopeAbort?.abort()
  stopStatsPoll()
})

function toggleModel(model: string) {
  const next = new Set(expandedModels.value)
  if (next.has(model)) next.delete(model)
  else next.add(model)
  expandedModels.value = next
  writeLiveStreamPreferences({ queue: { expandedModels: Array.from(next) } })
}

const modelGroups = computed<ModelGroup[]>(() => {
  const rawByScope = new Map<string, Set<string>>()
  for (const node of nodes.value) {
    if (!Array.isArray(node.raw_models)) continue
    for (const rawModel of node.raw_models) {
      const rawKey = modelKey(rawModel)
      const scopeKey = modelScopeAliasIndex.value.get(rawKey) ?? (modelScopeMeta.value.has(rawKey) ? rawKey : '')
      if (!scopeKey) continue
      const rawModels = rawByScope.get(scopeKey) ?? new Set<string>()
      rawModels.add(rawModel)
      rawByScope.set(scopeKey, rawModels)
    }
  }
  const groups: ModelGroup[] = []
  for (const [scopeKey, rawModels] of rawByScope) {
    const meta = modelScopeMeta.value.get(scopeKey)
    if (!meta) continue
    const credentialIds = new Set<number>()
    for (const rawModel of rawModels) {
      for (const node of getNodesForModel(rawModel)) credentialIds.add(node.credential_id)
    }
    const modelNodes = nodes.value.filter(node => credentialIds.has(node.credential_id))
    const aliases = [scopeKey, ...rawModels].map(modelKey)
    const rawModelList = [...rawModels]
    const rawModel = rawModelList.length === 1 ? rawModelList[0] : undefined
    const candidates = rawModel
      ? modelCandidatesByRawModel.value.get(modelKey(rawModel)) ?? []
      : []
    const candidateOrder = new Map(candidates.map((candidate, index) => [candidate.credential_id, index]))
    // Live nodes are often a subset of resolve candidates (offline / filtered-out
    // credentials still exist in the binding set). Require live ⊆ candidates so
    // we can order, label, size cards, and submit a full-set reorder safely.
    const liveCoveredByCandidates = rawModel != null
      && candidates.length > 0
      && modelNodes.every(node => candidateOrder.has(node.credential_id))
    const orderedNodes = liveCoveredByCandidates
      ? [...modelNodes].sort((left, right) => (candidateOrder.get(left.credential_id) ?? Number.MAX_SAFE_INTEGER) - (candidateOrder.get(right.credential_id) ?? Number.MAX_SAFE_INTEGER))
      : modelNodes
    const requestIds = new Set<string>()
    for (const credentialId of credentialIds) {
      for (const request of getRequestsForCredential(credentialId)) {
        if (request.request_id && aliases.includes(modelKey(request.model || ''))) requestIds.add(request.request_id)
      }
    }
    groups.push({
      model: scopeKey,
      nodes: orderedNodes,
      requestCount: requestIds.size,
      featured: meta.featured,
      hotRequests: meta.hotRequests,
      aliases,
      reorderRawModel: liveCoveredByCandidates ? rawModel : undefined,
      reorderRevision: liveCoveredByCandidates && rawModel
        ? reorderRevisionsByRawModel.value.get(modelKey(rawModel))
        : undefined,
    })
  }
  return groups.sort((a, b) => Number(b.featured) - Number(a.featured) || b.hotRequests - a.hotRequests || b.nodes.length - a.nodes.length || a.model.localeCompare(b.model))
})

// ── 节点状态过滤（在用 / 降级 / 人工禁用 / 配额耗尽） ─────────────────────
// 每个节点只归属一个主状态桶：人工禁用 > 耗尽/暂停 > 降级 > 在用。
// 这样取消"耗尽"即可稳定排除耗尽节点，而不会被"在用"的 OR 条件重新匹配。
const statusFilter = ref<Record<QueueStatusBucket, boolean>>(savedQueuePreferences.statusFilter)

function toggleStatusFilter(bucket: QueueStatusBucket) {
  const next = { ...statusFilter.value, [bucket]: !statusFilter.value[bucket] }
  statusFilter.value = next
  writeLiveStreamPreferences({ queue: { statusFilter: next } })
}

function nodeStatusBucket(n: LiveNodeStatus): QueueStatusBucket {
  if (n.manual_disabled || n.disable_kind === 'manual') return 'manualDisabled'
  if ((n.quota_state ?? '').includes('exhausted') || n.availability_state === 'suspended') return 'exhausted'
  if (n.circuit_state === 'open' || n.circuit_state === 'half_open' || n.health_status === 'unreachable'
    || n.availability_state === 'cooling' || n.disable_kind === 'system' || n.fp_disabled) return 'degraded'
  return 'active'
}

function passesStatusFilter(n: LiveNodeStatus): boolean {
  return statusFilter.value[nodeStatusBucket(n)]
}

// 仅展示当前过滤命中的节点；过滤全部命中数 + 命中节点
const filteredModelGroups = computed<ModelGroup[]>(() => {
  return modelGroups.value
    .map(group => ({ ...group, nodes: group.nodes.filter(passesStatusFilter) }))
    .filter(group => group.nodes.length > 0)
})

const hasFilteredGroups = computed(() => filteredModelGroups.value.length > 0)

// ── 节点拖拽调整优先级（HTML5 dnd） ───────────────────────────────────────
// 单一 raw-model + live ⊆ candidates + reorder_revision 即可重排（与状态过滤解耦）。
// 可见子集上拖动时，把相对顺序写回完整候选列表再提交（后端要求完整集原子写）。
const dragScopeKey = ref<string | null>(null)
const dragSourceCredentialId = ref<number | null>(null)
const dragOverCredentialId = ref<number | null>(null)
const dragSaving = ref(false)
const dragError = ref('')

function canReorder(group: ModelGroup): boolean {
  return isSuperAdmin()
    && !dragSaving.value
    && Boolean(group.reorderRawModel)
    && Boolean(group.reorderRevision)
}

function dragDisabledHint(group: ModelGroup): string {
  if (!isSuperAdmin()) return '仅超级管理员可以调整优先级。'
  if (dragSaving.value) return '正在保存优先级调整。'
  if (!group.reorderRawModel) return '该模型分组合并了多个原始模型，或存在不在候选集中的实时节点，无法安全调整优先级。'
  if (!group.reorderRevision) return '尚未拿到后端修订版本，请等待数据加载完成后再试。'
  return '拖动节点以调整优先级，越靠前优先级越高。隐藏状态的节点会保持原有相对位置。'
}

/** Map a reordered visible subset back onto the full candidate list. */
function mergeVisibleOrderIntoFull(
  fullCredentialIds: number[],
  visibleOrderedIds: number[],
): number[] {
  const visibleSet = new Set(visibleOrderedIds)
  const nextVisible = [...visibleOrderedIds]
  return fullCredentialIds.map(id => (visibleSet.has(id) ? nextVisible.shift()! : id))
}

function onDragStart(event: DragEvent, group: ModelGroup, credentialId: number) {
  if (!canReorder(group)) {
    event.preventDefault()
    return
  }
  dragScopeKey.value = group.model
  dragSourceCredentialId.value = credentialId
  dragOverCredentialId.value = credentialId
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/plain', `${group.model}:${credentialId}`)
  }
}

function onDragOver(event: DragEvent, group: ModelGroup, credentialId: number) {
  if (!canReorder(group) || dragScopeKey.value !== group.model) return
  event.preventDefault()
  if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
  dragOverCredentialId.value = credentialId
}

function onDragLeave(group: ModelGroup, credentialId: number) {
  if (dragScopeKey.value !== group.model) return
  if (dragOverCredentialId.value === credentialId) dragOverCredentialId.value = null
}

function isDragTarget(scopeKey: string, credentialId: number): boolean {
  return dragScopeKey.value === scopeKey && dragOverCredentialId.value === credentialId
}

function clearDragState() {
  dragScopeKey.value = null
  dragSourceCredentialId.value = null
  dragOverCredentialId.value = null
}

async function onDrop(event: DragEvent, group: ModelGroup, targetCredentialId: number) {
  if (!canReorder(group) || dragScopeKey.value !== group.model || !group.reorderRawModel || !group.reorderRevision) return
  event.preventDefault()
  const ordered = [...group.nodes]
  const fromIndex = ordered.findIndex(n => n.credential_id === dragSourceCredentialId.value)
  const toIndex = ordered.findIndex(n => n.credential_id === targetCredentialId)
  if (fromIndex < 0 || toIndex < 0 || fromIndex === toIndex) {
    clearDragState()
    return
  }
  const [moved] = ordered.splice(fromIndex, 1)
  ordered.splice(toIndex, 0, moved)
  const rawModel = group.reorderRawModel
  const expectedRevision = group.reorderRevision
  const candidates = modelCandidatesByRawModel.value.get(modelKey(rawModel)) ?? []
  const fullIds = candidates.map(c => c.credential_id)
  // Prefer full candidate list; if somehow empty, fall back to visible order only.
  const mergedIds = fullIds.length > 0
    ? mergeVisibleOrderIntoFull(fullIds, ordered.map(n => n.credential_id))
    : ordered.map(n => n.credential_id)
  const priorities = (() => {
    try {
      return assignSpacedPriorities(mergedIds.length)
    } catch (error) {
      clearDragState()
      dragError.value = error instanceof Error ? error.message : '候选数量超过优先级上限，无法排序'
      return null
    }
  })()
  if (!priorities) return
  const items: CandidateBindingReorderItem[] = mergedIds.map((credentialId, index) => ({
    credential_id: credentialId,
    raw_model: rawModel,
    manual_priority: priorities[index],
  }))
  clearDragState()
  dragSaving.value = true
  dragError.value = ''
  // Optimistic local order so the next drag sees spaced priorities immediately.
  const prevCandidates = modelCandidatesByRawModel.value.get(modelKey(rawModel)) ?? []
  const byId = new Map(prevCandidates.map(c => [c.credential_id, c]))
  const optimistic = mergedIds.map((credentialId, index) => {
    const base = byId.get(credentialId)
    return base
      ? { ...base, manual_priority: priorities[index], rank: index + 1 }
      : {
          credential_id: credentialId,
          model_name: rawModel,
          manual_priority: priorities[index],
          rank: index + 1,
        } as RoutingCandidate
  })
  modelCandidatesByRawModel.value = new Map(modelCandidatesByRawModel.value).set(modelKey(rawModel), optimistic)
  try {
    await reorderCandidateBindings(items, { rawModel, expectedRevision })
  } catch (error) {
    modelCandidatesByRawModel.value = new Map(modelCandidatesByRawModel.value).set(modelKey(rawModel), prevCandidates)
    const fallback = error instanceof Error ? error.message : '调整优先级失败'
    // 409 stale / incomplete / transient ordering conflict — surface a
    // user-friendly hint and refetch so the UI catches up with the server.
    if (error instanceof ApiError && error.status === 409) {
      dragError.value = '排序已过期，已自动刷新候选列表，请重试。'
      void loadModelScope()
    } else {
      dragError.value = fallback
    }
    dragSaving.value = false
    return
  }
  try {
    await loadModelScope()
  } catch (error) {
    // Server already accepted the new order — keep optimistic UI, do not roll back.
    dragError.value = error instanceof Error
      ? `已保存排序，但刷新候选列表失败：${error.message}`
      : '已保存排序，但刷新候选列表失败，请手动刷新。'
  } finally {
    dragSaving.value = false
  }
}

const hasModelGroups = computed(() => modelGroups.value.length > 0)
const hasReportedRawModels = computed(() => nodes.value.some(node => Array.isArray(node.raw_models)))

function nodeStatusSummary(n: LiveNodeStatus): string {
  const parts: string[] = []
  if (n.manual_disabled) parts.push('手工禁用')
  if (n.circuit_state === 'open') parts.push('熔断')
  else if (n.circuit_state === 'half_open') parts.push('半开')
  if (n.fp_disabled) parts.push('fpslot 禁用')
  if (n.disable_kind === 'system') parts.push('系统降级')
  if ((n.quota_state ?? '').includes('exhausted')) parts.push('配额耗尽')
  if (n.availability_state === 'suspended') parts.push('暂停')
  if (parts.length === 0) parts.push('可用')
  return parts.join(' / ')
}

function candidateForNode(group: ModelGroup, credentialId: number): RoutingCandidate | undefined {
  const raw = group.reorderRawModel
  if (!raw) return undefined
  return (modelCandidatesByRawModel.value.get(modelKey(raw)) ?? []).find(c => c.credential_id === credentialId)
}

function providerLabel(n: LiveNodeStatus): string {
  return n.provider_code || (n.provider_id ? `P${n.provider_id}` : '—')
}

function nodeTitle(n: LiveNodeStatus, group?: ModelGroup): string {
  const candidate = group ? candidateForNode(group, n.credential_id) : undefined
  return credentialDisplayName(candidate, providerLabel(n), n.credential_id)
}

function nodePriorityLabel(group: ModelGroup, n: LiveNodeStatus, index: number): string {
  const candidate = candidateForNode(group, n.credential_id)
  const priority = candidate?.manual_priority
  const rank = index + 1
  return typeof priority === 'number' ? `#${rank} · p${priority}` : `#${rank}`
}

function groupMaxCapacity(group: ModelGroup): number {
  let max = 1
  for (const node of group.nodes) {
    max = Math.max(max, nodeCapacity(candidateForNode(group, node.credential_id)))
  }
  return max
}

function nodeCardWidth(group: ModelGroup, n: LiveNodeStatus): number {
  return cardWidthFromCapacity(nodeCapacity(candidateForNode(group, n.credential_id)), groupMaxCapacity(group))
}

type WindowStatsLite = { success: number; failed: number; total: number }
const windowStatsByKey = ref<Map<string, WindowStatsLite>>(new Map())
let statsTimer: ReturnType<typeof setInterval> | null = null
let statsAbort: AbortController | null = null

function statsKey(credentialId: number, model: string): string {
  return `${credentialId}:${modelKey(model)}`
}

function windowStatsFor(group: ModelGroup, credentialId: number): WindowStatsLite | null {
  if (!group.reorderRawModel) return null
  return windowStatsByKey.value.get(statsKey(credentialId, group.reorderRawModel)) ?? null
}

async function refreshWindowStats() {
  const targets: Array<{ credentialId: number; model: string }> = []
  const seen = new Set<string>()
  for (const group of filteredModelGroups.value) {
    if (!group.reorderRawModel) continue
    for (const node of group.nodes) {
      const key = statsKey(node.credential_id, group.reorderRawModel)
      if (seen.has(key)) continue
      seen.add(key)
      targets.push({ credentialId: node.credential_id, model: group.reorderRawModel })
    }
  }
  if (!targets.length) return
  statsAbort?.abort()
  const controller = new AbortController()
  statsAbort = controller
  const next = new Map(windowStatsByKey.value)

  const applyStats = (credentialId: number, model: string, success: number, failed: number, total: number) => {
    next.set(statsKey(credentialId, model), { success, failed, total })
  }

  try {
    const batch = await getSlidingWindowBatch(
      targets.map(t => ({ credential_id: t.credentialId, model: t.model })),
      WINDOW_MINUTES,
      { signal: controller.signal },
    )
    if (controller.signal.aborted) return
    for (const row of batch.results || []) {
      if (!row || row.error || !row.stats) continue
      applyStats(row.credential_id, row.model, row.stats.success ?? 0, row.stats.failed ?? 0, row.stats.total ?? 0)
    }
  } catch {
    // Whole-batch failure → fall back to legacy N-way GET polling.
    if (controller.signal.aborted) return
    const concurrency = 6
    for (let i = 0; i < targets.length; i += concurrency) {
      const slice = targets.slice(i, i + concurrency)
      await Promise.all(slice.map(async ({ credentialId, model }) => {
        try {
          const result = await getSlidingWindow(credentialId, model, WINDOW_MINUTES, { signal: controller.signal })
          if (controller.signal.aborted) return
          applyStats(credentialId, model, result.stats?.success ?? 0, result.stats?.failed ?? 0, result.stats?.total ?? 0)
        } catch {
          // keep previous / leave missing — card shows em dash
        }
      }))
    }
  }
  if (!controller.signal.aborted) windowStatsByKey.value = next
}

function startStatsPoll() {
  stopStatsPoll()
  void refreshWindowStats()
  statsTimer = setInterval(() => { void refreshWindowStats() }, STATS_REFRESH_MS)
}
function stopStatsPoll() {
  if (statsTimer) { clearInterval(statsTimer); statsTimer = null }
  statsAbort?.abort()
  statsAbort = null
}

// Fingerprint only credential×model targets — ignore live node heartbeat churn.
const statsTargetFingerprint = computed(() => {
  const keys: string[] = []
  const seen = new Set<string>()
  for (const group of filteredModelGroups.value) {
    if (!group.reorderRawModel) continue
    for (const node of group.nodes) {
      const key = statsKey(node.credential_id, group.reorderRawModel)
      if (seen.has(key)) continue
      seen.add(key)
      keys.push(key)
    }
  }
  return keys.sort().join('|')
})

watch(statsTargetFingerprint, () => { void refreshWindowStats() })

// null = 字段未上报，按"未知"灰点展示（不冒充健康）。
function circuitOk(n: LiveNodeStatus): boolean | null {
  return n.circuit_state ? n.circuit_state === 'closed' : null
}
function availabilityOk(n: LiveNodeStatus): boolean | null {
  return n.availability_state
    ? n.availability_state === 'ready' || n.availability_state === 'active'
    : null
}
function quotaOk(n: LiveNodeStatus): boolean | null {
  return n.quota_state ? n.quota_state === 'ok' : null
}
function healthOk(n: LiveNodeStatus): boolean | null {
  return n.health_status ? n.health_status === 'healthy' : null
}

function dotClass(ok: boolean | null): string {
  if (ok === null) return 'qp-dot--unknown'
  return ok ? 'qp-dot--ok' : 'qp-dot--bad'
}

function nodeCardTone(n: LiveNodeStatus): string {
  if (n.manual_disabled || n.disable_kind === 'manual') return 'qp-node-card--disabled'
  if (n.circuit_state === 'open' || n.health_status === 'unreachable') return 'qp-node-card--danger'
  if (n.circuit_state === 'half_open' || n.availability_state === 'cooling' ||
      (n.quota_state ?? '').includes('exhausted')) return 'qp-node-card--warn'
  return 'qp-node-card--ok'
}

function requestsForNode(n: LiveNodeStatus, aliases?: string[]): LiveRequest[] {
  const requests = getRequestsForCredential(n.credential_id)
  if (!aliases?.length) return requests
  return requests.filter(request => aliases.includes(modelKey(request.model || '')))
}

function formatLatency(ms: number | null | undefined): string {
  if (ms == null) return '—'
  if (ms < 1000) return `${ms}ms`
  return `${(ms / 1000).toFixed(2)}s`
}

function formatTs(ts: string | undefined): string {
  if (!ts) return ''
  try {
    const d = new Date(ts)
    if (Number.isNaN(d.getTime())) return ts
    return d.toLocaleTimeString()
  } catch {
    return ts
  }
}
</script>

<template>
  <div class="queue-perspective">
    <div class="qp-header">
      <span class="qp-title">队列透视</span>
      <span v-if="congested" class="qp-badge qp-badge--warn">{{ congestionHint }}</span>
      <span v-else-if="hasData" class="qp-badge qp-badge--ok">畅通</span>
    </div>

    <!-- 空态 -->
    <div v-if="!hasData" class="qp-empty">
      <span class="qp-empty-text">队列数据未接入（dispatch 未启用或未 wired）</span>
    </div>

    <!-- 紧凑指标条：调度链路（BE3 缺省隐藏对应项）+ 节点健康度 一行看完 -->
    <div v-else class="qp-layers">
      <div class="qp-stats">
        <span v-if="pipeline" class="qp-stat">
          <span class="qp-stat-label">调度链路</span>
          <span class="qp-stat-value">排队 {{ pipeline.depth }} · 在途 {{ pipeline.inFlight }}<template v-if="typeof pipeline.waitingMsP50 === 'number'"> · p50 {{ pipeline.waitingMsP50 }}ms</template><template v-if="typeof pipeline.waitingMsP95 === 'number'"> · p95 {{ pipeline.waitingMsP95 }}ms</template><template v-if="pipeline.degraded"> · <em class="qp-stat-degraded">降级</em></template></span>
        </span>
        <span class="qp-stat">
          <span class="qp-stat-label">节点</span>
          <span class="qp-stat-value">{{ nodeStats.total }} 总 · {{ nodeStats.ready }} 可用<template v-if="nodeStats.suspended > 0"> · {{ nodeStats.suspended }} 暂停</template><template v-if="nodeStats.exhausted > 0"> · {{ nodeStats.exhausted }} 配额耗尽</template></span>
        </span>
      </div>

      <div v-if="isIdle" class="qp-idle">
        ✅ 当前无排队请求，调度链路畅通
      </div>

      <!-- 队列深度（诊断信息，默认折叠；拥堵时自动展开） -->
      <div class="qp-layer">
        <button type="button" class="qp-layer-header qp-depth-toggle" :aria-expanded="queueDepthOpen" @click="toggleQueueDepth">
          <span class="qp-layer-name">队列深度</span>
          <span class="qp-depth-summary">
            <span class="qp-layer-depth" :class="{ 'qp-depth--high': totalDepth > CONGESTION_THRESHOLD * 3 }">{{ totalDepth }}</span>
            <span class="qp-layer-count">{{ queue?.models.length || 0 }} 模型 · {{ queue?.credentials.length || 0 }} 节点</span>
          </span>
          <span class="qp-model-group-caret" :class="{ 'qp-model-group-caret--open': queueDepthOpen }">▸</span>
        </button>
        <div v-if="queueDepthOpen" class="qp-depth-body">
          <div class="qp-row">
            <span class="qp-row-label qp-row-label--total">总队列</span>
            <div class="qp-bar">
              <div
                class="qp-bar-fill qp-bar-fill--total"
                :style="{ transform: `scaleX(${Math.min(1, totalDepth / (CONGESTION_THRESHOLD * 6))})` }"
              />
            </div>
            <span class="qp-row-depth" :class="{ 'qp-depth--high': totalDepth > CONGESTION_THRESHOLD * 3 }">
              {{ totalDepth }}
            </span>
          </div>
          <div v-for="m in topModels" :key="m.model" class="qp-row">
            <span class="qp-row-label">{{ m.model }}</span>
            <div class="qp-bar qp-bar--sm">
              <div
                class="qp-bar-fill qp-bar-fill--model"
                :style="{ transform: `scaleX(${Math.min(1, (m.depth || 0) / (CONGESTION_THRESHOLD * 2))})` }"
              />
            </div>
            <span class="qp-row-depth" :class="{ 'qp-depth--high': (m.depth || 0) > CONGESTION_THRESHOLD }">
              {{ m.depth }}
            </span>
          </div>
          <div v-for="c in topCredentials" :key="c.credential" class="qp-row">
            <span class="qp-row-label">节点 {{ c.credential }}</span>
            <div class="qp-bar qp-bar--sm">
              <div
                class="qp-bar-fill qp-bar-fill--cred"
                :style="{ transform: `scaleX(${Math.min(1, (c.depth || 0) / (CONGESTION_THRESHOLD * 2))})` }"
              />
            </div>
            <span class="qp-row-depth" :class="{ 'qp-depth--high': (c.depth || 0) > CONGESTION_THRESHOLD }">
              {{ c.depth }}
            </span>
          </div>
        </div>
      </div>

      <!-- Dashboard 只保留特色模型和近 3 天有实际流量的热门模型；实时 SSE
           不提供 raw_models 时保持整个分区隐藏，避免把未知误报为无绑定。 -->
      <div v-if="hasReportedRawModels && (hasModelGroups || modelScopeLoading || modelScopeError)" class="qp-layer qp-layer--model-groups">
        <div class="qp-layer-header">
          <span class="qp-layer-name">按模型分组的可用节点</span>
          <span v-if="hasModelGroups" class="qp-layer-count">{{ filteredModelGroups.length }} 个模型<template v-if="modelGroups.length !== filteredModelGroups.length"> / {{ modelGroups.length }}</template></span>
          <button type="button" class="qp-retry" :disabled="modelScopeLoading || dragSaving" @click="loadModelScope">{{ dragSaving ? '正在保存…' : (modelScopeLoading ? t('requestJourneys.modelScopeLoading') : `↻ ${t('requestJourneys.refreshScope')}`) }}</button>
        </div>

        <!-- 状态过滤多选框：在用 / 降级 / 人工禁用 / 配额耗尽 -->
        <div v-if="hasModelGroups" class="qp-status-filters" role="group" :aria-label="'状态过滤'">
          <label class="qp-status-filter" :class="{ 'is-active': statusFilter.active }">
            <input type="checkbox" :checked="statusFilter.active" @change="toggleStatusFilter('active')" />
            <span class="qp-status-filter-dot qp-dot--ok" aria-hidden="true"></span>
            <span>在用</span>
          </label>
          <label class="qp-status-filter" :class="{ 'is-active': statusFilter.degraded }">
            <input type="checkbox" :checked="statusFilter.degraded" @change="toggleStatusFilter('degraded')" />
            <span class="qp-status-filter-dot qp-dot--bad" aria-hidden="true"></span>
            <span>降级</span>
          </label>
          <label class="qp-status-filter" :class="{ 'is-active': statusFilter.manualDisabled }">
            <input type="checkbox" :checked="statusFilter.manualDisabled" @change="toggleStatusFilter('manualDisabled')" />
            <span class="qp-status-filter-dot qp-status-filter-dot--muted" aria-hidden="true"></span>
            <span>人工禁用</span>
          </label>
          <label class="qp-status-filter" :class="{ 'is-active': statusFilter.exhausted }">
            <input type="checkbox" :checked="statusFilter.exhausted" @change="toggleStatusFilter('exhausted')" />
            <span class="qp-status-filter-dot qp-dot--bad" aria-hidden="true"></span>
            <span>耗尽</span>
          </label>
          <span v-if="dragError" class="qp-status-filter-error">{{ dragError }}</span>
        </div>

        <div v-if="modelScopeLoading" class="qp-model-scope-state">{{ t('requestJourneys.modelScopeLoading') }}</div>
        <div v-else-if="modelScopeError" class="qp-model-scope-state qp-model-scope-state--error">{{ t('requestJourneys.modelScopeError') }}</div>
        <div v-else-if="!hasFilteredGroups && hasModelGroups" class="qp-model-scope-state">当前过滤条件下没有可用节点。请调整状态过滤多选框。</div>
        <div v-else-if="!hasModelGroups" class="qp-model-scope-state">{{ t('requestJourneys.noModelNodes') }}</div>
        <div v-else v-for="group in filteredModelGroups" :key="group.model" class="qp-model-group">
          <div class="qp-model-compact">
            <button type="button" class="qp-model-group-toggle" :aria-expanded="expandedModels.has(group.model)" @click="toggleModel(group.model)">
              <span class="qp-model-group-caret" :class="{ 'qp-model-group-caret--open': expandedModels.has(group.model) }">▸</span>
              <strong class="qp-model-group-name">{{ group.model }}</strong>
            </button>
            <span v-if="group.featured" class="qp-model-tag">特色</span>
            <span v-if="group.hotRequests" class="qp-model-tag qp-model-tag--hot">热门 {{ group.hotRequests }}</span>
            <span class="qp-pill">{{ group.nodes.length }} 节点</span>
            <span class="qp-pill" :class="{ 'qp-pill--active': group.requestCount > 0 }">{{ group.requestCount }} 当前请求</span>
            <span class="qp-pill qp-pill--hint" :title="dragDisabledHint(group)">{{ canReorder(group) ? '拖动调整优先级' : '优先级排序不可用' }}</span>
            <div class="qp-model-nodes">
              <div
                v-for="(node, nodeIndex) in group.nodes"
                :key="node.credential_id"
                class="qp-node-card-wrap"
                :class="{ 'is-drag-over': isDragTarget(group.model, node.credential_id) }"
                :style="{ width: `${nodeCardWidth(group, node)}px` }"
                @dragover="onDragOver($event, group, node.credential_id)"
                @dragleave="onDragLeave(group, node.credential_id)"
                @drop="onDrop($event, group, node.credential_id)"
              >
                <button
                  type="button"
                  class="qp-node-card"
                  :class="nodeCardTone(node)"
                  :title="`${nodeTitle(node, group)}：${nodeStatusSummary(node)}${node.last_error ? `；${node.last_error}` : ''}`"
                  :draggable="canReorder(group)"
                  @click="openNode(node, group.aliases)"
                  @dragstart="onDragStart($event, group, node.credential_id)"
                  @dragend="clearDragState"
                >
                  <span class="qp-node-card-drag-handle" aria-hidden="true">⋮⋮</span>
                  <span class="qp-node-card-title">{{ nodeTitle(node, group) }}</span>
                  <span class="qp-node-card-dots" :title="`熔断 ${node.circuit_state || '未知'} · 可用性 ${node.availability_state || '未知'} · 配额 ${node.quota_state || '未知'} · 健康 ${node.health_status || '未知'}`">
                    <i class="qp-dot" :class="dotClass(circuitOk(node))" /><i class="qp-dot" :class="dotClass(availabilityOk(node))" /><i class="qp-dot" :class="dotClass(quotaOk(node))" /><i class="qp-dot" :class="dotClass(healthOk(node))" />
                    <span class="qp-node-rank">{{ nodePriorityLabel(group, node, nodeIndex) }}</span>
                  </span>
                  <span class="qp-node-card-meta">
                    <template v-if="windowStatsFor(group, node.credential_id)">
                      <span class="qp-stat-ok">✓{{ windowStatsFor(group, node.credential_id)!.success }}</span>
                      <span class="qp-stat-fail">✗{{ windowStatsFor(group, node.credential_id)!.failed }}</span>
                      <span class="qp-stat-window">· {{ WINDOW_MINUTES }}m</span>
                    </template>
                    <template v-else>
                      <span class="qp-stat-ok">✓—</span>
                      <span class="qp-stat-fail">✗—</span>
                      <span class="qp-stat-window">· {{ WINDOW_MINUTES }}m</span>
                    </template>
                    <template v-if="node.in_flight"> · 在途 {{ node.in_flight }}</template>
                  </span>
                </button>
              </div>
            </div>
          </div>
          <div v-if="expandedModels.has(group.model)" class="qp-model-group-body">
            <p class="qp-model-detail-hint">{{ t('requestJourneys.nodeDetailHint') }}</p>
            <ul v-if="group.nodes.some(node => requestsForNode(node, group.aliases).length)" class="qp-model-group-requests">
              <template v-for="node in group.nodes" :key="node.credential_id">
                <li v-for="request in requestsForNode(node, group.aliases)" :key="request.request_id" class="qp-model-group-request">
                  <span class="qp-rq-node">{{ nodeTitle(node, group) }}</span><span class="qp-rq-model">{{ request.model || '—' }}</span><span class="qp-rq-status" :class="`qp-rq-status--${request.status}`">{{ request.status || '—' }}</span><span v-if="typeof request.latency_ms === 'number'" class="qp-rq-latency">{{ formatLatency(request.latency_ms) }}</span><span v-if="request.error_kind" class="qp-rq-err">{{ request.error_kind }}</span><span class="qp-rq-ts">{{ formatTs(request.ts) }}</span>
                </li>
              </template>
            </ul>
            <p v-else class="qp-model-detail-hint">{{ t('requestJourneys.noNodeRequests') }}</p>
          </div>
        </div>
      </div>

      <RequestProcessingTrail />
      <NodeDetailDrawer v-model="drawerVisible" :node="selectedNode" :model="selectedNodeModel" @applied="drawerVisible = true" />
    </div>
  </div>
</template>

<style scoped>
.queue-perspective {
  background: var(--kx-surface);
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-md, 8px);
  padding: 12px 16px;
  margin-bottom: 12px;
}
.qp-header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}
.qp-title {
  font-weight: 600;
  color: var(--kx-text);
}
.qp-badge {
  font-size: 12px;
  padding: 2px 8px;
  border-radius: 10px;
}
.qp-badge--ok {
  background: color-mix(in srgb, var(--kx-success) 12%, var(--kx-surface));
  color: var(--kx-success);
}
.qp-badge--warn {
  background: color-mix(in srgb, var(--kx-warning) 12%, var(--kx-surface));
  color: var(--kx-warning);
}
.qp-empty {
  padding: 16px;
  text-align: center;
}
.qp-empty-text {
  color: var(--kx-text-secondary);
  font-size: 13px;
}
.qp-layers {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

/* 紧凑指标条：调度链路 + 节点健康度合并为一行 */
.qp-stats {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 16px;
  padding: 7px 12px;
  background: var(--kx-bg-elevated);
  border-radius: var(--kx-radius-sm, 6px);
}
.qp-stat {
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
}
.qp-stat-label {
  font-size: 11px;
  color: var(--kx-text-secondary);
  white-space: nowrap;
}
.qp-stat-value {
  font-size: 12px;
  font-weight: 600;
  color: var(--kx-text);
  font-variant-numeric: tabular-nums;
  overflow-wrap: anywhere;
}
.qp-stat-degraded {
  font-style: normal;
  font-size: 11px;
  padding: 1px 6px;
  border-radius: 10px;
  background: color-mix(in srgb, var(--kx-warning) 14%, var(--kx-surface));
  color: var(--kx-warning);
}

.qp-idle {
  color: var(--kx-text-secondary);
  font-size: 13px;
  padding: 8px 12px;
  background: color-mix(in srgb, var(--kx-success) 12%, var(--kx-surface));
  border-radius: var(--kx-radius-sm, 6px);
}

/* 队列深度折叠分区 */
.qp-depth-toggle {
  all: unset;
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
  cursor: pointer;
  box-sizing: border-box;
  width: 100%;
}
.qp-depth-toggle:focus-visible {
  outline: 2px solid var(--kx-primary);
  outline-offset: 2px;
}
.qp-depth-summary {
  display: inline-flex;
  align-items: baseline;
  gap: 8px;
  margin-left: auto;
}
.qp-depth-body {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding-top: 4px;
}
.qp-row-label--total {
  font-weight: 600;
  color: var(--kx-text);
}
.qp-layer-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 4px;
}
.qp-layer-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--kx-text);
}
.qp-layer-count {
  font-size: 12px;
  color: var(--kx-text-secondary);
}
.qp-layer-depth {
  font-size: 14px;
  font-weight: 600;
  color: var(--kx-text);
}
.qp-depth--high {
  color: var(--kx-danger);
}
.qp-bar {
  height: 6px;
  background: var(--kx-bg-elevated);
  border-radius: 3px;
  overflow: hidden;
}
.qp-bar--sm {
  height: 4px;
  flex: 1;
}
.qp-bar-fill {
  height: 100%;
  transform-origin: left;
  transition: transform 0.3s ease;
}
.qp-bar-fill--total { background: var(--kx-primary); }
.qp-bar-fill--model { background: var(--kx-success); }
.qp-bar-fill--cred { background: var(--kx-warning); }
.qp-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 2px 0;
}
.qp-row-label {
  font-size: 12px;
  color: var(--kx-text-secondary);
  min-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.qp-row-depth {
  font-size: 12px;
  font-weight: 600;
  color: var(--kx-text);
  min-width: 24px;
  text-align: right;
}

/* ── OBS-UI：按模型分组的可用节点（2026-08-17） ────────────────────────── */
.qp-layer--model-groups {
  /* 用主题已有的 surface-soft 别名（color-mix 微透明）做柔和下层；无主题覆盖时
     退到 --kx-bg 让区块与上层 --kx-surface 形成微弱对比。 */
  background: var(--surface-soft, var(--kx-bg, var(--kx-surface)));
  border-radius: var(--kx-radius-sm, 6px);
  padding: 8px 10px;
}
.qp-model-group {
  border-top: 1px solid var(--kx-border);
  padding: 6px 0;
}
.qp-model-group:first-child {
  border-top: none;
}
.qp-model-group-toggle {
  all: unset;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  padding: 4px 0;
  color: var(--kx-text);
}
.qp-model-compact { display:flex; align-items:center; gap:7px; flex-wrap:wrap; padding:7px 9px; }
.qp-model-tag { font-size:10px; padding:2px 6px; border-radius:999px; color:var(--kx-accent); background:color-mix(in srgb, var(--kx-accent) 12%, transparent); }
.qp-model-tag--hot { color:var(--kx-warning); background:color-mix(in srgb, var(--kx-warning) 12%, transparent); }
.qp-model-nodes { display:flex; gap:6px; flex-wrap:wrap; flex:1 1 100%; padding-left:18px; }
.qp-node-card-wrap { border-radius:6px; transition: background 120ms ease, outline-color 120ms ease, width 160ms ease; outline: 2px dashed transparent; outline-offset: 1px; flex: 0 0 auto; }
.qp-node-card-wrap.is-drag-over { background: color-mix(in srgb, var(--kx-accent) 14%, transparent); outline-color: var(--kx-accent); }
.qp-node-card { display:grid; gap:4px; text-align:left; width:100%; box-sizing:border-box; padding:7px 9px; border:1px solid var(--kx-border); border-left-width:3px; border-radius:6px; background:var(--kx-surface); cursor:pointer; color:var(--kx-text); position:relative; }
.qp-node-rank { margin-left:6px; font-size:10px; color:var(--kx-muted); font-variant-numeric: tabular-nums; }
.qp-node-card-meta { display:flex; flex-wrap:wrap; gap:4px; align-items:baseline; font-size:11px; color:var(--kx-muted); overflow:hidden; }
.qp-stat-ok { color: var(--kx-success); font-variant-numeric: tabular-nums; }
.qp-stat-fail { color: var(--kx-danger); font-variant-numeric: tabular-nums; }
.qp-stat-window { color: var(--kx-muted); }
.qp-node-card:hover { border-color:var(--kx-accent); }
.qp-node-card-drag-handle { position:absolute; top:3px; right:5px; font-size:10px; color:var(--kx-text-secondary); opacity:.6; line-height:1; user-select:none; }
.qp-node-card[draggable="true"] { cursor: grab; }
.qp-node-card[draggable="true"]:active { cursor: grabbing; }
.qp-node-card-title { font-size:12px; font-weight:600; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; padding-right:14px; }
.qp-node-card-dots { display:inline-flex; gap:4px; align-items:center; }
.qp-dot { width:7px; height:7px; border-radius:50%; background:var(--kx-text-secondary); opacity:.5; }
.qp-dot--ok { background:var(--kx-success); opacity:1; }
.qp-dot--bad { background:var(--kx-danger); opacity:1; }
.qp-dot--unknown { background:var(--kx-text-secondary); opacity:.4; }
.qp-node-card--ok { border-left-color:var(--kx-success); }
.qp-node-card--warn { border-left-color:var(--kx-warning); }
.qp-node-card--danger { border-left-color:var(--kx-danger); }
.qp-node-card--disabled { border-left-color:var(--kx-text-secondary); opacity:.72; }
.qp-retry { margin-left:auto; border:0; background:transparent; color:var(--kx-text-secondary); cursor:pointer; font-size:11px; }
.qp-model-scope-state,.qp-model-detail-hint { color:var(--kx-text-secondary); font-size:11px; padding:8px 10px; margin:0; }
.qp-model-scope-state--error { color:var(--kx-danger); }
.qp-model-group-toggle:focus-visible {
  outline: 2px solid var(--kx-primary);
  outline-offset: 2px;
}
.qp-model-group-caret {
  display: inline-block;
  width: 12px;
  font-size: 12px;
  color: var(--kx-muted, var(--kx-text));
  transition: transform 120ms ease;
}
.qp-model-group-caret--open {
  transform: rotate(90deg);
}
.qp-model-group-name {
  font-weight: 600;
  font-size: 13px;
  color: var(--kx-text);
}
.qp-model-group-counts {
  display: inline-flex;
  gap: 6px;
  margin-left: auto;
}
.qp-pill {
  display: inline-block;
  font-size: 11px;
  padding: 1px 8px;
  border-radius: 10px;
  /* 用 --kx-bg 比 surface 略深，做出"凹陷 pill" 视觉；无主题时退到
     --kx-surface 保持可见性。 */
  background: var(--kx-bg, var(--kx-surface));
  border: 1px solid var(--kx-border);
  color: var(--kx-muted, var(--kx-text));
}
.qp-pill--active {
  color: var(--kx-primary);
  border-color: var(--kx-primary);
}
.qp-model-group-body {
  padding: 6px 0 4px 20px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.qp-model-group-node {
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm, 6px);
  padding: 6px 8px;
  background: var(--kx-surface);
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.qp-model-group-node-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  font-size: 12px;
  color: var(--kx-muted, var(--kx-text));
}
.qp-status-text {
  color: var(--kx-text);
  font-weight: 500;
}
.qp-meta-text--err {
  color: var(--kx-danger);
}
.qp-model-group-requests {
  list-style: none;
  margin: 4px 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.qp-model-group-request {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  font-size: 12px;
  padding: 2px 4px;
  border-top: 1px dashed var(--kx-border);
  color: var(--kx-text);
}
.qp-model-group-request:first-child {
  border-top: none;
}
.qp-rq-model {
  font-weight: 500;
  min-width: 120px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.qp-rq-status {
  font-size: 11px;
  padding: 0 6px;
  border-radius: 6px;
  background: var(--kx-bg, var(--kx-surface));
  color: var(--kx-muted, var(--kx-text));
  border: 1px solid var(--kx-border);
}
.qp-rq-status--success {
  color: var(--kx-success);
  border-color: var(--kx-success);
}
.qp-rq-status--failure {
  color: var(--kx-danger);
  border-color: var(--kx-danger);
}
.qp-rq-status--in_progress {
  color: var(--kx-primary);
  border-color: var(--kx-primary);
}
.qp-rq-status--rate_limited {
  color: var(--kx-warning);
  border-color: var(--kx-warning);
}
.qp-rq-latency {
  color: var(--kx-muted, var(--kx-text));
}
.qp-rq-err {
  color: var(--kx-danger);
}
.qp-rq-ts {
  margin-left: auto;
  color: var(--kx-muted, var(--kx-text));
  font-variant-numeric: tabular-nums;
}

/* ── FE-A5 (2026-08-19): 状态过滤多选框 + 拖拽样式 ──────────────────────── */
.qp-status-filters {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px 10px;
  padding: 6px 4px 8px;
  border-bottom: 1px dashed var(--kx-border);
  margin-bottom: 6px;
}
.qp-status-filter {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  padding: 3px 9px;
  border-radius: 999px;
  border: 1px solid var(--kx-border);
  background: var(--kx-surface);
  color: var(--kx-text-secondary);
  cursor: pointer;
  user-select: none;
  transition: border-color 120ms ease, color 120ms ease, background 120ms ease;
}
.qp-status-filter:hover { border-color: var(--kx-accent); color: var(--kx-text); }
.qp-status-filter.is-active { color: var(--kx-text); border-color: color-mix(in srgb, var(--kx-accent) 50%, var(--kx-border)); background: color-mix(in srgb, var(--kx-accent) 8%, var(--kx-surface)); }
.qp-status-filter input { display: none; }
.qp-status-filter-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  display: inline-block;
  background: var(--kx-text-secondary);
  opacity: .5;
}
.qp-status-filter-dot.qp-dot--ok { background: var(--kx-success); opacity: 1; }
.qp-status-filter-dot.qp-dot--bad { background: var(--kx-danger); opacity: 1; }
.qp-status-filter-dot--muted { background: var(--kx-text-secondary); opacity: .8; }
.qp-status-filter-error {
  margin-left: 6px;
  font-size: 11px;
  color: var(--kx-danger);
}
.qp-pill--hint {
  border-style: dashed;
  color: var(--kx-muted, var(--kx-text-secondary));
  cursor: help;
}
</style>

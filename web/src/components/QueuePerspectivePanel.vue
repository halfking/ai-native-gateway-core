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
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getFeatured, resolveRouting } from '../api/routing'
import { getRequestLogTopModels, type TopRequestModel } from '../api/logs'
import {
  queueRef,
  nodesRef,
  liveStreamState,
  getNodesForModel,
  getRequestsForCredential,
  type LiveNodeStatus,
  type LiveRequest,
} from '../composables/liveStreamStore'
import RequestProcessingTrail from './RequestProcessingTrail.vue'
import NodeOpsRow from './NodeOpsRow.vue'
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

// ── OBS-FE2：节点操作行（26号 §2 最右列） ────────────────────────────────────
// 异常/禁用节点排前，健康节点按在途数排后；上限 8 行避免把面板撑爆。
// 只有真实节点数据（node_update 推送）才渲染，无数据时整节隐藏。
function nodeSeverity(n: LiveNodeStatus): number {
  if (n.manual_disabled || n.disable_kind === 'manual') return 0
  if (n.circuit_state === 'open' || n.health_status === 'unreachable') return 1
  if (n.fp_disabled) return 1
  if (n.circuit_state === 'half_open' || n.availability_state === 'cooling' ||
      (n.quota_state ?? '').includes('exhausted') || n.disable_kind === 'system') return 2
  return 3
}

const opsNodes = computed<LiveNodeStatus[]>(() => {
  if (nodes.value.length === 0) return []
  return [...nodes.value]
    .sort((a, b) => {
      const diff = nodeSeverity(a) - nodeSeverity(b)
      if (diff !== 0) return diff
      return (b.in_flight ?? 0) - (a.in_flight ?? 0)
    })
    .slice(0, 8)
})

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
}

const modelScopeMeta = ref<Map<string, ModelScopeMeta>>(new Map())
const modelScopeAliasIndex = ref<Map<string, string>>(new Map())
const modelScopeLoading = ref(true)
const modelScopeError = ref('')
const selectedNode = ref<LiveNodeStatus | null>(null)
const drawerVisible = ref(false)

function modelKey(model: string): string {
  return model.trim().toLowerCase()
}

function openNode(node: LiveNodeStatus) {
  selectedNode.value = node
  drawerVisible.value = true
}

async function loadModelScope() {
  modelScopeLoading.value = true
  modelScopeError.value = ''
  const to = new Date()
  const from = new Date(to.getTime() - 72 * 60 * 60 * 1000)
  const [featured, hot] = await Promise.allSettled([
    getFeatured(),
    getRequestLogTopModels({ from: from.toISOString(), to: to.toISOString(), limit: 50 }),
  ])
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
  for (const meta of scope.values()) {
    const name = [...meta.aliases][0]
    try {
      const resolved = await resolveRouting(name)
      const assignAlias = (raw: string) => {
        const key = modelKey(raw)
        if (!key) return
        const existing = aliases.get(key)
        if (!existing || existing === meta.key) aliases.set(key, meta.key)
        else aliases.delete(key)
      }
      for (const raw of resolved.raw_models) assignAlias(raw)
      for (const candidate of resolved.candidates) assignAlias(candidate.model_name)
    } catch {
      // Keep exact canonical/featured matches; ambiguous aliases stay hidden.
    }
  }
  modelScopeMeta.value = scope
  modelScopeAliasIndex.value = aliases
  if (featured.status === 'rejected' && hot.status === 'rejected') modelScopeError.value = '模型范围暂不可用，未展示模型节点。'
  modelScopeLoading.value = false
}

onMounted(() => { void loadModelScope() })

const expandedModels = ref<Set<string>>(new Set())

function toggleModel(model: string) {
  const next = new Set(expandedModels.value)
  if (next.has(model)) next.delete(model)
  else next.add(model)
  expandedModels.value = next
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
    const requestIds = new Set<string>()
    for (const credentialId of credentialIds) {
      for (const request of getRequestsForCredential(credentialId)) {
        if (request.request_id && aliases.includes(modelKey(request.model || ''))) requestIds.add(request.request_id)
      }
    }
    groups.push({ model: scopeKey, nodes: modelNodes, requestCount: requestIds.size, featured: meta.featured, hotRequests: meta.hotRequests, aliases })
  }
  return groups.sort((a, b) => Number(b.featured) - Number(a.featured) || b.hotRequests - a.hotRequests || b.nodes.length - a.nodes.length || a.model.localeCompare(b.model))
})

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

    <!-- 三层队列 -->
    <div v-else class="qp-layers">
      <!-- 节点健康度摘要 -->
      <div class="qp-node-summary">
        <span class="qp-summary-item">
          <span class="qp-summary-label">总节点</span>
          <span class="qp-summary-value">{{ nodeStats.total }}</span>
        </span>
        <span class="qp-summary-item qp-summary-item--ok">
          <span class="qp-summary-label">可用</span>
          <span class="qp-summary-value">{{ nodeStats.ready }}</span>
        </span>
        <span v-if="nodeStats.suspended > 0" class="qp-summary-item qp-summary-item--warn">
          <span class="qp-summary-label">暂停</span>
          <span class="qp-summary-value">{{ nodeStats.suspended }}</span>
        </span>
        <span v-if="nodeStats.exhausted > 0" class="qp-summary-item qp-summary-item--danger">
          <span class="qp-summary-label">配额耗尽</span>
          <span class="qp-summary-value">{{ nodeStats.exhausted }}</span>
        </span>
      </div>

      <div v-if="isIdle" class="qp-idle">
        ✅ 当前无排队请求，调度链路畅通
      </div>

      <!-- OBS-BE3 pipeline 总览行：字段缺省时整层隐藏（禁止零值冒充）。
           waitingMsP50/P95 无样本时省略（不是 0）；degraded 才显示降级徽标。 -->
      <div v-if="pipeline" class="qp-pipeline" :class="{ 'qp-pipeline--degraded': pipeline.degraded }">
        <span class="qp-pipeline-label">调度链路</span>
        <span class="qp-pipeline-stat">
          <span class="qp-pipeline-stat-label">排队</span>
          <span class="qp-pipeline-stat-value">{{ pipeline.depth }}</span>
        </span>
        <span class="qp-pipeline-stat">
          <span class="qp-pipeline-stat-label">在途</span>
          <span class="qp-pipeline-stat-value">{{ pipeline.inFlight }}</span>
        </span>
        <span v-if="typeof pipeline.waitingMsP50 === 'number'" class="qp-pipeline-stat">
          <span class="qp-pipeline-stat-label">等待 p50</span>
          <span class="qp-pipeline-stat-value">{{ pipeline.waitingMsP50 }}ms</span>
        </span>
        <span v-if="typeof pipeline.waitingMsP95 === 'number'" class="qp-pipeline-stat">
          <span class="qp-pipeline-stat-label">p95</span>
          <span class="qp-pipeline-stat-value">{{ pipeline.waitingMsP95 }}ms</span>
        </span>
        <span v-if="pipeline.degraded" class="qp-pipeline-degraded">降级</span>
      </div>

      <!-- 总队列 -->
      <div class="qp-layer">
        <div class="qp-layer-header">
          <span class="qp-layer-name">总队列</span>
          <span class="qp-layer-depth" :class="{ 'qp-depth--high': totalDepth > CONGESTION_THRESHOLD * 3 }">
            {{ totalDepth }}
          </span>
        </div>
        <div class="qp-bar">
          <div
            class="qp-bar-fill qp-bar-fill--total"
            :style="{ transform: `scaleX(${Math.min(1, totalDepth / (CONGESTION_THRESHOLD * 6))})` }"
          />
        </div>
      </div>

      <!-- 模型队列 -->
      <div class="qp-layer">
        <div class="qp-layer-header">
          <span class="qp-layer-name">模型队列</span>
          <span class="qp-layer-count">{{ queue?.models.length || 0 }} 个模型</span>
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
      </div>

      <!-- 节点队列 -->
      <div class="qp-layer">
        <div class="qp-layer-header">
          <span class="qp-layer-name">节点队列</span>
          <span class="qp-layer-count">{{ queue?.credentials.length || 0 }} 个节点</span>
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

      <!-- OBS-FE2 节点操作区（26号 §2 最右列）：测试 / 强制启用 / 手工禁用下拉。
           异常优先、上限 8 行，超出的用 n/total 标注（不做无提示截断）。 -->
      <div v-if="opsNodes.length > 0" class="qp-layer">
        <div class="qp-layer-header">
          <span class="qp-layer-name">节点操作</span>
          <span class="qp-layer-count">{{ opsNodes.length }}/{{ nodes.length }} 个节点</span>
        </div>
        <NodeOpsRow v-for="n in opsNodes" :key="n.credential_id" :node="n" />
      </div>

      <!-- Dashboard 只保留特色模型和近 3 天有实际流量的热门模型；实时 SSE
           不提供 raw_models 时保持整个分区隐藏，避免把未知误报为无绑定。 -->
      <div v-if="hasReportedRawModels && (hasModelGroups || modelScopeLoading || modelScopeError)" class="qp-layer qp-layer--model-groups">
        <div class="qp-layer-header">
          <span class="qp-layer-name">按模型分组的可用节点</span>
          <span v-if="hasModelGroups" class="qp-layer-count">{{ modelGroups.length }} 个模型</span>
          <button type="button" class="qp-retry" :disabled="modelScopeLoading" @click="loadModelScope">{{ modelScopeLoading ? t('requestJourneys.modelScopeLoading') : `↻ ${t('requestJourneys.refreshScope')}` }}</button>
        </div>
        <div v-if="modelScopeLoading" class="qp-model-scope-state">{{ t('requestJourneys.modelScopeLoading') }}</div>
        <div v-else-if="modelScopeError" class="qp-model-scope-state qp-model-scope-state--error">{{ t('requestJourneys.modelScopeError') }}</div>
        <div v-else-if="!hasModelGroups" class="qp-model-scope-state">{{ t('requestJourneys.noModelNodes') }}</div>
        <div v-else v-for="group in modelGroups" :key="group.model" class="qp-model-group">
          <div class="qp-model-compact">
            <button type="button" class="qp-model-group-toggle" :aria-expanded="expandedModels.has(group.model)" @click="toggleModel(group.model)">
              <span class="qp-model-group-caret" :class="{ 'qp-model-group-caret--open': expandedModels.has(group.model) }">▸</span>
              <strong class="qp-model-group-name">{{ group.model }}</strong>
            </button>
            <span v-if="group.featured" class="qp-model-tag">特色</span>
            <span v-if="group.hotRequests" class="qp-model-tag qp-model-tag--hot">热门 {{ group.hotRequests }}</span>
            <span class="qp-pill">{{ group.nodes.length }} 节点</span>
            <span class="qp-pill" :class="{ 'qp-pill--active': group.requestCount > 0 }">{{ group.requestCount }} 当前请求</span>
            <span class="qp-model-nodes">
              <button v-for="node in group.nodes" :key="node.credential_id" type="button" class="qp-node-chip" :class="{ 'qp-node-chip--bad': nodeStatusSummary(node) !== '可用' }" :title="`节点 ${node.credential_id}：${nodeStatusSummary(node)}${node.last_error ? `；${node.last_error}` : ''}`" @click="openNode(node)">
                节点 {{ node.credential_id }}（{{ nodeStatusSummary(node) }}<template v-if="node.in_flight"> · 在途 {{ node.in_flight }}</template><template v-if="node.last_latency_ms != null"> · {{ formatLatency(node.last_latency_ms) }}</template>）
              </button>
            </span>
          </div>
          <div v-if="expandedModels.has(group.model)" class="qp-model-group-body">
            <p class="qp-model-detail-hint">{{ t('requestJourneys.nodeDetailHint') }}</p>
            <ul v-if="group.nodes.some(node => requestsForNode(node, group.aliases).length)" class="qp-model-group-requests">
              <template v-for="node in group.nodes" :key="node.credential_id">
                <li v-for="request in requestsForNode(node, group.aliases)" :key="request.request_id" class="qp-model-group-request">
                  <span class="qp-rq-node">节点 {{ node.credential_id }}</span><span class="qp-rq-model">{{ request.model || '—' }}</span><span class="qp-rq-status" :class="`qp-rq-status--${request.status}`">{{ request.status || '—' }}</span><span v-if="typeof request.latency_ms === 'number'" class="qp-rq-latency">{{ formatLatency(request.latency_ms) }}</span><span v-if="request.error_kind" class="qp-rq-err">{{ request.error_kind }}</span><span class="qp-rq-ts">{{ formatTs(request.ts) }}</span>
                </li>
              </template>
            </ul>
            <p v-else class="qp-model-detail-hint">{{ t('requestJourneys.noNodeRequests') }}</p>
          </div>
        </div>
      </div>

      <RequestProcessingTrail />
      <NodeDetailDrawer v-model="drawerVisible" :node="selectedNode" @applied="drawerVisible = true" />
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

/* 节点健康度摘要 */
.qp-node-summary {
  display: flex;
  gap: 12px;
  padding: 8px 12px;
  background: var(--kx-bg-elevated);
  border-radius: var(--kx-radius-sm, 6px);
  margin-bottom: 4px;
}
.qp-summary-item {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 12px;
}
.qp-summary-label {
  color: var(--kx-text-secondary);
  font-size: 11px;
}
.qp-summary-value {
  font-weight: 600;
  font-size: 14px;
  color: var(--kx-text);
}
.qp-summary-item--ok .qp-summary-value {
  color: var(--kx-success);
}
.qp-summary-item--warn .qp-summary-value {
  color: var(--kx-warning);
}
.qp-summary-item--danger .qp-summary-value {
  color: var(--kx-danger);
}

.qp-idle {
  color: var(--kx-text-secondary);
  font-size: 13px;
  padding: 8px 12px;
  background: color-mix(in srgb, var(--kx-success) 12%, var(--kx-surface));
  border-radius: var(--kx-radius-sm, 6px);
}

/* OBS-BE3 pipeline 总览行 */
.qp-pipeline {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 4px 14px;
  padding: 6px 12px;
  border: 1px solid color-mix(in srgb, var(--kx-primary) 30%, var(--kx-border));
  border-radius: var(--kx-radius-sm, 6px);
  background: color-mix(in srgb, var(--kx-primary) 6%, var(--kx-surface));
}
.qp-pipeline--degraded {
  border-color: var(--kx-warning);
  background: color-mix(in srgb, var(--kx-warning) 10%, var(--kx-surface));
}
.qp-pipeline-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--kx-text);
}
.qp-pipeline-stat {
  display: inline-flex;
  align-items: baseline;
  gap: 4px;
}
.qp-pipeline-stat-label {
  font-size: 11px;
  color: var(--kx-text-secondary);
}
.qp-pipeline-stat-value {
  font-size: 13px;
  font-weight: 600;
  color: var(--kx-text);
  font-variant-numeric: tabular-nums;
}
.qp-pipeline-degraded {
  font-size: 11px;
  padding: 1px 8px;
  border-radius: 10px;
  background: color-mix(in srgb, var(--kx-warning) 14%, var(--kx-surface));
  color: var(--kx-warning);
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
.qp-model-nodes { display:inline-flex; gap:5px; flex-wrap:wrap; flex:1 1 100%; padding-left:18px; }
.qp-node-chip { border:0; border-bottom:1px dashed var(--kx-border); background:transparent; color:var(--kx-text-secondary); cursor:pointer; padding:2px 0; font-size:11px; }
.qp-node-chip:hover { color:var(--kx-accent); }
.qp-node-chip--bad { color:var(--kx-danger); }
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
</style>

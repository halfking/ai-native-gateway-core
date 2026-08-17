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
import { computed, ref } from 'vue'
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
interface ModelGroup {
  model: string
  nodes: LiveNodeStatus[]
  requestCount: number
}

const expandedModels = ref<Set<string>>(new Set())

function toggleModel(model: string) {
  const next = new Set(expandedModels.value)
  if (next.has(model)) next.delete(model)
  else next.add(model)
  expandedModels.value = next
}

const modelGroups = computed<ModelGroup[]>(() => {
  // 从节点收集 distinct 模型；按节点数降序以稳定展示
  const byModel = new Map<string, Set<number>>()
  for (const n of nodes.value) {
    if (!n || !Array.isArray(n.raw_models)) continue
    for (const m of n.raw_models) {
      if (!m) continue
      let set = byModel.get(m)
      if (!set) {
        set = new Set<number>()
        byModel.set(m, set)
      }
      set.add(n.credential_id)
    }
  }
  const out: ModelGroup[] = []
  for (const [model, credSet] of byModel) {
    const credNodes = getNodesForModel(model)
    if (credNodes.length === 0) continue
    let requestCount = 0
    for (const credID of credSet) {
      requestCount += getRequestsForCredential(credID).length
    }
    out.push({ model, nodes: credNodes, requestCount })
  }
  out.sort((a, b) => b.nodes.length - a.nodes.length || a.model.localeCompare(b.model))
  return out
})

const hasModelGroups = computed(() => modelGroups.value.length > 0)

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

function requestsForNode(n: LiveNodeStatus): LiveRequest[] {
  return getRequestsForCredential(n.credential_id)
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

      <!-- OBS-UI：按模型分组的可用节点（2026-08-17） ───────────────────────────
           缺省隐藏：raw_models 未上报时整节不出。
           节点复用 NodeOpsRow；节点下请求列表来自 requestCredential 索引
           （窗口语义：仅 SSE 回放窗口内的请求）。 -->
      <div v-if="hasModelGroups" class="qp-layer qp-layer--model-groups">
        <div class="qp-layer-header">
          <span class="qp-layer-name">按模型分组的可用节点</span>
          <span class="qp-layer-count">{{ modelGroups.length }} 个模型</span>
        </div>
        <div v-for="g in modelGroups" :key="g.model" class="qp-model-group">
          <button
            type="button"
            class="qp-model-group-toggle"
            :aria-expanded="expandedModels.has(g.model)"
            @click="toggleModel(g.model)"
          >
            <span class="qp-model-group-caret" :class="{ 'qp-model-group-caret--open': expandedModels.has(g.model) }">▸</span>
            <span class="qp-model-group-name">{{ g.model }}</span>
            <span class="qp-model-group-counts">
              <span class="qp-pill">{{ g.nodes.length }} 节点</span>
              <span class="qp-pill" :class="{ 'qp-pill--active': g.requestCount > 0 }">
                {{ g.requestCount }} 请求
              </span>
            </span>
          </button>
          <div v-if="expandedModels.has(g.model)" class="qp-model-group-body">
            <div v-for="n in g.nodes" :key="n.credential_id" class="qp-model-group-node">
              <NodeOpsRow :node="n" />
              <div class="qp-model-group-node-meta">
                <span class="qp-status-text">{{ nodeStatusSummary(n) }}</span>
                <span v-if="typeof n.last_latency_ms === 'number'" class="qp-meta-text">
                  最近延迟 {{ formatLatency(n.last_latency_ms) }}
                </span>
                <span v-if="typeof n.in_flight === 'number' && n.in_flight > 0" class="qp-meta-text">
                  在途 {{ n.in_flight }}
                </span>
                <span v-if="n.last_error" class="qp-meta-text qp-meta-text--err">
                  最近错误：{{ n.last_error }}
                </span>
              </div>
              <ul
                v-if="requestsForNode(n).length > 0"
                class="qp-model-group-requests"
                :data-testid="`mng-requests-${n.credential_id}`"
              >
                <li
                  v-for="r in requestsForNode(n)"
                  :key="r.request_id"
                  class="qp-model-group-request"
                >
                  <span class="qp-rq-model">{{ r.model || '—' }}</span>
                  <span class="qp-rq-status" :class="`qp-rq-status--${r.status}`">{{ r.status || '—' }}</span>
                  <span v-if="typeof r.latency_ms === 'number'" class="qp-rq-latency">
                    {{ formatLatency(r.latency_ms) }}
                  </span>
                  <span v-if="r.error_kind" class="qp-rq-err">{{ r.error_kind }}</span>
                  <span class="qp-rq-ts">{{ formatTs(r.ts) }}</span>
                </li>
              </ul>
            </div>
          </div>
        </div>
      </div>

      <RequestProcessingTrail />
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
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  cursor: pointer;
  padding: 4px 0;
  color: var(--kx-text);
}
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

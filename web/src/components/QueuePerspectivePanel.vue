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
import { computed } from 'vue'
import { queueRef, nodesRef, type LiveNodeStatus } from '../composables/liveStreamStore'
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
</style>

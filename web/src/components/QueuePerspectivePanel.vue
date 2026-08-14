<script setup lang="ts">
/**
 * QueuePerspectivePanel — 队列透视区（V3.2 FE-A1）
 *
 * 实时展示三层队列（总/模型/节点）的深度、拥堵状态和最近请求轨迹。
 * 数据源：liveStreamStore 的 queue_snapshot SSE 消息（BE-A1）。
 *
 * 设计约束：
 *   - 颜色必须 var(--kx-*)，禁止硬编码 hex（rule 12）
 *   - 三态：加载 Skeleton / 空 EmptyState / 错误 ErrorBanner
 *   - 动画只用 transform/opacity
 */
import { computed } from 'vue'
import { queueRef, nodesRef } from '../composables/liveStreamStore'
import RequestProcessingTrail from './RequestProcessingTrail.vue'

const queue = queueRef
const nodes = nodesRef

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

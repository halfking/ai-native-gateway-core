<script setup lang="ts">
/**
 * QueuePerspectivePanel — 队列透视区（V3.2 FE-A1）
 *
 * KEEP: 等待 V3.2 BE-A1 wire SetQueueSnapshotProvider + liveStreamStore 加 queueRef state 后启用。
 * 当前为 WIP（`.v32wip` 后缀）— 引用了尚未实现的 queueRef。
 * 重命名为 .vue 会编译失败。@v3-team 2026-Q3 review.
 *
 * 实时展示三层队列（总/模型/节点）的深度与拥堵状态。
 * 数据源：liveStreamStore 的 queue_snapshot SSE 消息（BE-A1）。
 *
 * 设计约束：
 *   - 颜色必须 var(--kx-*)，禁止硬编码 hex（rule 12）
 *   - 三态：加载 Skeleton / 空 EmptyState / 错误 ErrorBanner
 *   - 动画只用 transform/opacity
 */
import { computed } from 'vue'
import { queueRef } from '../composables/liveStreamStore'

const queue = queueRef

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
  background: var(--kx-success-bg, rgba(103, 194, 58, 0.12));
  color: var(--kx-success);
}
.qp-badge--warn {
  background: var(--kx-warning-bg, rgba(230, 162, 60, 0.12));
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

<script setup lang="ts">
import { computed } from 'vue'
import { requestsRef, type LiveRequest } from '../composables/liveStreamStore'

const recentRequests = computed(() => requestsRef.value
  .filter((request) => request.type !== 'idle_marker' && request.request_id)
  .slice(-3)
  .reverse())

function requestLabel(request: LiveRequest) {
  return request.request_id?.slice(-8) || 'unknown'
}

function statusLabel(request: LiveRequest) {
  if (request.status === 'success') return '已完成'
  if (request.status === 'failure') return '失败'
  if (request.status === 'rate_limited') return '受限'
  return '处理中'
}

function statusClass(request: LiveRequest) {
  if (request.status === 'success') return 'trail-request--success'
  if (request.status === 'failure' || request.status === 'rate_limited') return 'trail-request--danger'
  return 'trail-request--active'
}
</script>

<template>
  <section class="processing-trail">
    <div class="trail-header">
      <span class="trail-title">最近请求处理轨迹</span>
      <span class="trail-note">来自实时请求流</span>
    </div>

    <div v-if="recentRequests.length === 0" class="trail-empty">等待新请求进入网关</div>
    <div v-else class="trail-list">
      <article
        v-for="request in recentRequests"
        :key="request.request_id"
        class="trail-request"
        :class="statusClass(request)"
      >
        <div class="trail-request-header">
          <span class="trail-request-id">#{{ requestLabel(request) }}</span>
          <span class="trail-model">{{ request.model || '未知模型' }}</span>
          <span class="trail-status">{{ statusLabel(request) }}</span>
          <span v-if="request.latency_ms != null" class="trail-latency">{{ request.latency_ms }}ms</span>
        </div>
        <div class="trail-steps">
          <span class="trail-step">进入网关</span>
          <i aria-hidden="true">→</i>
          <span class="trail-step">路由选择</span>
          <i aria-hidden="true">→</i>
          <span class="trail-step trail-step--provider">{{ request.provider_code || '选择节点' }}</span>
          <i aria-hidden="true">→</i>
          <span class="trail-step">{{ request.status === 'in_progress' ? '等待响应' : statusLabel(request) }}</span>
        </div>
      </article>
    </div>
  </section>
</template>

<style scoped>
.processing-trail {
  border-top: 1px solid var(--kx-border);
  padding-top: 12px;
}
.trail-header,
.trail-request-header,
.trail-steps {
  display: flex;
  align-items: center;
}
.trail-header {
  justify-content: space-between;
  margin-bottom: 8px;
}
.trail-title,
.trail-request-id {
  color: var(--kx-text);
  font-weight: 600;
}
.trail-title { font-size: 13px; }
.trail-note,
.trail-empty,
.trail-latency { color: var(--kx-text-secondary); font-size: 12px; }
.trail-list { display: grid; gap: 8px; }
.trail-request {
  border-left: 3px solid var(--kx-primary);
  padding: 6px 0 6px 10px;
}
.trail-request--success { border-left-color: var(--kx-success); }
.trail-request--danger { border-left-color: var(--kx-danger); }
.trail-request--active { border-left-color: var(--kx-warning); }
.trail-request-header { gap: 8px; font-size: 12px; }
.trail-model {
  color: var(--kx-text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.trail-status { color: var(--kx-text-secondary); margin-left: auto; }
.trail-steps {
  gap: 6px;
  margin-top: 5px;
  color: var(--kx-text-secondary);
  font-size: 12px;
  overflow-x: auto;
  white-space: nowrap;
}
.trail-steps i { color: var(--kx-border-strong, var(--kx-border)); font-style: normal; }
.trail-step--provider { color: var(--kx-text); font-weight: 500; }
@media (max-width: 640px) {
  .trail-note { display: none; }
  .trail-request-header { flex-wrap: wrap; }
  .trail-status { margin-left: 0; }
}
</style>

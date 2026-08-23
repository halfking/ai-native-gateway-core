<script setup lang="ts">
// RequestProcessingTrail — 最近请求处理轨迹（V3.2 FE-A1 → V3.3-OBS OBS-FE2 升级）
//
// 24号 §7：轨迹由动作事件驱动（原为快照驱动）。进行中请求的小形态卡显示
// stage 徽标（仅后端上报时）+ 最新动作（request_lifecycle 推送）+ 当前节点；
// 管道两端解耦：左端客户端协议（client_protocol），右端当前上游节点
// （最新动作的 credential_id）。动作事件未推送时退回快照文案，
// 不造第二状态机、不用零值冒充。
import { computed } from 'vue'
import { requestsRef, getRequestActions, type ActionEvent, type LiveRequest } from '../composables/liveStreamStore'
import { actionEventLabel } from '../composables/liveStreamDisplay'
import { credentialDisplayName } from '../composables/useCredentialLabels'

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

// 最新动作（seq 升序取末位）；未推送时返回 null，由模板退回快照文案。
function latestAction(requestId?: string): ActionEvent | null {
  if (!requestId) return null
  const list = getRequestActions(requestId)
  return list.length > 0 ? list[list.length - 1] : null
}

// 右端当前上游节点：最近一个携带 credential_id 的动作（24号 §2 语义）。
function currentNode(requestId?: string): number | null {
  if (!requestId) return null
  const list = getRequestActions(requestId)
  for (let i = list.length - 1; i >= 0; i--) {
    const cred = list[i].credential_id
    if (typeof cred === 'number' && cred > 0) return cred
  }
  return null
}
</script>

<template>
  <section class="processing-trail">
    <div class="trail-header">
      <span class="trail-title">最近请求处理轨迹</span>
      <span class="trail-note">动作事件驱动</span>
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
          <!-- 左端：客户端侧（协议），管道两端解耦（26号 §2） -->
          <span class="trail-step trail-step--client">{{ request.client_protocol || '客户端' }}</span>
          <i aria-hidden="true">→</i>
          <template v-if="latestAction(request.request_id)">
            <!-- stage 徽标：仅后端上报 stage 时渲染（禁止猜测值冒充） -->
            <span v-if="request.stage" class="trail-stage">{{ request.stage }}</span>
            <span class="trail-step trail-step--latest">{{ actionEventLabel(latestAction(request.request_id)?.action) }}</span>
            <template v-if="currentNode(request.request_id)">
              <i aria-hidden="true">→</i>
              <span class="trail-step trail-step--provider">{{ credentialDisplayName(currentNode(request.request_id)) }}</span>
            </template>
          </template>
          <template v-else>
            <span class="trail-step">进入网关</span>
            <i aria-hidden="true">→</i>
            <span class="trail-step">路由选择</span>
            <i aria-hidden="true">→</i>
            <span class="trail-step trail-step--provider">{{ request.provider_code || '选择节点' }}</span>
          </template>
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
.trail-step--client { color: var(--kx-muted, var(--kx-text)); }
/* 最新动作（动作事件驱动）+ stage 徽标（仅后端上报时渲染） */
.trail-step--latest { color: var(--kx-text); font-weight: 500; }
.trail-stage {
  font-size: 10px;
  font-weight: 700;
  padding: 0 4px;
  border-radius: 3px;
  color: var(--kx-primary);
  border: 1px solid var(--kx-primary);
  white-space: nowrap;
}
@media (max-width: 640px) {
  .trail-note { display: none; }
  .trail-request-header { flex-wrap: wrap; }
  .trail-status { margin-left: 0; }
}
</style>

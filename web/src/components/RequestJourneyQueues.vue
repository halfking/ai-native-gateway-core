<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Close, RefreshRight } from '@element-plus/icons-vue'
import { credentialDisplayName as credentialLabelById, useCredentialLabels } from '../composables/useCredentialLabels'
import {
  getRequestJourney,
  getRequestJourneyQueues,
  type ModelRequestFIFOSnapshot,
  type NodeRequestFIFOSnapshot,
  type RequestIngressSnapshot,
  type RequestJourney,
  type RequestJourneyQueueView,
  type RequestJourneyQueuesPayload,
  type RequestJourneyQueuesResponse,
  type RequestJourneySnapshot,
  type TotalRequestFIFOSnapshot,
} from '../api/request-journeys'
import { isSuperAdmin, store } from '../store'
import RoutingAttemptsTimeline from './RoutingAttemptsTimeline.vue'

const { t, locale } = useI18n()
// 2026-08-23 凭据显示：订阅标签缓存 revision，让异步加载完成后
// 节点分组标题的 credentialLabelById 结果自动刷新。
const { labelRevision } = useCredentialLabels()
function nodeLabel(id: number): string {
  void labelRevision.value
  return credentialLabelById(id)
}
const DEFAULT_CAPACITY = 100

interface QueueGroup {
  key: string
  label: string
  capacity: number
  requests: RequestJourneySnapshot[]
}

const showDialog = ref(false)
const hasLoaded = ref(false)
const activeView = ref<RequestJourneyQueueView>('total')

const payloads = ref<Partial<Record<RequestJourneyQueueView, RequestJourneyQueuesPayload>>>({})
const loadingView = ref<RequestJourneyQueueView | null>(null)
const viewErrors = ref<Partial<Record<RequestJourneyQueueView, string>>>({})
const selectedRequestId = ref<string | null>(null)
const journey = ref<RequestJourney | null>(null)
const journeyLoading = ref(false)
const journeyError = ref('')

function isEnvelope(payload: RequestJourneyQueuesPayload): payload is RequestJourneyQueuesResponse {
  return !Array.isArray(payload) && 'view' in payload
}

function ingressAsJourneySnapshot(snapshot: RequestIngressSnapshot): RequestJourneySnapshot {
  const terminal = snapshot.status !== 'arrived'
  // 保留入站快照上的 tenant_id（如有），便于 scope=all 视图下按租户过滤可跳详情。
  // IngressSnapshot 接口未声明 tenant_id（匿名入站常见），因此可选携带。
  const ingressTenant = (snapshot as RequestIngressSnapshot & { tenant_id?: string }).tenant_id
  return {
    request_id: snapshot.request_id,
    gateway_instance_id: snapshot.gateway_instance_id,
    tenant_id: ingressTenant,
    current_stage: terminal ? 'terminal' : 'received',
    outcome: snapshot.status === 'succeeded'
      ? 'success'
      : snapshot.status === 'failed'
        ? 'failure'
        : snapshot.status === 'canceled'
          ? 'canceled'
          : undefined,
    error_kind: snapshot.error_kind,
    http_status: snapshot.http_status,
    started_at: snapshot.arrived_at,
    updated_at: snapshot.updated_at,
  }
}

function totalSnapshot(payload: RequestJourneyQueuesPayload | undefined): TotalRequestFIFOSnapshot | null {
  if (!payload || Array.isArray(payload)) return null
  if (isEnvelope(payload)) {
    const snapshot = payload.total_snapshot
    if (!snapshot) return null
    if (payload.scope === 'all') {
      return {
        capacity: snapshot.capacity,
        requests: (snapshot.requests as RequestIngressSnapshot[]).map(ingressAsJourneySnapshot),
      }
    }
    return snapshot as TotalRequestFIFOSnapshot
  }
  return payload
}

function modelSnapshots(payload: RequestJourneyQueuesPayload | undefined): ModelRequestFIFOSnapshot[] {
  if (!payload) return []
  if (Array.isArray(payload)) return payload as ModelRequestFIFOSnapshot[]
  return isEnvelope(payload) ? (payload.model_snapshots ?? []) : []
}

function nodeSnapshots(payload: RequestJourneyQueuesPayload | undefined): NodeRequestFIFOSnapshot[] {
  if (!payload) return []
  if (Array.isArray(payload)) return payload as NodeRequestFIFOSnapshot[]
  return isEnvelope(payload) ? (payload.node_snapshots ?? []) : []
}

const groups = computed<QueueGroup[]>(() => {
  const payload = payloads.value[activeView.value]
  if (activeView.value === 'total') {
    const snapshot = totalSnapshot(payload)
    return snapshot ? [{
      key: 'total',
      label: t('requestJourneys.totalGroup'),
      capacity: snapshot.capacity || DEFAULT_CAPACITY,
      requests: snapshot.requests ?? [],
    }] : []
  }
  if (activeView.value === 'models') {
    return modelSnapshots(payload).map(snapshot => ({
      key: `model:${snapshot.model}`,
      label: t('requestJourneys.modelGroup', { model: snapshot.model }),
      capacity: snapshot.capacity || DEFAULT_CAPACITY,
      requests: snapshot.requests ?? [],
    }))
  }
  return nodeSnapshots(payload).map(snapshot => ({
    key: `node:${snapshot.model}:${snapshot.provider_id ?? 0}:${snapshot.credential_id}`,
    // 2026-08-23 凭据显示：用共享标签缓存替代原始 ID；缓存未命中时仍显示
    // 「凭据 #ID」便于排查（credentialLabelById 已包含 fallback）。
    label: t('requestJourneys.nodeGroup', {
      model: snapshot.model,
      provider: snapshot.provider_id ?? '—',
      node: nodeLabel(snapshot.credential_id),
    }),
    capacity: snapshot.capacity || DEFAULT_CAPACITY,
    requests: snapshot.requests ?? [],
  }))
})

const isLoading = computed(() => loadingView.value === activeView.value)
const activeError = computed(() => viewErrors.value[activeView.value] ?? '')
const hasRequests = computed(() => groups.value.some(group => group.requests.length > 0))
const queueObservationDegraded = computed(() => {
  const payload = payloads.value[activeView.value]
  if (payload && isEnvelope(payload) && payload.observation_status === 'observation_degraded') return true
  return groups.value.some(group =>
    group.requests.some(request => request.observation_status === 'observation_degraded'),
  )
})

function canOpenDetailForRequest(request: RequestJourneySnapshot | RequestIngressSnapshot): boolean {
  const payload = payloads.value[activeView.value]
  if (!payload || !isEnvelope(payload) || payload.scope !== 'all') return true
  // request-journeys 携带 tenant_id；scope=all 视图下仅匹配自身租户允许跳详情。
  // 缺 tenant_id 的入站快照（匿名）按不可跳处理，避免跨租户细节泄露。
  const userTenant = store.userInfo?.tenant_id
  if (!userTenant) return false
  const reqTenant = (request as { tenant_id?: string }).tenant_id
  return Boolean(reqTenant) && reqTenant === userTenant
}

async function loadView(view: RequestJourneyQueueView, force = false) {
  if (!force && payloads.value[view]) return
  hasLoaded.value = true
  loadingView.value = view
  viewErrors.value[view] = ''
  try {
    payloads.value[view] = await getRequestJourneyQueues(view, view === 'total' && isSuperAdmin() ? 'all' : undefined)
  } catch (error) {
    viewErrors.value[view] = error instanceof Error ? error.message : String(error)
  } finally {
    if (loadingView.value === view) loadingView.value = null
  }
}

async function selectView(view: RequestJourneyQueueView) {
  activeView.value = view
  await loadView(view)
}

async function openJourney(requestId: string) {
  selectedRequestId.value = requestId
  journey.value = null
  journeyError.value = ''
  journeyLoading.value = true
  try {
    journey.value = await getRequestJourney(requestId)
  } catch (error) {
    journeyError.value = error instanceof Error ? error.message : String(error)
  } finally {
    journeyLoading.value = false
  }
}

function closeJourney() {
  selectedRequestId.value = null
  journey.value = null
  journeyError.value = ''
}

function statusOf(snapshot: RequestJourneySnapshot): string {
  if (snapshot.outcome === 'success' || snapshot.last_event_type === 'attempt_succeeded' || snapshot.last_event_type === 'request_succeeded') return 'success'
  if (snapshot.outcome === 'failure' || snapshot.last_event_type === 'attempt_failed' || snapshot.last_event_type === 'request_failed') return 'failed'
  if (snapshot.outcome === 'canceled' || snapshot.last_event_type === 'request_canceled') return 'canceled'
  if (snapshot.last_event_type === 'first_byte' || snapshot.current_stage === 'streaming') return 'first_byte'
  if (snapshot.last_event_type === 'attempt_started' || snapshot.current_stage === 'upstream') return 'forwarding'
  return 'queued'
}

function formatTime(value: string | undefined): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale.value, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(date)
}

function openDialog() {
  showDialog.value = true
  if (!hasLoaded.value) void loadView('total')
}

function closeDialog() {
  showDialog.value = false
  closeJourney()
}
</script>

<template>
  <section class="journey-queues" :aria-label="t('requestJourneys.title')">
    <button type="button" class="journey-trigger" @click="openDialog">
      <span class="journey-trigger-title">{{ t('requestJourneys.title') }}</span>
      <span class="journey-trigger-meta">{{ hasLoaded ? (hasRequests ? t('requestJourneys.activeCount', { count: groups.reduce((sum, group) => sum + group.requests.length, 0) }) : t('requestJourneys.noActiveRequests')) : t('requestJourneys.triggerMeta') }}</span>
      <RefreshRight aria-hidden="true" />
    </button>
  </section>

  <Teleport to="body">
    <div v-if="showDialog" class="journey-modal-mask" @click.self="closeDialog">
      <section class="journey-modal" role="dialog" aria-modal="true" :aria-label="t('requestJourneys.title')">
        <div class="journey-header">
          <div><h3>{{ t('requestJourneys.title') }}</h3><p class="journey-modal-sub">{{ t('requestJourneys.modalSubtitle') }}</p></div>
          <div class="journey-header-actions"><button type="button" class="icon-button" :title="t('requestJourneys.retry')" :aria-label="t('requestJourneys.retry')" @click="loadView(activeView, true)"><RefreshRight aria-hidden="true" /></button><button type="button" class="icon-button" aria-label="关闭" title="关闭" @click="closeDialog"><Close aria-hidden="true" /></button></div>
        </div>

    <div class="journey-tabs" role="tablist" :aria-label="t('requestJourneys.title')">
      <button
        v-for="view in (['total', 'models', 'nodes'] as const)"
        :key="view"
        type="button"
        role="tab"
        class="journey-tab"
        :class="{ 'journey-tab--active': activeView === view }"
        :aria-selected="activeView === view"
        :data-view="view"
        @click="selectView(view)"
      >
        {{ t(`requestJourneys.tabs.${view}`) }}
      </button>
    </div>

    <div
      v-if="!isLoading && !activeError && queueObservationDegraded"
      class="degraded-notice"
      role="status"
    >
      <strong>{{ t('requestJourneys.observationDegraded') }}</strong>
      <span>{{ t('requestJourneys.observationDegradedHint') }}</span>
    </div>

    <div v-if="isLoading" class="journey-state">{{ t('requestJourneys.loading') }}</div>
    <div v-else-if="activeError" class="journey-state journey-state--error" role="alert">
      <span>{{ t('requestJourneys.unavailable') }}: {{ activeError }}</span>
      <button type="button" class="text-button" @click="loadView(activeView, true)">{{ t('requestJourneys.retry') }}</button>
    </div>
    <div v-else-if="!hasRequests" class="journey-state">{{ t('requestJourneys.empty') }}</div>

    <div v-else class="queue-groups">
      <section v-for="group in groups" :key="group.key" class="queue-group">
        <div class="queue-group-header">
          <strong>{{ group.label }}</strong>
          <span data-testid="queue-window">{{ t('requestJourneys.queueWindow', { used: group.requests.length, capacity: group.capacity }) }}</span>
        </div>
        <ol class="queue-list">
          <li v-for="(request, index) in group.requests" :key="request.request_id">
            <button
              type="button"
              class="queue-row"
              data-testid="journey-queue-row"
              :aria-label="canOpenDetailForRequest(request) ? t('requestJourneys.openDetail', { requestId: request.request_id }) : request.request_id"
              :disabled="!canOpenDetailForRequest(request)"
              @click="canOpenDetailForRequest(request) && openJourney(request.request_id)"
            >
              <span class="queue-position">{{ t('requestJourneys.position', { position: index + 1 }) }}</span>
              <span class="queue-request">
                <strong>{{ request.request_id }}</strong>
                <span class="queue-models">
                  <span v-if="request.requested_model">{{ t('requestJourneys.requestedModel', { model: request.requested_model }) }}</span>
                  <span v-if="request.resolved_model">{{ t('requestJourneys.resolvedModel', { model: request.resolved_model }) }}</span>
                </span>
              </span>
              <span v-if="request.attempt" class="queue-attempt">{{ t('requestJourneys.attempt', { number: request.attempt.attempt_no }) }}</span>
              <span class="queue-status" :class="`queue-status--${statusOf(request)}`">{{ t(`requestJourneys.status.${statusOf(request)}`) }}</span>
              <span class="queue-updated">{{ t('requestJourneys.updatedAt', { time: formatTime(request.updated_at) }) }}</span>
            </button>
          </li>
        </ol>
      </section>
    </div>

    <section v-if="selectedRequestId" class="journey-detail" aria-live="polite">
      <div class="journey-detail-header">
        <h4>{{ t('requestJourneys.detailTitle', { requestId: selectedRequestId }) }}</h4>
        <button
          type="button"
          class="icon-button"
          :title="t('requestJourneys.closeDetail')"
          :aria-label="t('requestJourneys.closeDetail')"
          @click="closeJourney"
        >
          <Close aria-hidden="true" />
        </button>
      </div>
      <div v-if="journeyLoading" class="journey-state">{{ t('requestJourneys.detailLoading') }}</div>
      <div v-else-if="journeyError" class="journey-state journey-state--error" role="alert">
        {{ t('requestJourneys.detailError') }}: {{ journeyError }}
      </div>
      <template v-else-if="journey">
        <div v-if="journey.observation_status === 'observation_degraded'" class="degraded-notice" role="status">
          <strong>{{ t('requestJourneys.observationDegraded') }}</strong>
          <span>{{ t('requestJourneys.observationDegradedHint') }}</span>
        </div>
        <RoutingAttemptsTimeline :journey-events="journey.events" />
      </template>
    </section>
      </section>
    </div>
  </Teleport>
</template>

<style scoped>
.journey-queues { min-width: 0; }
.journey-trigger { width:100%; display:flex; align-items:center; gap:10px; padding:9px 12px; border:1px solid var(--kx-border); border-radius:var(--kx-radius-sm, 6px); background:var(--kx-surface); color:var(--kx-text); cursor:pointer; text-align:left; }
.journey-trigger:hover { border-color:var(--kx-primary); }
.journey-trigger-title { font-weight:600; font-size:13px; }
.journey-trigger-meta { color:var(--kx-text-secondary); font-size:11px; margin-left:auto; }
.journey-trigger svg { width:15px; height:15px; color:var(--kx-text-secondary); }
.journey-modal-mask { position:fixed; inset:0; z-index:2900; background:rgba(0,0,0,.38); display:flex; justify-content:flex-end; }
.journey-modal { width:min(760px, 96vw); height:100vh; overflow:auto; background:var(--kx-surface); color:var(--kx-text); box-shadow:-10px 0 30px rgba(0,0,0,.24); padding:18px 20px 28px; box-sizing:border-box; }
.journey-header-actions { display:flex; gap:6px; }
.journey-modal-sub { margin:4px 0 0; color:var(--kx-text-secondary); font-size:11px; }
.journey-header,
.queue-group-header,
.journey-detail-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.journey-header h3,
.journey-detail-header h4 {
  margin: 0;
  min-width: 0;
  color: var(--kx-text);
  font-size: 13px;
  overflow-wrap: anywhere;
}
.icon-button {
  display: inline-grid;
  place-items: center;
  flex: 0 0 28px;
  width: 28px;
  height: 28px;
  padding: 5px;
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm, 6px);
  background: var(--kx-surface);
  color: var(--kx-text-secondary);
  cursor: pointer;
}
.icon-button svg { width: 16px; height: 16px; }
.icon-button:hover { color: var(--kx-primary); border-color: var(--kx-primary); }
.journey-tabs {
  display: flex;
  gap: 2px;
  margin-top: 8px;
  border-bottom: 1px solid var(--kx-border);
  overflow-x: auto;
  scrollbar-width: thin;
}
.journey-tab {
  flex: 0 0 auto;
  min-height: 36px;
  padding: 7px 12px;
  border: 0;
  border-bottom: 2px solid transparent;
  background: transparent;
  color: var(--kx-text-secondary);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}
.journey-tab--active {
  border-bottom-color: var(--kx-primary);
  color: var(--kx-primary);
  font-weight: 600;
}
.journey-state {
  padding: 18px 8px;
  color: var(--kx-text-secondary);
  font-size: 12px;
  text-align: center;
}
.journey-state--error { color: var(--kx-danger); }
.text-button {
  margin-left: 8px;
  border: 0;
  background: transparent;
  color: var(--kx-primary);
  cursor: pointer;
}
.queue-groups { display: grid; gap: 14px; margin-top: 10px; }
.queue-group { min-width: 0; }
.queue-group-header { margin-bottom: 4px; color: var(--kx-text-secondary); font-size: 11px; }
.queue-group-header strong { min-width: 0; color: var(--kx-text); font-size: 12px; overflow-wrap: anywhere; }
.queue-list { margin: 0; padding: 0; list-style: none; border-top: 1px solid var(--kx-border); }
.queue-list li { border-bottom: 1px solid var(--kx-border); }
.queue-row {
  display: grid;
  grid-template-columns: 36px minmax(150px, 1fr) minmax(80px, auto) minmax(72px, auto) minmax(110px, auto);
  align-items: center;
  gap: 8px;
  width: 100%;
  min-width: 0;
  padding: 8px 4px;
  border: 0;
  background: transparent;
  color: var(--kx-text);
  text-align: left;
  cursor: pointer;
}
.queue-row:hover { background: var(--kx-bg-elevated); }
.queue-position,
.queue-attempt,
.queue-updated { color: var(--kx-text-secondary); font-size: 11px; }
.queue-position { font-variant-numeric: tabular-nums; }
.queue-request { display: flex; min-width: 0; flex-direction: column; gap: 2px; }
.queue-request strong { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
.queue-models { display: flex; flex-wrap: wrap; gap: 2px 10px; color: var(--kx-text-secondary); font-size: 11px; }
.queue-status {
  justify-self: start;
  padding: 2px 7px;
  border: 1px solid var(--kx-border);
  border-radius: var(--kx-radius-sm, 6px);
  color: var(--kx-text-secondary);
  font-size: 11px;
  white-space: nowrap;
}
.queue-status--forwarding,
.queue-status--first_byte { border-color: var(--kx-primary); color: var(--kx-primary); }
.queue-status--success { border-color: var(--kx-success); color: var(--kx-success); }
.queue-status--failed { border-color: var(--kx-danger); color: var(--kx-danger); }
.queue-status--canceled { border-color: var(--kx-warning); color: var(--kx-warning); }
.queue-updated { justify-self: end; white-space: nowrap; }
.journey-detail { margin-top: 14px; padding-top: 12px; border-top: 1px solid var(--kx-border); min-width: 0; }
.degraded-notice {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 8px;
  margin-top: 8px;
  padding: 8px 10px;
  border-left: 3px solid var(--kx-warning);
  background: color-mix(in srgb, var(--kx-warning) 10%, var(--kx-surface));
  color: var(--kx-text-secondary);
  font-size: 12px;
}
.degraded-notice strong { color: var(--kx-warning); }
@media (max-width: 720px) {
  .queue-row { grid-template-columns: 32px minmax(0, 1fr) auto; }
  .queue-attempt { grid-column: 2; }
  .queue-status { grid-column: 3; grid-row: 1 / span 2; }
  .queue-updated { grid-column: 2 / -1; justify-self: start; white-space: normal; }
  .queue-request strong { white-space: normal; overflow-wrap: anywhere; }
}
</style>

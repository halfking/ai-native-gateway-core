<script setup lang="ts">
// RequestRegistryView.vue — T9 mock-stage 三态注册表视图
//
// 三态：pending / in_flight / completed（来自 request-journeys queues API 的
// additive lifecycle_state 字段，对齐后端 LifecycleState）。卡片形态沿用
// probe/ProbeTriStateQueue.vue：
//   - SSE 主：liveStreamStore 单例（request_lifecycle 推送，挂在
//     store.actions Map 上）——实时增量
//   - REST 校准：getRequestJourneyQueues(view='total')（三段整体替换为权威
//     快照 + 保留 SSE 先行看到、API 尚未落库的增量）
//   - 页面不可见：v-if 卸下列表 + 暂停推进 in_flight 计时（沿用可见性门控）
//
// 不重复后端 probeCompletedWindow：completed cap 走 200，复用 ProbeTriStateQueue
// 的 trim 语义；前端 cap 与后端对齐（producer 写 200 + 旁路 SSE）。
//
// 点击卡片 → router.push 到 RequestJourneyDetailView（仅 own-tenant 卡片可
// 跳；scope=all（super_admin 全局入站）的卡片按 RequestJourneyQueues 同款
// 规则禁用跳转，避免暴露跨租户详情）。
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { getRequestJourneyQueues } from '../api/request-journeys'
import type {
  RequestJourneySnapshot,
  RequestJourneyQueuesPayload,
  RequestJourneyQueueView,
} from '../api/request-journeys'
import {
  acquireLiveStream,
  liveStreamState,
  type ActionEvent,
} from '../composables/liveStreamStore'

const router = useRouter()
const { t, locale } = useI18n()
const MAX_COMPLETED = 200
const POLL_MS = 15_000

type CardStatus = 'pending' | 'in_flight' | 'completed'

interface RegistryCard {
  key: string
  dedup: string
  source: 'api' | 'sse'
  request_id: string
  tenant_id?: string
  gateway_instance_id?: string
  requested_model?: string
  resolved_model?: string
  status: CardStatus
  attempt?: number
  outcome?: 'success' | 'failure' | 'canceled'
  error_kind?: string
  http_status?: number
  /** in_flight 实时计时基点（unix ms） */
  started_ms?: number
  /** next_retry_at_ms（仅 pending 重臂行携带） */
  retry_at_ms?: number
  updated_at?: string
  finished_at?: string
}

// ── 状态 ─────────────────────────────────────────────────────
const cards = ref(new Map<string, RegistryCard>())
const firstLoading = ref(true)
const apiDegraded = ref(false)
const pageHidden = ref(typeof document !== 'undefined' ? document.hidden : false)
const expandedKeys = ref(new Set<string>())
const nowMs = ref(Date.now())
const sseReconnecting = ref(false)
const isSuperAdminView = ref(false)
const searchId = ref('')
const filterText = ref('')

let pollTimer: number | undefined
let tickTimer: number | undefined
let releaseStream: (() => void) | null = null
let reconnectCheckTimer: number | undefined

// ── 工具 ─────────────────────────────────────────────────────
function statusClass(s: CardStatus): string {
  return s === 'pending' ? 'is-warning' : s === 'in_flight' ? 'is-primary' : 'is-muted'
}

function statusLabel(s: CardStatus): string {
  return s === 'pending' ? t('requestRegistry.status.pending') :
         s === 'in_flight' ? t('requestRegistry.status.inFlight') :
         t('requestRegistry.status.completed')
}

function outcomeLabel(o?: 'success' | 'failure' | 'canceled'): string {
  if (!o) return ''
  return o === 'success' ? t('requestRegistry.outcome.success') :
         o === 'failure' ? t('requestRegistry.outcome.failure') :
         t('requestRegistry.outcome.canceled')
}

function fmtClock(ms?: number): string {
  if (!ms) return '—'
  return new Date(ms).toLocaleTimeString(locale.value, { hour12: false })
}

function fmtMs(v?: number): string {
  if (v === undefined || v === null) return '—'
  if (v >= 1000) return (v / 1000).toFixed(2) + 's'
  return v + 'ms'
}

function fmtCountdown(targetMs?: number): string {
  if (!targetMs) return ''
  const delta = targetMs - nowMs.value
  if (delta <= 0) return '已到'
  if (delta < 60_000) return Math.ceil(delta / 1000) + 's 后'
  if (delta < 3_600_000) return Math.ceil(delta / 60_000) + 'm 后'
  return Math.ceil(delta / 3_600_000) + 'h 后'
}

function fmtDateTime(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString(locale.value, { hour12: false })
}

function elapsedOf(c: RegistryCard): string {
  const base = c.started_ms ?? (c.updated_at ? new Date(c.updated_at).getTime() : NaN)
  if (!base || Number.isNaN(base)) return '—'
  const v = Math.max(0, nowMs.value - base)
  if (v < 60_000) return (v / 1000).toFixed(1) + 's'
  return Math.floor(v / 60_000) + 'm' + Math.floor((v % 60_000) / 1000) + 's'
}

function sortTs(c: RegistryCard): number {
  if (c.finished_at) return new Date(c.finished_at).getTime() || 0
  if (c.updated_at) return new Date(c.updated_at).getTime() || 0
  return c.started_ms ?? 0
}

function matchesFilter(c: RegistryCard): boolean {
  const q = filterText.value.trim().toLowerCase()
  if (!q) return true
  return (
    c.request_id.toLowerCase().includes(q) ||
    (c.requested_model ?? '').toLowerCase().includes(q) ||
    (c.resolved_model ?? '').toLowerCase().includes(q)
  )
}

function inferStatus(snap: RequestJourneySnapshot): CardStatus {
  if (snap.lifecycle_state === 'pending' || snap.lifecycle_state === 'in_flight' || snap.lifecycle_state === 'completed') {
    return snap.lifecycle_state
  }
  if (snap.outcome === 'success' || snap.outcome === 'failure' || snap.outcome === 'canceled') return 'completed'
  if (snap.current_stage === 'terminal') return 'completed'
  // 用 last_event_type 兜底（前端 caps：first_byte/attempt_started 视为 in_flight）
  if (snap.last_event_type === 'first_byte' || snap.last_event_type === 'attempt_started') return 'in_flight'
  if (snap.last_event_type?.startsWith('request_') && (snap.last_event_type.endsWith('succeeded') || snap.last_event_type.endsWith('failed') || snap.last_event_type.endsWith('canceled'))) return 'completed'
  return 'pending'
}

function capCompleted(map: Map<string, RegistryCard>) {
  const done = [...map.values()].filter((c) => c.status === 'completed').sort((a, b) => sortTs(b) - sortTs(a))
  if (done.length > MAX_COMPLETED) {
    for (const c of done.slice(MAX_COMPLETED)) map.delete(c.key)
  }
}

// ── API → Card ────────────────────────────────────────────────
function snapshotToCard(snap: RequestJourneySnapshot): RegistryCard {
  const status = inferStatus(snap)
  return {
    key: `api-${snap.request_id}`,
    dedup: snap.request_id,
    source: 'api',
    request_id: snap.request_id,
    tenant_id: snap.tenant_id,
    gateway_instance_id: snap.gateway_instance_id,
    requested_model: snap.requested_model,
    resolved_model: snap.resolved_model,
    status,
    attempt: snap.attempt?.attempt_no,
    outcome: snap.outcome,
    error_kind: snap.error_kind,
    http_status: snap.http_status,
    started_ms: status === 'in_flight' && snap.updated_at ? new Date(snap.updated_at).getTime() : undefined,
    retry_at_ms: snap.retry_at ? new Date(snap.retry_at).getTime() : undefined,
    finished_at: status === 'completed' && snap.updated_at ? snap.updated_at : undefined,
    updated_at: snap.updated_at,
  }
}

function actionToCard(action: ActionEvent): RegistryCard | null {
  if (!action.request_id) return null
  const act = action.action
  if (!act) return null
  // 跳过路由决策类（不算 lifecycle 起点）
  let status: CardStatus
  if (act === 'arrive') status = 'pending'
  else if (act === 'route_resolved' || act === 'model_enqueued' || act === 'credential_selected' || act === 'node_enqueued' || act === 'node_selected' || act === 'upstream_request') status = 'in_flight'
  else if (act === 'reply' || act === 'no_route') status = 'completed'
  else return null
  const ts = action.ts ? new Date(action.ts).getTime() : Date.now()
  return {
    key: `sse-${action.request_id}`,
    dedup: action.request_id,
    source: 'sse',
    request_id: action.request_id,
    status,
    started_ms: status === 'in_flight' ? ts : undefined,
    finished_at: status === 'completed' ? action.ts : undefined,
    updated_at: action.ts,
  }
}

function upsertCard(next: RegistryCard) {
  const prev = cards.value.get(next.key)
  if (prev) {
    cards.value.set(next.key, {
      ...next,
      requested_model: next.requested_model ?? prev.requested_model,
      resolved_model: next.resolved_model ?? prev.resolved_model,
      outcome: next.outcome ?? prev.outcome,
      attempt: next.attempt ?? prev.attempt,
      error_kind: next.error_kind ?? prev.error_kind,
      http_status: next.http_status ?? prev.http_status,
    })
  } else {
    cards.value.set(next.key, next)
  }
  capCompleted(cards.value)
}

// ── 数据加载 ─────────────────────────────────────────────────
async function refreshFromApi() {
  try {
    const payload: RequestJourneyQueuesPayload = await getRequestJourneyQueues('total' as RequestJourneyQueueView)
    apiDegraded.value = false
    const next = new Map<string, RegistryCard>()
    const nextDedups = new Set<string>()
    const requests: RequestJourneySnapshot[] = []
    if (payload && !Array.isArray(payload) && 'view' in payload) {
      const total = payload.total_snapshot
      if (total) requests.push(...((total.requests ?? []) as RequestJourneySnapshot[]))
      if (payload.scope === 'all') isSuperAdminView.value = true
      else isSuperAdminView.value = false
    } else if (payload && !Array.isArray(payload) && (payload as { requests?: RequestJourneySnapshot[] }).requests) {
      requests.push(...((payload as { requests: RequestJourneySnapshot[] }).requests))
    }
    for (const r of requests) {
      const card = snapshotToCard(r)
      next.set(card.key, card)
      nextDedups.add(card.dedup)
    }
    // 保留 SSE 增量
    for (const c of cards.value.values()) {
      if (c.source === 'sse' && !nextDedups.has(c.dedup) && c.status !== 'completed') {
        next.set(c.key, c)
      }
    }
    cards.value = next
    capCompleted(cards.value)
  } catch {
    apiDegraded.value = true
  } finally {
    firstLoading.value = false
  }
}

function startPoll() {
  stopPoll()
  pollTimer = window.setInterval(() => {
    if (typeof document !== 'undefined' && document.hidden) return
    void refreshFromApi()
  }, POLL_MS)
}

function stopPoll() {
  if (pollTimer) clearInterval(pollTimer)
  pollTimer = undefined
}

function startTick() {
  stopTick()
  tickTimer = window.setInterval(() => {
    if (typeof document !== 'undefined' && document.hidden) return
    nowMs.value = Date.now()
  }, 1000)
}

function stopTick() {
  if (tickTimer) clearInterval(tickTimer)
  tickTimer = undefined
}

function startReconnectCheck() {
  stopReconnectCheck()
  reconnectCheckTimer = window.setInterval(() => {
    // 简化：通过 store.connection 暴露的状态在 liveStreamStore 中；这里仅做粗粒度标记。
    sseReconnecting.value = false
  }, 2_000)
}

function stopReconnectCheck() {
  if (reconnectCheckTimer) clearInterval(reconnectCheckTimer)
  reconnectCheckTimer = undefined
}

function onVisibilityChange() {
  const hidden = typeof document !== 'undefined' ? document.hidden : false
  pageHidden.value = hidden
  if (!hidden) {
    nowMs.value = Date.now()
    void refreshFromApi()
  }
}

onMounted(() => {
  void refreshFromApi()
  startPoll()
  startTick()
  startReconnectCheck()
  document.addEventListener('visibilitychange', onVisibilityChange)
  releaseStream = acquireLiveStream()
})

onUnmounted(() => {
  stopPoll()
  stopTick()
  stopReconnectCheck()
  document.removeEventListener('visibilitychange', onVisibilityChange)
  if (releaseStream) { releaseStream(); releaseStream = null }
  expandedKeys.value.clear()
})

// SSE 增量：liveStreamStore 单例暴露的 actions Map 按 request_id 索引。
// 监听 → 折叠到 cards（不引入第二状态机）。
watch(() => liveStreamState.actions, () => {
  const seen = new Set<string>()
  for (const [rid, list] of liveStreamState.actions) {
    if (!list.length || seen.has(rid)) continue
    seen.add(rid)
    const last = list[list.length - 1]
    const card = actionToCard(last)
    if (card) upsertCard(card)
  }
}, { deep: false })

// ── 视图投影 ─────────────────────────────────────────────────
const pendingCards = computed(() =>
  [...cards.value.values()]
    .filter((c) => c.status === 'pending' && matchesFilter(c))
    .sort((a, b) => (a.retry_at_ms ?? Number.MAX_SAFE_INTEGER) - (b.retry_at_ms ?? Number.MAX_SAFE_INTEGER)),
)

const inFlightCards = computed(() =>
  [...cards.value.values()]
    .filter((c) => c.status === 'in_flight' && matchesFilter(c))
    .sort((a, b) => (a.started_ms ?? sortTs(a)) - (b.started_ms ?? sortTs(b))),
)

const completedCards = computed(() =>
  [...cards.value.values()]
    .filter((c) => c.status === 'completed' && matchesFilter(c))
    .sort((a, b) => sortTs(b) - sortTs(a)),
)

function canOpen(card: RegistryCard): boolean {
  // scope=all 卡片（super_admin 全局入站）禁止跳详情 — 与 RequestJourneyQueues
  // canOpenDetails 对齐；该规则写在这里是因为 request-journeys/queues API
  // 在 super_admin 全局入站时整张表都标记 scope=all，前端不能逐卡判定。
  return !isSuperAdminView.value
}

function toggleExpand(key: string) {
  const s = expandedKeys.value
  if (s.has(key)) s.delete(key)
  else s.add(key)
}

function openDetail(card: RegistryCard) {
  if (!canOpen(card)) return
  router.push({ name: 'request-journey-detail', params: { requestId: card.request_id } })
}

function openSearchJourney() {
  const id = searchId.value.trim()
  if (!id) return
  router.push({ name: 'request-journey-detail', params: { requestId: id } })
}
</script>

<template>
  <section class="request-registry" data-testid="request-registry">
    <header class="registry-head">
      <h2>{{ t('requestRegistry.title') }}</h2>
      <p class="registry-sub">{{ t('requestRegistry.subtitle') }}</p>
    </header>

    <div class="registry-search-row" data-testid="reg-search-row">
      <input
        v-model="searchId"
        type="search"
        class="registry-search-input"
        :placeholder="t('requestRegistry.searchPlaceholder')"
        data-testid="reg-search-input"
        @keydown.enter.prevent="openSearchJourney()"
      />
      <button
        type="button"
        class="registry-search-btn"
        :disabled="!searchId.trim()"
        data-testid="reg-search-btn"
        @click="openSearchJourney()"
      >
        {{ t('requestRegistry.search') }}
      </button>
      <input
        v-model="filterText"
        type="search"
        class="registry-filter-input"
        :placeholder="t('requestRegistry.filterPlaceholder')"
        data-testid="reg-filter-input"
      />
    </div>

    <div class="registry-status-row">
      <span class="reg-chip" :class="sseReconnecting ? 'is-warning' : 'is-success'" data-testid="sse-state">
        {{ sseReconnecting ? t('requestRegistry.sseReconnecting') : t('requestRegistry.sseConnected') }}
      </span>
      <span v-if="apiDegraded" class="reg-chip is-danger" data-testid="api-degraded">
        {{ t('requestRegistry.apiDegraded') }}
      </span>
      <span v-if="isSuperAdminView" class="reg-chip is-muted" data-testid="super-admin-scope">
        {{ t('requestRegistry.scopeAll') }}
      </span>
      <span class="reg-chip is-muted">{{ t('requestRegistry.historyNote') }}</span>
    </div>

    <template v-if="!pageHidden">
      <div v-if="firstLoading && cards.size === 0" class="registry-skeleton" data-testid="registry-skeleton">
        <div v-for="i in 3" :key="i" class="registry-skeleton-card"></div>
      </div>
      <template v-else>
        <section class="reg-section" data-testid="reg-pending">
          <header class="reg-section-head">
            <h3>{{ t('requestRegistry.sections.pending') }}</h3>
            <span class="reg-count">{{ pendingCards.length }}</span>
          </header>
          <div v-if="pendingCards.length" class="reg-cards">
            <TransitionGroup name="reg-in">
              <button
                v-for="c in pendingCards"
                :key="c.key"
                type="button"
                class="reg-card reg-card--pending"
                :class="statusClass(c.status)"
                :disabled="!canOpen(c)"
                @click="openDetail(c)"
                data-testid="reg-pending-card"
              >
                <span class="reg-card__id">{{ c.request_id }}</span>
                <span v-if="c.requested_model" class="reg-card__model">{{ c.requested_model }}</span>
                <span v-if="c.retry_at_ms" class="reg-card__eta">
                  {{ t('requestRegistry.retryAt', { time: fmtClock(c.retry_at_ms), delta: fmtCountdown(c.retry_at_ms) }) }}
                </span>
              </button>
            </TransitionGroup>
          </div>
          <div v-else class="reg-empty">{{ t('requestRegistry.empty.pending') }}</div>
        </section>

        <section class="reg-section" data-testid="reg-inflight">
          <header class="reg-section-head">
            <h3>{{ t('requestRegistry.sections.inFlight') }}</h3>
            <span class="reg-count">{{ inFlightCards.length }}</span>
          </header>
          <div v-if="inFlightCards.length" class="reg-cards">
            <TransitionGroup name="reg-in">
              <button
                v-for="c in inFlightCards"
                :key="c.key"
                type="button"
                class="reg-card reg-card--inflight"
                :class="statusClass(c.status)"
                :disabled="!canOpen(c)"
                @click="openDetail(c)"
                data-testid="reg-inflight-card"
              >
                <span class="reg-card__id">{{ c.request_id }}</span>
                <span v-if="c.resolved_model" class="reg-card__model">{{ c.resolved_model }}</span>
                <span class="reg-card__elapsed">{{ elapsedOf(c) }}</span>
              </button>
            </TransitionGroup>
          </div>
          <div v-else class="reg-empty">{{ t('requestRegistry.empty.inFlight') }}</div>
        </section>

        <section class="reg-section" data-testid="reg-completed">
          <header class="reg-section-head">
            <h3>{{ t('requestRegistry.sections.completed') }}</h3>
            <span class="reg-count">{{ completedCards.length }}<span class="reg-count-cap">/200</span></span>
          </header>
          <div v-if="completedCards.length" class="reg-cards reg-cards--large">
            <TransitionGroup name="reg-in">
              <div
                v-for="c in completedCards"
                :key="c.key"
                class="reg-card reg-card--completed"
                :class="statusClass(c.status)"
                :data-expanded="expandedKeys.has(c.key) ? 'true' : 'false'"
                data-testid="reg-completed-card"
              >
                <div class="reg-card__summary" @click="toggleExpand(c.key)">
                  <span class="reg-badge" :data-outcome="c.outcome">{{ outcomeLabel(c.outcome) }}</span>
                  <span class="reg-card__id" @click.stop="canOpen(c) && openDetail(c)">
                    <button v-if="canOpen(c)" type="button" class="reg-link-btn">{{ c.request_id }}</button>
                    <span v-else>{{ c.request_id }}</span>
                  </span>
                  <span v-if="c.resolved_model" class="reg-card__model">{{ c.resolved_model }}</span>
                  <span class="reg-card__expand">{{ expandedKeys.has(c.key) ? '▾' : '▸' }}</span>
                </div>
                <div v-if="expandedKeys.has(c.key)" class="reg-card__detail">
                  <div class="reg-detail-row"><span class="reg-detail-label">{{ t('requestRegistry.detail.requestId') }}</span><span>{{ c.request_id }}</span></div>
                  <div v-if="c.outcome" class="reg-detail-row"><span class="reg-detail-label">{{ t('requestRegistry.detail.outcome') }}</span><span>{{ outcomeLabel(c.outcome) }}</span></div>
                  <div v-if="c.error_kind" class="reg-detail-row"><span class="reg-detail-label">{{ t('requestRegistry.detail.error') }}</span><span>{{ c.error_kind }}</span></div>
                  <div v-if="c.http_status" class="reg-detail-row"><span class="reg-detail-label">{{ t('requestRegistry.detail.http') }}</span><span>{{ c.http_status }}</span></div>
                  <div v-if="c.attempt" class="reg-detail-row"><span class="reg-detail-label">{{ t('requestRegistry.detail.attempt') }}</span><span>{{ c.attempt }}</span></div>
                  <div v-if="c.finished_at" class="reg-detail-row"><span class="reg-detail-label">{{ t('requestRegistry.detail.finishedAt') }}</span><span>{{ fmtDateTime(c.finished_at) }}</span></div>
                </div>
              </div>
            </TransitionGroup>
          </div>
          <div v-else class="reg-empty">{{ t('requestRegistry.empty.completed') }}</div>
        </section>
      </template>
    </template>
  </section>
</template>

<style scoped>
.request-registry {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.registry-head h2 {
  margin: 0;
  font-size: 16px;
  color: var(--kx-text);
}

.registry-sub {
  margin: 4px 0 0;
  color: var(--kx-muted);
  font-size: 12px;
}

.registry-search-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.registry-search-input,
.registry-filter-input {
  flex: 1;
  min-width: 180px;
  padding: 6px 10px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  font-size: 12px;
  background: var(--kx-surface);
  color: var(--kx-text);
}

.registry-search-btn {
  padding: 6px 12px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-primary);
  color: #fff;
  font-size: 12px;
  cursor: pointer;
}

.registry-search-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.registry-status-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.reg-chip {
  display: inline-flex;
  align-items: center;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 12px;
  border: 1px solid transparent;
}

.reg-chip.is-success { color: var(--kx-success); background: var(--kx-success-soft); }
.reg-chip.is-warning { color: var(--kx-warning); background: var(--kx-warning-soft); }
.reg-chip.is-danger { color: var(--kx-danger); background: var(--kx-danger-soft); }
.reg-chip.is-muted { color: var(--kx-muted); background: var(--kx-bg-accent); }

.reg-section {
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  background: var(--kx-surface);
  padding: 10px 12px;
}

.reg-section-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 8px;
}

.reg-section-head h3 {
  margin: 0;
  font-size: 13px;
  color: var(--kx-text);
}

.reg-count {
  font-size: 12px;
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
}

.reg-count-cap { opacity: 0.8; }

.reg-cards {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  position: relative;
}

.reg-cards--large { flex-direction: column; }

.reg-card {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-surface);
  font-size: 12px;
  color: var(--kx-text);
  min-width: 220px;
  text-align: left;
}

.reg-card--inflight { border-color: var(--kx-primary); }
.reg-card--completed.is-warning { border-color: var(--kx-warning); }

.reg-card:disabled { cursor: not-allowed; opacity: 0.7; }
.reg-card:not(:disabled) { cursor: pointer; }
.reg-card:not(:disabled):hover { border-color: var(--kx-primary); }

.reg-card__id {
  font-weight: 600;
  font-family: var(--kx-mono, ui-monospace, monospace);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 220px;
}

.reg-card__model { color: var(--kx-muted); }

.reg-card__elapsed {
  margin-left: auto;
  color: var(--kx-primary);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.reg-card__eta { color: var(--kx-warning); font-variant-numeric: tabular-nums; }

.reg-card__summary {
  display: flex;
  align-items: center;
  gap: 8px;
  cursor: pointer;
  flex-wrap: wrap;
}

.reg-card__expand {
  margin-left: auto;
  color: var(--kx-muted);
}

.reg-link-btn {
  border: 0;
  background: transparent;
  color: var(--kx-primary);
  cursor: pointer;
  font-weight: 600;
  font: inherit;
  font-size: 12px;
  font-weight: 600;
}

.reg-card__detail {
  border-top: 1px solid var(--kx-border);
  padding-top: 6px;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 4px 16px;
}

.reg-detail-row { display: flex; gap: 8px; font-size: 12px; }
.reg-detail-label { color: var(--kx-muted); min-width: 5em; }

.reg-badge {
  padding: 1px 8px;
  border-radius: 4px;
  font-size: 11px;
}

.reg-badge[data-outcome="success"] { color: var(--kx-success); background: var(--kx-success-soft); }
.reg-badge[data-outcome="failure"] { color: var(--kx-danger); background: var(--kx-danger-soft); }
.reg-badge[data-outcome="canceled"] { color: var(--kx-warning); background: var(--kx-warning-soft); }

.reg-empty {
  padding: 12px;
  text-align: center;
  color: var(--kx-muted);
  font-size: 12px;
}

.registry-skeleton {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.registry-skeleton-card {
  height: 56px;
  border-radius: 8px;
  background: var(--kx-bg-accent);
  animation: reg-skeleton-pulse 1.2s ease-in-out infinite;
}

@keyframes reg-skeleton-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.55; }
}

.reg-in-enter-active { transition: transform 0.3s ease, opacity 0.3s ease; }
.reg-in-enter-from { transform: translateX(32px); opacity: 0; }
.reg-in-leave-active { transition: transform 0.2s ease, opacity 0.2s ease; position: absolute; }
.reg-in-leave-to { transform: translateX(-16px); opacity: 0; }
.reg-in-move { transition: transform 0.3s ease; }
</style>
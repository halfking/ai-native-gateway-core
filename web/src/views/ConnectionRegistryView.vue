<script setup lang="ts">
// ConnectionRegistryView.vue — T9 mock-stage 连接注册台视图
//
// 三态视图（沿用 ProbeTriStateQueue 三列 + SSE 主 / REST 校准 模式）：
//   connected / connecting / disconnected（来自 connection-registry mock API）
//
// 复用：
//   - connection-registry.ts fetchConnectionRegistry mock
//   - liveStreamStore 单例（liveStreamState.nodes）作为 SSE 通道更新 in_flight
//
// 可见性门控沿用三态模式：页面 hidden 时不渲染、不轮询；恢复可见时立即刷新。
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  fetchConnectionRegistry,
  type ConnectionRecord,
  type ConnectionRegistryResponse,
  type ConnectionState,
} from '../api/connection-registry'
import { acquireLiveStream, liveStreamState, type LiveNodeStatus } from '../composables/liveStreamStore'

const router = useRouter()
const { t, locale } = useI18n()
const POLL_MS = 15_000

interface ViewCard {
  key: string
  record: ConnectionRecord
  state: ConnectionState
  in_flight: number
  updated_at_ms: number
}

const records = ref<ConnectionRecord[]>([])
const firstLoading = ref(true)
const apiDegraded = ref(false)
const pageHidden = ref(typeof document !== 'undefined' ? document.hidden : false)
const expandedKeys = ref(new Set<string>())
const nowMs = ref(Date.now())

let pollTimer: number | undefined
let tickTimer: number | undefined
let releaseStream: (() => void) | null = null

function stateClass(s: ConnectionState): string {
  return s === 'connected' ? 'is-success' :
         s === 'connecting' ? 'is-warning' :
         'is-danger'
}

function stateLabel(s: ConnectionState): string {
  return s === 'connected' ? t('connectionRegistry.state.connected') :
         s === 'connecting' ? t('connectionRegistry.state.connecting') :
         t('connectionRegistry.state.disconnected')
}

function fmtClock(ms?: number): string {
  if (!ms) return '—'
  return new Date(ms).toLocaleTimeString(locale.value, { hour12: false })
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

function elapsedOf(c: ConnectionRecord): string {
  if (!c.updated_at_ms) return '—'
  const v = Math.max(0, nowMs.value - c.updated_at_ms)
  if (v < 60_000) return Math.floor(v / 1000) + 's 前'
  if (v < 3_600_000) return Math.floor(v / 60_000) + 'm 前'
  return Math.floor(v / 3_600_000) + 'h 前'
}

const cards = computed<ViewCard[]>(() => records.value.map((r) => ({
  key: r.connection_id,
  record: r,
  state: r.state,
  in_flight: r.in_flight,
  updated_at_ms: r.updated_at_ms,
})))

const connectedCards = computed(() => cards.value.filter((c) => c.state === 'connected'))
const connectingCards = computed(() => cards.value.filter((c) => c.state === 'connecting'))
const disconnectedCards = computed(() => cards.value.filter((c) => c.state === 'disconnected'))

async function refreshFromApi() {
  try {
    const payload: ConnectionRegistryResponse = await fetchConnectionRegistry()
    apiDegraded.value = false
    records.value = payload.records
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
  document.addEventListener('visibilitychange', onVisibilityChange)
  releaseStream = acquireLiveStream()
})

onUnmounted(() => {
  stopPoll()
  stopTick()
  document.removeEventListener('visibilitychange', onVisibilityChange)
  if (releaseStream) { releaseStream(); releaseStream = null }
  expandedKeys.value.clear()
})

// SSE 增量：liveStreamStore.nodes 携带每个凭据的最新 in_flight，按 credential_id 折叠。
watch(() => liveStreamState.nodes, (nodes: LiveNodeStatus[]) => {
  const byCred = new Map<number, LiveNodeStatus>()
  for (const n of nodes) {
    if (n && Number.isFinite(n.credential_id)) byCred.set(n.credential_id, n)
  }
  records.value = records.value.map((r) => {
    const n = byCred.get(r.credential_id)
    if (!n) return r
    return {
      ...r,
      in_flight: typeof n.in_flight === 'number' ? n.in_flight : r.in_flight,
      fp_disabled: typeof n.fp_disabled === 'boolean' ? n.fp_disabled : r.fp_disabled,
      manual_disabled: n.manual_disabled,
      updated_at_ms: Date.now(),
    }
  })
}, { deep: true })

function toggleExpand(key: string) {
  const s = expandedKeys.value
  if (s.has(key)) s.delete(key)
  else s.add(key)
}

function openRecovery(credentialId: number) {
  router.push({ name: 'node-health-timeline', params: { credentialId: String(credentialId) } })
}
</script>

<template>
  <section class="connection-registry" data-testid="connection-registry">
    <header class="cr-head">
      <h2>{{ t('connectionRegistry.title') }}</h2>
      <p class="cr-sub">{{ t('connectionRegistry.subtitle') }}</p>
    </header>

    <div class="cr-status-row">
      <span v-if="apiDegraded" class="cr-chip is-danger" data-testid="api-degraded">
        {{ t('connectionRegistry.apiDegraded') }}
      </span>
    </div>

    <template v-if="!pageHidden">
      <div v-if="firstLoading && records.length === 0" class="cr-skeleton" data-testid="cr-skeleton">
        <div v-for="i in 3" :key="i" class="cr-skeleton-card"></div>
      </div>
      <template v-else>
        <section class="cr-section" data-testid="cr-connected">
          <header class="cr-section-head">
            <h3>{{ t('connectionRegistry.sections.connected') }}</h3>
            <span class="cr-count">{{ connectedCards.length }}</span>
          </header>
          <div v-if="connectedCards.length" class="cr-cards">
            <article v-for="c in connectedCards" :key="c.key" class="cr-card" :class="stateClass(c.state)" data-testid="cr-connected-card">
              <div class="cr-card__head">
                <span class="cr-badge" :class="stateClass(c.state)">{{ stateLabel(c.state) }}</span>
                <span class="cr-card__provider">{{ c.record.provider_name || c.record.provider_code || '—' }}</span>
                <span class="cr-card__cred">#{{ c.record.credential_id }}</span>
              </div>
              <div class="cr-card__models">
                <span v-for="m in c.record.raw_models" :key="m" class="cr-card__model">{{ m }}</span>
              </div>
              <div class="cr-card__meta">
                <span class="cr-card__inflight">{{ t('connectionRegistry.inFlight', { count: c.in_flight }) }}</span>
                <button type="button" class="cr-link" @click="openRecovery(c.record.credential_id)">
                  {{ t('connectionRegistry.viewTimeline') }}
                </button>
              </div>
            </article>
          </div>
          <div v-else class="cr-empty">{{ t('connectionRegistry.empty.connected') }}</div>
        </section>

        <section class="cr-section" data-testid="cr-connecting">
          <header class="cr-section-head">
            <h3>{{ t('connectionRegistry.sections.connecting') }}</h3>
            <span class="cr-count">{{ connectingCards.length }}</span>
          </header>
          <div v-if="connectingCards.length" class="cr-cards">
            <article v-for="c in connectingCards" :key="c.key" class="cr-card" :class="stateClass(c.state)" data-testid="cr-connecting-card">
              <div class="cr-card__head">
                <span class="cr-badge" :class="stateClass(c.state)">{{ stateLabel(c.state) }}</span>
                <span class="cr-card__provider">{{ c.record.provider_name || c.record.provider_code || '—' }}</span>
                <span class="cr-card__cred">#{{ c.record.credential_id }}</span>
              </div>
              <div class="cr-card__models">
                <span v-for="m in c.record.raw_models" :key="m" class="cr-card__model">{{ m }}</span>
              </div>
              <div class="cr-card__meta">
                <span class="cr-card__elapsed">{{ elapsedOf(c.record) }}</span>
              </div>
            </article>
          </div>
          <div v-else class="cr-empty">{{ t('connectionRegistry.empty.connecting') }}</div>
        </section>

        <section class="cr-section" data-testid="cr-disconnected">
          <header class="cr-section-head">
            <h3>{{ t('connectionRegistry.sections.disconnected') }}</h3>
            <span class="cr-count">{{ disconnectedCards.length }}</span>
          </header>
          <div v-if="disconnectedCards.length" class="cr-cards cr-cards--large">
            <article
              v-for="c in disconnectedCards"
              :key="c.key"
              class="cr-card cr-card--disconnected"
              :class="stateClass(c.state)"
              :data-expanded="expandedKeys.has(c.key) ? 'true' : 'false'"
              data-testid="cr-disconnected-card"
            >
              <div class="cr-card__summary" @click="toggleExpand(c.key)">
                <span class="cr-badge" :class="stateClass(c.state)">{{ stateLabel(c.state) }}</span>
                <span class="cr-card__provider">{{ c.record.provider_name || c.record.provider_code || '—' }}</span>
                <span class="cr-card__cred">#{{ c.record.credential_id }}</span>
                <span v-if="c.record.last_error_kind" class="cr-card__err">{{ c.record.last_error_kind }}</span>
                <span class="cr-card__expand">{{ expandedKeys.has(c.key) ? '▾' : '▸' }}</span>
              </div>
              <div v-if="c.record.recover_at" class="cr-card__meta">
                {{ t('connectionRegistry.recoverAt', { time: fmtClock(new Date(c.record.recover_at).getTime()), delta: fmtCountdown(new Date(c.record.recover_at).getTime()) }) }}
              </div>
              <div v-if="expandedKeys.has(c.key)" class="cr-card__detail">
                <div class="cr-detail-row"><span class="cr-detail-label">{{ t('connectionRegistry.detail.lastError') }}</span><span>{{ c.record.last_error_kind || '—' }}</span></div>
                <div v-if="c.record.last_error_at_ms" class="cr-detail-row"><span class="cr-detail-label">{{ t('connectionRegistry.detail.lastErrorAt') }}</span><span>{{ fmtDateTime(new Date(c.record.last_error_at_ms).toISOString()) }}</span></div>
                <div v-if="c.record.recover_at" class="cr-detail-row"><span class="cr-detail-label">{{ t('connectionRegistry.detail.recoverAt') }}</span><span>{{ fmtDateTime(c.record.recover_at) }}</span></div>
                <div class="cr-detail-row">
                  <button type="button" class="cr-link" @click="openRecovery(c.record.credential_id)">
                    {{ t('connectionRegistry.viewTimeline') }}
                  </button>
                </div>
              </div>
            </article>
          </div>
          <div v-else class="cr-empty">{{ t('connectionRegistry.empty.disconnected') }}</div>
        </section>
      </template>
    </template>
  </section>
</template>

<style scoped>
.connection-registry {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.cr-head h2 {
  margin: 0;
  font-size: 16px;
  color: var(--kx-text);
}

.cr-sub {
  margin: 4px 0 0;
  color: var(--kx-muted);
  font-size: 12px;
}

.cr-status-row { display: flex; flex-wrap: wrap; gap: 8px; }

.cr-chip {
  display: inline-flex;
  align-items: center;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 12px;
}

.cr-chip.is-danger { color: var(--kx-danger); background: var(--kx-danger-soft); }

.cr-section {
  border: 1px solid var(--kx-border);
  border-radius: 8px;
  background: var(--kx-surface);
  padding: 10px 12px;
}

.cr-section-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 8px;
}

.cr-section-head h3 {
  margin: 0;
  font-size: 13px;
  color: var(--kx-text);
}

.cr-count {
  font-size: 12px;
  color: var(--kx-muted);
  font-variant-numeric: tabular-nums;
}

.cr-cards {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.cr-cards--large { flex-direction: column; }

.cr-card {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 240px;
  padding: 8px 10px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-surface);
  font-size: 12px;
  color: var(--kx-text);
}

.cr-card.is-success { border-color: var(--kx-success); }
.cr-card.is-warning { border-color: var(--kx-warning); }
.cr-card.is-danger { border-color: var(--kx-danger); }

.cr-card__head,
.cr-card__summary {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.cr-card__summary { cursor: pointer; }

.cr-card__provider { font-weight: 600; }
.cr-card__cred { color: var(--kx-muted); font-family: var(--kx-mono, ui-monospace, monospace); }
.cr-card__err { color: var(--kx-danger); }
.cr-card__expand { margin-left: auto; color: var(--kx-muted); }

.cr-card__models { display: flex; flex-wrap: wrap; gap: 4px; }
.cr-card__model {
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 11px;
  background: var(--kx-bg-accent);
  color: var(--kx-text-secondary);
}

.cr-card__meta {
  display: flex;
  align-items: center;
  gap: 12px;
  color: var(--kx-text-secondary);
  font-size: 11px;
}

.cr-card__inflight { font-weight: 600; color: var(--kx-primary); }
.cr-card__elapsed { color: var(--kx-warning); font-variant-numeric: tabular-nums; }

.cr-link {
  border: 0;
  background: transparent;
  color: var(--kx-primary);
  cursor: pointer;
  font: inherit;
  font-size: 12px;
}

.cr-card__detail {
  border-top: 1px solid var(--kx-border);
  padding-top: 6px;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 4px 16px;
}

.cr-detail-row { display: flex; gap: 8px; font-size: 12px; }
.cr-detail-label { color: var(--kx-muted); min-width: 5em; }

.cr-badge {
  padding: 1px 8px;
  border-radius: 4px;
  font-size: 11px;
  white-space: nowrap;
}

.cr-badge.is-success { color: var(--kx-success); background: var(--kx-success-soft); }
.cr-badge.is-warning { color: var(--kx-warning); background: var(--kx-warning-soft); }
.cr-badge.is-danger { color: var(--kx-danger); background: var(--kx-danger-soft); }

.cr-empty {
  padding: 12px;
  text-align: center;
  color: var(--kx-muted);
  font-size: 12px;
}

.cr-skeleton {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.cr-skeleton-card {
  height: 56px;
  border-radius: 8px;
  background: var(--kx-bg-accent);
  animation: cr-skeleton-pulse 1.2s ease-in-out infinite;
}

@keyframes cr-skeleton-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.55; }
}
</style>
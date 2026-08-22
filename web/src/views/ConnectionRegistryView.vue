<script setup lang="ts">
// ConnectionRegistryView.vue — 流式客户端连接注册表（live + closed 审计）
//
// REST：GET /api/admin/connection-registry（15s 轮询）
// 按 request_id 查询：GET /api/admin/connection-registry/{id}（含近期 closed）
// 点击 request_id → 请求旅程详情
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import {
  fetchConnectionRegistry,
  fetchConnectionByRequestId,
  type ConnectionSnapshot,
} from '../api/connection-registry'

const router = useRouter()
const { t, locale } = useI18n()
const POLL_MS = 15_000

const live = ref<ConnectionSnapshot[]>([])
const closed = ref<ConnectionSnapshot[]>([])
const capacity = ref(0)
const liveCount = ref(0)
const firstLoading = ref(true)
const apiDegraded = ref(false)
const pageHidden = ref(typeof document !== 'undefined' ? document.hidden : false)
const nowMs = ref(Date.now())
const searchId = ref('')
const searchError = ref('')
const searchLoading = ref(false)
const filterText = ref('')

let pollTimer: number | undefined
let tickTimer: number | undefined

function fmtDateTime(iso?: string): string {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return '—'
  return d.toLocaleString(locale.value, { hour12: false })
}

function elapsedOf(iso?: string): string {
  if (!iso) return '—'
  const base = new Date(iso).getTime()
  if (Number.isNaN(base)) return '—'
  const v = Math.max(0, nowMs.value - base)
  if (v < 60_000) return Math.floor(v / 1000) + 's'
  if (v < 3_600_000) return Math.floor(v / 60_000) + 'm'
  return Math.floor(v / 3_600_000) + 'h'
}

function matchesFilter(snap: ConnectionSnapshot): boolean {
  const q = filterText.value.trim().toLowerCase()
  if (!q) return true
  return (
    snap.request_id.toLowerCase().includes(q) ||
    (snap.protocol ?? '').toLowerCase().includes(q) ||
    (snap.client_type ?? '').toLowerCase().includes(q) ||
    (snap.tenant_id ?? '').toLowerCase().includes(q)
  )
}

const filteredLive = computed(() => live.value.filter(matchesFilter))
const filteredClosed = computed(() => closed.value.filter(matchesFilter))

async function refreshFromApi() {
  try {
    const payload = await fetchConnectionRegistry()
    apiDegraded.value = false
    live.value = payload.live ?? []
    closed.value = payload.closed ?? []
    capacity.value = payload.capacity ?? 0
    liveCount.value = payload.live_count ?? live.value.length
  } catch {
    apiDegraded.value = true
  } finally {
    firstLoading.value = false
  }
}

async function searchByRequestId() {
  const id = searchId.value.trim()
  if (!id) return
  searchError.value = ''
  searchLoading.value = true
  try {
    const snap = await fetchConnectionByRequestId(id)
    if (snap.closed) {
      const idx = closed.value.findIndex((c) => c.request_id === id)
      if (idx >= 0) closed.value[idx] = snap
      else closed.value = [snap, ...closed.value]
    } else {
      const idx = live.value.findIndex((c) => c.request_id === id)
      if (idx >= 0) live.value[idx] = snap
      else live.value = [snap, ...live.value]
    }
    filterText.value = id
  } catch (e) {
    searchError.value = e instanceof Error ? e.message : String(e)
  } finally {
    searchLoading.value = false
  }
}

function openJourney(requestId: string) {
  router.push({ name: 'request-journey-detail', params: { requestId } })
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
})

onUnmounted(() => {
  stopPoll()
  stopTick()
  document.removeEventListener('visibilitychange', onVisibilityChange)
})
</script>

<template>
  <section class="connection-registry" data-testid="connection-registry">
    <header class="cr-head">
      <h2>{{ t('connectionRegistry.title') }}</h2>
      <p class="cr-sub">{{ t('connectionRegistry.subtitle') }}</p>
    </header>

    <div class="cr-search-row" data-testid="cr-search-row">
      <input
        v-model="searchId"
        type="search"
        class="cr-search-input"
        :placeholder="t('connectionRegistry.searchPlaceholder')"
        data-testid="cr-search-input"
        @keydown.enter.prevent="searchByRequestId()"
      />
      <button
        type="button"
        class="cr-search-btn"
        :disabled="searchLoading || !searchId.trim()"
        data-testid="cr-search-btn"
        @click="searchByRequestId()"
      >
        {{ t('connectionRegistry.search') }}
      </button>
      <button
        type="button"
        class="cr-search-btn cr-search-btn--secondary"
        :disabled="!searchId.trim()"
        data-testid="cr-journey-btn"
        @click="openJourney(searchId.trim())"
      >
        {{ t('connectionRegistry.openJourney') }}
      </button>
    </div>
    <p v-if="searchError" class="cr-search-error" data-testid="cr-search-error">{{ searchError }}</p>

    <div class="cr-filter-row">
      <input
        v-model="filterText"
        type="search"
        class="cr-filter-input"
        :placeholder="t('connectionRegistry.filterPlaceholder')"
        data-testid="cr-filter-input"
      />
    </div>

    <div class="cr-status-row">
      <span class="cr-chip is-muted" data-testid="cr-capacity">
        {{ t('connectionRegistry.liveCount', { count: liveCount, capacity }) }}
      </span>
      <span v-if="apiDegraded" class="cr-chip is-danger" data-testid="api-degraded">
        {{ t('connectionRegistry.apiDegraded') }}
      </span>
      <span class="cr-chip is-muted">{{ t('connectionRegistry.historyNote') }}</span>
    </div>

    <template v-if="!pageHidden">
      <div v-if="firstLoading && live.length === 0 && closed.length === 0" class="cr-skeleton" data-testid="cr-skeleton">
        <div v-for="i in 3" :key="i" class="cr-skeleton-card"></div>
      </div>
      <template v-else>
        <section class="cr-section" data-testid="cr-live">
          <header class="cr-section-head">
            <h3>{{ t('connectionRegistry.sections.live') }}</h3>
            <span class="cr-count">{{ filteredLive.length }}</span>
          </header>
          <div v-if="filteredLive.length" class="cr-table-wrap">
            <table class="cr-table">
              <thead>
                <tr>
                  <th>{{ t('connectionRegistry.columns.requestId') }}</th>
                  <th>{{ t('connectionRegistry.columns.protocol') }}</th>
                  <th>{{ t('connectionRegistry.columns.client') }}</th>
                  <th>{{ t('connectionRegistry.columns.frames') }}</th>
                  <th>{{ t('connectionRegistry.columns.lastFrame') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="c in filteredLive" :key="c.request_id" data-testid="cr-live-row">
                  <td>
                    <button type="button" class="cr-link" @click="openJourney(c.request_id)">{{ c.request_id }}</button>
                  </td>
                  <td>{{ c.protocol || '—' }}</td>
                  <td>{{ c.client_type || '—' }}</td>
                  <td>{{ c.frames_written ?? 0 }}</td>
                  <td>{{ elapsedOf(c.last_frame_at) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-else class="cr-empty">{{ t('connectionRegistry.empty.live') }}</div>
        </section>

        <section class="cr-section" data-testid="cr-closed">
          <header class="cr-section-head">
            <h3>{{ t('connectionRegistry.sections.closed') }}</h3>
            <span class="cr-count">{{ filteredClosed.length }}</span>
          </header>
          <div v-if="filteredClosed.length" class="cr-table-wrap">
            <table class="cr-table">
              <thead>
                <tr>
                  <th>{{ t('connectionRegistry.columns.requestId') }}</th>
                  <th>{{ t('connectionRegistry.columns.protocol') }}</th>
                  <th>{{ t('connectionRegistry.columns.closeReason') }}</th>
                  <th>{{ t('connectionRegistry.columns.frames') }}</th>
                  <th>{{ t('connectionRegistry.columns.registeredAt') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="c in filteredClosed" :key="'closed-' + c.request_id" data-testid="cr-closed-row">
                  <td>
                    <button type="button" class="cr-link" @click="openJourney(c.request_id)">{{ c.request_id }}</button>
                  </td>
                  <td>{{ c.protocol || '—' }}</td>
                  <td>{{ c.close_reason || '—' }}</td>
                  <td>{{ c.frames_written ?? 0 }}</td>
                  <td>{{ fmtDateTime(c.registered_at) }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div v-else class="cr-empty">{{ t('connectionRegistry.empty.closed') }}</div>
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

.cr-search-row,
.cr-filter-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}

.cr-search-input,
.cr-filter-input {
  flex: 1;
  min-width: 200px;
  padding: 6px 10px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  font-size: 12px;
  background: var(--kx-surface);
  color: var(--kx-text);
}

.cr-search-btn {
  padding: 6px 12px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  background: var(--kx-primary);
  color: #fff;
  font-size: 12px;
  cursor: pointer;
}

.cr-search-btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.cr-search-btn--secondary {
  background: var(--kx-surface);
  color: var(--kx-primary);
}

.cr-search-error {
  margin: 0;
  font-size: 12px;
  color: var(--kx-danger);
}

.cr-status-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.cr-chip {
  display: inline-flex;
  align-items: center;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 12px;
}

.cr-chip.is-danger { color: var(--kx-danger); background: var(--kx-danger-soft); }
.cr-chip.is-muted { color: var(--kx-muted); background: var(--kx-bg-accent); }

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

.cr-table-wrap { overflow-x: auto; }

.cr-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}

.cr-table th,
.cr-table td {
  padding: 6px 8px;
  text-align: left;
  border-bottom: 1px solid var(--kx-border);
}

.cr-table th {
  color: var(--kx-muted);
  font-weight: 500;
}

.cr-link {
  border: 0;
  background: transparent;
  color: var(--kx-primary);
  cursor: pointer;
  font: inherit;
  font-size: 12px;
  font-family: var(--kx-mono, ui-monospace, monospace);
}

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

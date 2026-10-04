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
import { fmtDateTime24h } from '../i18n/useFormat'
import { useWindowClass } from '../composables/useWindowClass'
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../components/ui/CardList.vue'

const router = useRouter()
const { t, locale } = useI18n()
const { isCompact } = useWindowClass()
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

/**
 * ── H6 第十二条切片（2026-10-06）：两张连接表接 compact 卡片形态 ───────────────
 *
 * ## 无分页 API ⇒ 只改呈现形态
 *
 * `fetchConnectionRegistry` 一次返回 live + closed 两段，**没有 offset/limit/page**
 * ⇒ 与第七~十那几页同源，**不引入连续加载**（门禁断「页面里不存在 `HyperLoadMore` /
 * `createHyperPages`」）。
 *
 * ## 三态归属：**第一种形态（表内三态）**的变体 —— 判据仍是「空态时那块东西在不在」
 *
 * 每节的空态是 `<div v-if="filteredX.length" class="cr-table-wrap">` + `<div v-else class="cr-empty">`
 * —— 空态时**整块表格被撤掉**、换上一段空文案。
 * ⇒ 容器挂在同一个 `v-if` 分支上（**不传 `:empty` / `:loading`**），
 * 两档共用页面自己的 `.cr-empty` 与 `.cr-skeleton`。
 *
 * ## 删掉 `.cr-table-wrap`（它自带 `overflow-x: auto`）
 *
 * 容器已经有 `.responsive-data-view__table { overflow-x: auto }`，
 * 两个横滚容器嵌套会出现**双横向滚动条**。同切片九。
 *
 * `table-min-width="0px"`：本页 `.cr-table` 只有 `width:100%`、**没有** `min-width`
 * —— 传默认 720px 会给窄内容凭空加一条横向滚动条。
 *
 * ## 卡头就是 `request_id`：键与脸同源，不用 `titleFormat`
 *
 * `request_id` 既是这一行的唯一身份，也是桌面上那个跳转按钮的文案。
 * `CardList` 的 `:key` 取自 `titleKey`、卡头取原始值 —— 这里两者**恰好都是它**，
 * 所以不需要 `titleFormat`（前几条切片需要，是因为卡头要与键不同）。
 *
 * ## `frames_written` 缺值出 `0` 而不是破折号 —— 与表格**故意保持一致**
 *
 * 桌面就是 `c.frames_written ?? 0`。「字段缺失」与「真的是 0 帧」在页面上被当成同一件事，
 * 严格说是个小口径问题（切片九的 M20 就为它红过一次）。
 * 本切片**不动桌面**，卡片也照抄 —— 表格与卡片共用同一份口径是硬规则，
 * 宁可两边一起不完美，也不要两边不一致。
 *
 * ⚠️ 本页也命中 §4.6 的 D1：`fmtDateTime24h(c.registered_at)` 只有日期没有时分
 * （那张表是 closed 历史，「注册于」只有日期确实不好用）。此处**沿用现状不改**。
 */

/** live 表卡片字段。标签全部复用 `connectionRegistry.columns.*` ⇒ 0 新增 i18n 键。 */
const liveCardFields = computed<CardField[]>(() => [
  { key: 'protocol', label: t('connectionRegistry.columns.protocol') },
  { key: 'client_type', label: t('connectionRegistry.columns.client') },
  {
    key: 'frames_written',
    label: t('connectionRegistry.columns.frames'),
    type: 'metric',
    align: 'end',
    // 与桌面同一个回落口径：缺失按 0（见上方注释）
    format: (v) => String(v ?? 0),
  },
  {
    key: 'last_frame_at',
    label: t('connectionRegistry.columns.lastFrame'),
    // ★ 复用同一个 elapsedOf（相对时间由 1s tick 的 nowMs 驱动）——
    //   写第二份就会与桌面漂移，而且**断言会变成依赖墙上时钟的时间炸弹**。
    format: (v) => elapsedOf(v == null ? undefined : String(v)),
  },
])

/** closed 表卡片字段。 */
const closedCardFields = computed<CardField[]>(() => [
  { key: 'protocol', label: t('connectionRegistry.columns.protocol') },
  { key: 'close_reason', label: t('connectionRegistry.columns.closeReason') },
  {
    key: 'frames_written',
    label: t('connectionRegistry.columns.frames'),
    type: 'metric',
    align: 'end',
    format: (v) => String(v ?? 0),
  },
  {
    key: 'registered_at',
    label: t('connectionRegistry.columns.registeredAt'),
    format: (v) => fmtDateTime24h(v == null ? undefined : String(v)),
  },
])
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
          <ResponsiveDataView
            v-if="filteredLive.length"
            :rows="filteredLive"
            title-key="request_id"
            :fields="liveCardFields"
            table-min-width="0px"
          >
            <template #table>
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
            </template>
            <template #actions="{ row }">
              <button type="button" class="cr-link cr-action" @click="openJourney(String(row.request_id))">
                {{ t('connectionRegistry.openJourney') }}
              </button>
            </template>
          </ResponsiveDataView>
          <div v-else class="cr-empty">{{ t('connectionRegistry.empty.live') }}</div>
        </section>

        <section class="cr-section" data-testid="cr-closed">
          <header class="cr-section-head">
            <h3>{{ t('connectionRegistry.sections.closed') }}</h3>
            <span class="cr-count">{{ filteredClosed.length }}</span>
          </header>
          <ResponsiveDataView
            v-if="filteredClosed.length"
            :rows="filteredClosed"
            title-key="request_id"
            :fields="closedCardFields"
            table-min-width="0px"
          >
            <template #table>
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
                  <td>{{ fmtDateTime24h(c.registered_at) }}</td>
                </tr>
              </tbody>
            </table>
            </template>
            <template #actions="{ row }">
              <button type="button" class="cr-link cr-action" @click="openJourney(String(row.request_id))">
                {{ t('connectionRegistry.openJourney') }}
              </button>
            </template>
          </ResponsiveDataView>
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
  color: var(--on-primary);
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

/*
 * 卡片里的跳转钮：桌面 `.cr-link` 是行内小字，按钮化后必须抬到 48px 触控下限。
 * 只在这条路径上加，桌面像素不动。
 * ★ 续行写成 `*` 开头是**计数器的口径要求**：它只跳过 trim 后以 `//` / `*` / `/*`
 *   开头的整行，续行不以 `*` 开头就会被计进硬编码中文（本段实测 +2）。
 *   这与「把注释改写成英文骗计数」是两回事 —— 这里是按常规块注释格式书写。
 */
.cr-action {
  min-height: 48px;
  padding: 0 10px;
  border: 1px solid var(--kx-border);
  border-radius: 6px;
  font: inherit;
  font-size: 13px;
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

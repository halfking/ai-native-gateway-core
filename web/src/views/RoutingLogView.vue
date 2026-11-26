<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { surfaceError } from '../i18n/errorSurface'
import {
  getCredentialRoutingLog,
  type RoutingLogEntry,
  type RoutingLogKind,
  type RoutingLogResult,
} from '../api'
import { parseLocalMinute } from '../utils/datetime'
import KxDateRangePicker from '../components/ui/KxDateRangePicker.vue'
import CollapsibleFilterSection from '../components/ui/CollapsibleFilterSection.vue'
import type { KxDateRange } from '../components/ui/kx-date-types'
import { isSuperAdmin, isRegularUser } from '../store'

// RoutingLogView — 路由记录 tab (docs/FEATURE-REQ-credential-heatmap-routing-log.md §5)
// Unified timeline of routing decisions / self-test runs / status changes.

// 2026-11-26: 权限控制
// - super_admin: 看凭据ID和标签
// - tenant_admin 和 user: 凭据详情已隐藏
const canSeeCredentialDetails = computed(() => isSuperAdmin())

type TimePreset = '24h' | 'today' | '7d' | 'month' | 'custom'

const { t } = useI18n()
const timePreset = ref<TimePreset>('24h')
const customTimeStart = ref('')
const customTimeEnd = ref('')
const kindFilter = ref<RoutingLogKind>('all')
const resultFilter = ref<RoutingLogResult>('all')
const modelInput = ref('')

// 2026-09-30 统一日历轮：custom 分支换 KxDateRangePicker（datetime 精度，instant——
// 原 @change=resetAndLoad 即时生效；presets=[] 因工具栏自带预设 select）。
// customTimeStart/End 维持 datetime-local 的 'T' 分隔，仅在边界做 'T'↔空格 互转
// （组件契约为空格分隔）。
const customRangeValue = computed<KxDateRange | null>(() => {
  if (!customTimeStart.value || !customTimeEnd.value) return null
  return { start: customTimeStart.value.replace('T', ' '), end: customTimeEnd.value.replace('T', ' ') }
})

function onKxRangeApply(range: KxDateRange) {
  customTimeStart.value = range.start.replace(' ', 'T')
  customTimeEnd.value = range.end.replace(' ', 'T')
  resetAndLoad()
}

const loading = ref(false)
const error = ref<string | null>(null)
const entries = ref<RoutingLogEntry[]>([])
const total = ref(0)
const meta = ref<{ duration_ms: number; time_start: string; time_end: string } | null>(null)

const PAGE_SIZE = 100
const currentPage = ref(0) // 0-based offset page

const expandedKey = ref<string | null>(null)

const kindOptions = computed<{ value: RoutingLogKind; label: string }[]>(() => [
 { value: 'all', label: t('routing.shared.log.kindAll') },
 { value: 'routing', label: t('routing.shared.log.kindRouting') },
 { value: 'probe', label: t('routing.shared.log.kindProbe') },
 { value: 'state_change', label: t('routing.shared.log.kindStateChange') },
])

const resultOptions = computed<{ value: RoutingLogResult; label: string }[]>(() => [
 { value: 'all', label: t('routing.shared.log.resultAll') },
 { value: 'success', label: t('routing.shared.log.resultSuccess') },
 { value: 'failed', label: t('routing.shared.log.resultFailed') },
])

// 2026-11-18 筛选收纳轮：生效条件 chips + 清空回调（默认值不算生效条件）
const TIME_PRESET_LABELS = computed<Record<TimePreset, string>>(() => ({
 '24h': t('routing.shared.log.preset24h'),
 today: t('routing.shared.log.presetToday'),
 '7d': t('routing.shared.log.preset7d'),
 month: t('routing.shared.log.presetMonth'),
 custom: t('routing.shared.log.presetCustom'),
}))
const logFilterChips = computed(() => {
  const chips: Array<{ key: string; label: string; onRemove: () => void }> = []
  if (timePreset.value !== '24h') {
    chips.push({ key: 'time', label: t('routing.shared.log.chipTime', { v: TIME_PRESET_LABELS.value[timePreset.value] }), onRemove: () => { timePreset.value = '24h'; resetAndLoad() } })
  }
  if (kindFilter.value !== 'all') {
    chips.push({ key: 'kind', label: t('routing.shared.log.chipKind', { v: kindOptions.value.find(o => o.value === kindFilter.value)?.label ?? kindFilter.value }), onRemove: () => { kindFilter.value = 'all'; resetAndLoad() } })
  }
  if (resultFilter.value !== 'all') {
    chips.push({ key: 'result', label: t('routing.shared.log.chipResult', { v: resultOptions.value.find(o => o.value === resultFilter.value)?.label ?? resultFilter.value }), onRemove: () => { resultFilter.value = 'all'; resetAndLoad() } })
  }
  if (modelInput.value.trim()) {
    chips.push({ key: 'model', label: t('routing.shared.log.chipModel', { v: modelInput.value.trim() }), onRemove: () => { modelInput.value = ''; resetAndLoad() } })
  }
  return chips
})

function resetLogFilters() {
  timePreset.value = '24h'
  kindFilter.value = 'all'
  resultFilter.value = 'all'
  modelInput.value = ''
  resetAndLoad()
}

function computeRange(): { start: string; end: string } {
  const now = new Date()
  let start: Date
  let end: Date = now
  switch (timePreset.value) {
    case '24h':
      start = new Date(now.getTime() - 24 * 3600 * 1000)
      break
    case 'today':
      start = new Date(now.getFullYear(), now.getMonth(), now.getDate())
      break
    case '7d':
      start = new Date(now.getTime() - 7 * 24 * 3600 * 1000)
      break
    case 'month':
      start = new Date(now.getFullYear(), now.getMonth(), 1)
      break
    case 'custom':
      // 'YYYY-MM-DDTHH:mm' 无秒非规范格式，补秒解析（P3-3）
      start = customTimeStart.value ? parseLocalMinute(customTimeStart.value) : new Date(now.getTime() - 24 * 3600 * 1000)
      end = customTimeEnd.value ? parseLocalMinute(customTimeEnd.value) : now
      break
  }
  return { start: start.toISOString(), end: end.toISOString() }
}

async function loadLog() {
  loading.value = true
  error.value = null
  try {
    const range = computeRange()
    const resp = await getCredentialRoutingLog({
      timeStart: range.start,
      timeEnd: range.end,
      kind: kindFilter.value,
      result: resultFilter.value,
      model: modelInput.value.trim() || undefined,
      limit: PAGE_SIZE,
      offset: currentPage.value * PAGE_SIZE,
    })
    entries.value = resp.entries
    total.value = resp.total
    meta.value = resp.meta
  } catch (e) {
    error.value = surfaceError(e)
    console.error('Failed to load routing log', e)
  } finally {
    loading.value = false
  }
}

function resetAndLoad() {
  currentPage.value = 0
  loadLog()
}

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))

const kindLabels = computed<Record<string, string>>(() => ({
 routing: t('routing.shared.log.kindRoutingShort'),
 probe: t('routing.shared.log.kindProbeShort'),
 state_change: t('routing.shared.log.kindStateChangeShort'),
}))

const kindBadgeClass: Record<string, string> = {
  routing: 'badge-kind-routing',
  probe: 'badge-kind-probe',
  state_change: 'badge-kind-state',
}

const changeLabels = computed<Record<string, string>>(() => ({
 recovered: t('routing.shared.log.changeRecovered'),
 broke: t('routing.shared.log.changeBroke'),
 online: t('routing.shared.log.changeOnline'),
 offline: t('routing.shared.log.changeOffline'),
}))

const changeClass: Record<string, string> = {
  recovered: 'change-recovered',
  broke: 'change-broke',
  online: 'change-manual',
  offline: 'change-manual',
}

function statusText(e: RoutingLogEntry): string {
  if (e.kind === 'state_change') return changeLabels.value[e.change || ''] || e.status
  if (e.success) return t('routing.shared.log.statusSuccess')
  return t('routing.shared.log.statusFailed')
}

function statusClass(e: RoutingLogEntry): string {
  if (e.kind === 'state_change') return changeClass[e.change || ''] || ''
  return e.success ? 'st-ok' : 'st-fail'
}

function rowKey(e: RoutingLogEntry, idx: number): string {
  return `${e.ts}|${e.kind}|${e.model}|${e.credential_id ?? ''}|${idx}`
}

function toggleExpand(key: string) {
  expandedKey.value = expandedKey.value === key ? null : key
}

function formatTs(ts: string): string {
  const d = new Date(ts)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
}

function errorSummary(e: RoutingLogEntry): string {
  const msg = e.error_message || e.error_code || ''
  return msg.length > 80 ? msg.slice(0, 80) + '…' : msg
}

let refreshTimer: number | null = null
const autoRefresh = ref(false)

function toggleAutoRefresh() {
  if (autoRefresh.value) {
    if (refreshTimer) { clearInterval(refreshTimer); refreshTimer = null }
    autoRefresh.value = false
  } else {
    refreshTimer = window.setInterval(loadLog, 30000)
    autoRefresh.value = true
  }
}

onMounted(loadLog)
onUnmounted(() => { if (refreshTimer) clearInterval(refreshTimer) })
</script>

<template>
  <div class="routing-log">
    <!-- Toolbar（2026-11-18 筛选收纳轮：条件默认收拢进 CollapsibleFilterSection，
         自动刷新/刷新按钮是页面级操作留在头部常驻） -->
    <div class="log-toolbar">
      <div class="toolbar-row">
        <CollapsibleFilterSection :chips="logFilterChips" @reset="resetLogFilters" style="flex:1;border:none;margin:0;background:transparent">
          <template #actions></template>
          <span class="label">{{ t('routing.shared.log.timeRange') }}</span>
          <select v-model="timePreset" class="field-input w-preset" @change="resetAndLoad">
            <option value="24h">{{ t('routing.shared.log.preset24h') }}</option>
            <option value="today">{{ t('routing.shared.log.presetToday') }}</option>
            <option value="7d">{{ t('routing.shared.log.preset7d') }}</option>
            <option value="month">{{ t('routing.shared.log.presetMonth') }}</option>
            <option value="custom">{{ t('routing.shared.log.presetCustom') }}</option>
          </select>

          <span class="label">{{ $t('common.terms.type') }}</span>
          <select v-model="kindFilter" class="field-input w-kind" @change="resetAndLoad">
            <option v-for="o in kindOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
          </select>

          <span class="label">{{ $t('common.terms.result') }}</span>
          <select v-model="resultFilter" class="field-input w-result" @change="resetAndLoad">
            <option v-for="o in resultOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
          </select>

          <input
            v-model="modelInput"
            class="field-input model-input"
            :placeholder="t('routing.shared.log.modelPlaceholder')"
            @keyup.enter="resetAndLoad"
          />
        </CollapsibleFilterSection>

        <label class="checkbox-label">
          <input type="checkbox" :checked="autoRefresh" @change="toggleAutoRefresh" />
          {{ $t('common.terms.autoRefresh') }}
        </label>

        <span class="spacer"></span>
        <button class="btn btn-sm btn-primary" :disabled="loading" @click="loadLog">
          {{ loading ? $t('common.feedback.loading') : $t('common.button.refresh') }}
        </button>
      </div>

      <div class="toolbar-row" v-if="timePreset === 'custom'">
        <span class="label">{{ t('routing.shared.log.customRange') }}</span>
        <KxDateRangePicker
          :model-value="customRangeValue"
          :presets="[]"
          precision="datetime"
          instant
          @apply="onKxRangeApply"
        />
      </div>
    </div>

    <!-- Error banner -->
    <div v-if="error" class="error-banner">⚠️ {{ error }}</div>

    <!-- Meta -->
    <div v-if="meta" class="meta-info">
 {{ t('routing.shared.log.metaTotal', { total, ms: meta.duration_ms }) }}
    </div>

    <!-- Loading -->
    <div v-if="loading && !entries.length" class="loading-state">
      <div class="spinner"></div>
      <p>{{ t('routing.shared.log.loading') }}</p>
    </div>

    <!-- Empty -->
    <div v-else-if="!loading && !entries.length" class="empty-state">
      <p>{{ $t('routing.shared.logEmpty') }}</p>
    </div>

    <!-- Table -->
    <div v-else class="log-table card">
      <table>
        <thead>
          <tr>
            <th class="col-ts">{{ $t('common.time') }}</th>
            <th class="col-kind">{{ $t('common.terms.type') }}</th>
            <th>{{ $t('common.terms.model') }}</th>
            <th>{{ $t('common.credentialFallback') }}</th>
            <th class="col-status">{{ $t('common.terms.result') }}</th>
            <th class="col-lat">{{ $t('common.terms.latency') }}</th>
            <th class="col-err">{{ t('routing.shared.log.colErrorSummary') }}</th>
          </tr>
        </thead>
        <tbody>
          <template v-for="(e, idx) in entries" :key="rowKey(e, idx)">
            <tr
              class="log-row"
              :class="{ 'row-state-change': e.kind === 'state_change', expanded: expandedKey === rowKey(e, idx) }"
              @click="toggleExpand(rowKey(e, idx))"
            >
              <td class="col-ts mono">{{ formatTs(e.ts) }}</td>
              <td class="col-kind">
                <span class="kind-badge" :class="kindBadgeClass[e.kind]">{{ kindLabels[e.kind] || e.kind }}</span>
              </td>
              <td class="mono model-cell">{{ e.model }}</td>
              <td>
                <!-- 2026-11-26: 凭据脱敏 - super_admin看全部，tenant_admin和user看隐藏 -->
                <span v-if="canSeeCredentialDetails && e.credential_id != null">
                  {{ e.credential_label || `#${e.credential_id}` }}
                </span>
                <span v-else-if="e.credential_id != null" class="muted">
                  {{ $t('routing.credentialHidden') }}
                </span>
                <span v-else class="muted">—</span>
                <span v-if="canSeeCredentialDetails && e.provider_name" class="muted"> · {{ e.provider_name }}</span>
              </td>
              <td class="col-status">
                <span class="status-pill" :class="statusClass(e)">{{ statusText(e) }}</span>
              </td>
              <td class="col-lat mono">{{ e.latency_ms != null ? `${e.latency_ms}ms` : '—' }}</td>
              <td class="col-err mono muted">{{ errorSummary(e) || '—' }}</td>
            </tr>
            <tr v-if="expandedKey === rowKey(e, idx)" class="detail-row-tr">
              <td colspan="7">
                <div class="detail-grid">
                  <div class="detail-item">
                    <span class="detail-label">{{ t('routing.shared.log.detailFullTime') }}</span>
                    <span class="mono">{{ e.ts }}</span>
                  </div>
                  <div class="detail-item" v-if="e.kind === 'state_change'">
                    <span class="detail-label">{{ t('routing.shared.log.detailChangeType') }}</span>
                    <span>{{ changeLabels[e.change || ''] || e.change }}</span>
                  </div>
                  <div class="detail-item" v-if="e.kind === 'state_change'">
                    <span class="detail-label">{{ $t('common.terms.operator') }}</span>
                    <span>{{ e.actor || t('routing.shared.log.actorSystem') }}</span>
                  </div>
                  <div class="detail-item">
                    <span class="detail-label">{{ $t('common.terms.source') }}</span>
                    <span>{{ e.source }}</span>
                  </div>
                  <div class="detail-item" v-if="e.tier != null">
                    <span class="detail-label">{{ t('routing.shared.log.detailTier') }}</span>
                    <span>Tier {{ e.tier }}</span>
                  </div>
                  <div class="detail-item" v-if="e.status">
                    <span class="detail-label">{{ t('routing.shared.log.detailStatusCode') }}</span>
                    <span class="mono">{{ e.status }}</span>
                  </div>
                  <div class="detail-item" v-if="e.error_code">
                    <span class="detail-label">{{ t('routing.shared.log.detailErrorCategory') }}</span>
                    <span class="mono">{{ e.error_code }}</span>
                  </div>
                  <div class="detail-item detail-full" v-if="e.error_message">
                    <span class="detail-label">{{ t('routing.shared.log.detailFullError') }}</span>
                    <pre class="error-pre">{{ e.error_message }}</pre>
                  </div>
                  <div class="detail-item" v-if="e.request_id">
                    <span class="detail-label">{{ $t('common.terms.request') }} ID</span>
                    <router-link :to="`/request-detail/${e.request_id}`" target="_blank" class="mono req-link">
                      {{ e.request_id }}
                    </router-link>
                  </div>
                  <div class="detail-item" v-if="e.http_status != null && e.kind === 'probe'">
                    <span class="detail-label">HTTP 状态码</span>
                    <span class="mono">{{ e.http_status }}</span>
                  </div>
                  <div class="detail-item" v-if="e.sticky != null && e.kind === 'routing'">
                    <span class="detail-label">{{ t('routing.shared.log.detailSticky') }}</span>
                    <span>{{ e.sticky ? $t('common.button.yes') : $t('common.button.no') }}</span>
                  </div>
                  <div class="detail-item" v-if="e.outbound_model && e.kind === 'routing'">
                    <span class="detail-label">{{ $t('common.terms.outboundModel') }}</span>
                    <span class="mono">{{ e.outbound_model }}</span>
                  </div>
                  <div class="detail-item detail-full" v-if="e.detail">
                    <span class="detail-label">{{ t('routing.shared.log.detailInfo') }}</span>
                    <pre class="error-pre">{{ e.detail }}</pre>
                  </div>
                </div>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>

    <!-- Pagination -->
    <div v-if="total > PAGE_SIZE" class="pagination">
      <button class="btn btn-sm" :disabled="currentPage === 0 || loading" @click="currentPage--; loadLog()">
        {{ $t('common.pagination.previous') }}
      </button>
 <span class="page-info">{{ t('routing.shared.log.pageInfo', { cur: currentPage +1, total: totalPages }) }}</span>
      <button class="btn btn-sm" :disabled="(currentPage + 1) * PAGE_SIZE >= total || loading" @click="currentPage++; loadLog()">
        {{ $t('common.pagination.next') }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.routing-log {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.log-toolbar {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}

.toolbar-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.label {
  font-size: 12px;
  color: var(--muted);
  font-weight: 600;
}

.field-input {
  /* width:auto 覆盖全局 input/select width:100%，避免筛选控件占满整行 */
  width: auto;
  padding: 4px 8px;
  font-size: 12px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg);
  color: var(--text);
}

.field-input.w-preset { width: 128px; flex-shrink: 0; }
.field-input.w-kind { width: 130px; flex-shrink: 0; }
.field-input.w-result { width: 110px; flex-shrink: 0; }

.model-input {
  width: 200px;
}

.checkbox-label {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  cursor: pointer;
}

.spacer { flex: 1; }

.error-banner {
  padding: 12px;
  background: rgba(239, 68, 68, 0.1);
  border: 1px solid rgba(239, 68, 68, 0.3);
  border-radius: var(--radius);
  color: var(--danger);
  font-size: 13px;
}

.meta-info {
  padding: 8px 12px;
  background: var(--bg-subtle, transparent);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  font-size: 11px;
  color: var(--muted);
}

.loading-state,
.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  padding: 64px;
  color: var(--muted);
}

.spinner {
  width: 36px;
  height: 36px;
  border: 3px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spin 1s linear infinite;
}

@keyframes spin { to { transform: rotate(360deg); } }

.log-table { overflow-x: auto; }

.log-table table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}

.log-table th, .log-table td {
  padding: 8px 8px;
  text-align: left;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}

.log-table th {
  font-size: 11px;
  color: var(--muted);
  font-weight: 600;
  position: sticky;
  top: 0;
  background: var(--card);
}

.col-ts { width: 130px; }
.col-kind { width: 84px; }
.col-status { width: 88px; }
.col-lat { width: 76px; text-align: right; }
.col-err { max-width: 360px; overflow: hidden; text-overflow: ellipsis; }

.log-row { cursor: pointer; }
.log-row:hover { background: var(--bg-subtle); }
.log-row.expanded { background: var(--bg-subtle); }

/* ★ 状态变化行用「整行语义软填充底」表达，不用首列彩色侧边条
   （craft-floor：ban-side-stripe-borders）。
   放在 tr 上而不是 td 上 ⇒ `:hover`（.log-row:hover，特异性更高）仍然压得住它。 */
.row-state-change { background: color-mix(in srgb, var(--accent) 8%, transparent); }

.mono { font-family: var(--font-mono, monospace); font-size: 11px; }
.muted { color: var(--muted); }

.model-cell { max-width: 220px; overflow: hidden; text-overflow: ellipsis; }

.kind-badge {
  display: inline-block;
  padding: 2px 4px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
}

.badge-kind-routing { background: color-mix(in srgb, var(--accent) 15%, transparent); color: var(--accent); }
.badge-kind-probe { background: color-mix(in srgb, var(--purple) 15%, transparent); color: var(--purple); }
.badge-kind-state { background: color-mix(in srgb, var(--warning) 15%, transparent); color: var(--warning); }

.status-pill {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 600;
}

.st-ok { background: color-mix(in srgb, var(--success) 15%, transparent); color: var(--success); }
.st-fail { background: color-mix(in srgb, var(--danger) 15%, transparent); color: var(--danger); }

.change-recovered { background: color-mix(in srgb, var(--success) 15%, transparent); color: var(--success); }
.change-broke { background: color-mix(in srgb, var(--danger) 15%, transparent); color: var(--danger); }
.change-manual { background: color-mix(in srgb, var(--accent) 15%, transparent); color: var(--accent); }

.detail-row-tr td {
  background: var(--bg-subtle);
  white-space: normal;
}

.detail-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 8px 16px;
  padding: 4px 2px;
}

.detail-item { display: flex; flex-direction: column; gap: 2px; }
.detail-full { grid-column: 1 / -1; }

.detail-label {
  font-size: 11px;
  color: var(--muted);
  font-weight: 600;
}

.error-pre {
  margin: 0;
  padding: 8px;
  background: var(--bg);
  border-radius: 4px;
  font-size: 11px;
  font-family: var(--font-mono, monospace);
  white-space: pre-wrap;
  word-break: break-all;
  max-height: 180px;
  overflow-y: auto;
}

.req-link { color: var(--accent); word-break: break-all; }

.pagination {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 12px;
}

.page-info { font-size: 12px; color: var(--muted); }
</style>

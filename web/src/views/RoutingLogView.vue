<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import {
  getCredentialRoutingLog,
  type RoutingLogEntry,
  type RoutingLogKind,
  type RoutingLogResult,
} from '../api'

// RoutingLogView — 路由记录 tab (docs/FEATURE-REQ-credential-heatmap-routing-log.md §5)
// Unified timeline of routing decisions / self-test runs / status changes.

type TimePreset = '24h' | 'today' | '7d' | 'month' | 'custom'

const timePreset = ref<TimePreset>('24h')
const customTimeStart = ref('')
const customTimeEnd = ref('')
const kindFilter = ref<RoutingLogKind>('all')
const resultFilter = ref<RoutingLogResult>('all')
const modelInput = ref('')

const loading = ref(false)
const error = ref<string | null>(null)
const entries = ref<RoutingLogEntry[]>([])
const total = ref(0)
const meta = ref<{ duration_ms: number; time_start: string; time_end: string } | null>(null)

const PAGE_SIZE = 100
const currentPage = ref(0) // 0-based offset page

const expandedKey = ref<string | null>(null)

const kindOptions: { value: RoutingLogKind; label: string }[] = [
  { value: 'all', label: '全部' },
  { value: 'routing', label: '路由选择' },
  { value: 'probe', label: '自检测试' },
  { value: 'state_change', label: '状态变化' },
]

const resultOptions: { value: RoutingLogResult; label: string }[] = [
  { value: 'all', label: '全部结果' },
  { value: 'success', label: '仅成功' },
  { value: 'failed', label: '仅失败' },
]

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
      start = customTimeStart.value ? new Date(customTimeStart.value) : new Date(now.getTime() - 24 * 3600 * 1000)
      end = customTimeEnd.value ? new Date(customTimeEnd.value) : now
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
    error.value = e instanceof Error ? e.message : String(e)
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

const kindLabels: Record<string, string> = {
  routing: '路由选择',
  probe: '自检',
  state_change: '状态变化',
}

const kindBadgeClass: Record<string, string> = {
  routing: 'badge-kind-routing',
  probe: 'badge-kind-probe',
  state_change: 'badge-kind-state',
}

const changeLabels: Record<string, string> = {
  recovered: '恢复 ✓',
  broke: '故障 ✗',
  online: '手动上线',
  offline: '手动下线',
}

const changeClass: Record<string, string> = {
  recovered: 'change-recovered',
  broke: 'change-broke',
  online: 'change-manual',
  offline: 'change-manual',
}

function statusText(e: RoutingLogEntry): string {
  if (e.kind === 'state_change') return changeLabels[e.change || ''] || e.status
  if (e.success) return '成功'
  return '失败'
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
    <!-- Toolbar -->
    <div class="log-toolbar">
      <div class="toolbar-row">
        <span class="label">时间范围</span>
        <select v-model="timePreset" class="field-input" @change="resetAndLoad">
          <option value="24h">最近24小时</option>
          <option value="today">今天</option>
          <option value="7d">最近7天</option>
          <option value="month">本月</option>
          <option value="custom">自定义</option>
        </select>

        <span class="label">类型</span>
        <select v-model="kindFilter" class="field-input" @change="resetAndLoad">
          <option v-for="o in kindOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>

        <span class="label">结果</span>
        <select v-model="resultFilter" class="field-input" @change="resetAndLoad">
          <option v-for="o in resultOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
        </select>

        <input
          v-model="modelInput"
          class="field-input model-input"
          placeholder="按模型过滤 (回车搜索)"
          @keyup.enter="resetAndLoad"
        />

        <label class="checkbox-label">
          <input type="checkbox" :checked="autoRefresh" @change="toggleAutoRefresh" />
          自动刷新
        </label>

        <span class="spacer"></span>
        <button class="btn btn-sm btn-primary" :disabled="loading" @click="loadLog">
          {{ loading ? '加载中...' : '刷新' }}
        </button>
      </div>

      <div class="toolbar-row" v-if="timePreset === 'custom'">
        <span class="label">开始时间</span>
        <input type="datetime-local" v-model="customTimeStart" class="field-input" @change="resetAndLoad" />
        <span class="label">结束时间</span>
        <input type="datetime-local" v-model="customTimeEnd" class="field-input" @change="resetAndLoad" />
      </div>
    </div>

    <!-- Error banner -->
    <div v-if="error" class="error-banner">⚠️ {{ error }}</div>

    <!-- Meta -->
    <div v-if="meta" class="meta-info">
      共 {{ total }} 条记录 · 查询耗时 {{ meta.duration_ms }}ms
    </div>

    <!-- Loading -->
    <div v-if="loading && !entries.length" class="loading-state">
      <div class="spinner"></div>
      <p>加载路由记录...</p>
    </div>

    <!-- Empty -->
    <div v-else-if="!loading && !entries.length" class="empty-state">
      <p>暂无记录</p>
      <p class="hint-text">请调整时间范围或过滤条件</p>
    </div>

    <!-- Table -->
    <div v-else class="log-table card">
      <table>
        <thead>
          <tr>
            <th class="col-ts">时间</th>
            <th class="col-kind">类型</th>
            <th>模型</th>
            <th>凭据</th>
            <th class="col-status">结果</th>
            <th class="col-lat">延迟</th>
            <th class="col-err">错误摘要</th>
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
                <span v-if="e.credential_id != null">{{ e.credential_label || `#${e.credential_id}` }}</span>
                <span v-else class="muted">—</span>
                <span v-if="e.provider_name" class="muted"> · {{ e.provider_name }}</span>
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
                    <span class="detail-label">完整时间</span>
                    <span class="mono">{{ e.ts }}</span>
                  </div>
                  <div class="detail-item" v-if="e.kind === 'state_change'">
                    <span class="detail-label">变化类型</span>
                    <span>{{ changeLabels[e.change || ''] || e.change }}</span>
                  </div>
                  <div class="detail-item" v-if="e.kind === 'state_change'">
                    <span class="detail-label">操作人</span>
                    <span>{{ e.actor || '系统' }}</span>
                  </div>
                  <div class="detail-item">
                    <span class="detail-label">来源</span>
                    <span>{{ e.source }}</span>
                  </div>
                  <div class="detail-item" v-if="e.tier != null">
                    <span class="detail-label">候选层级</span>
                    <span>Tier {{ e.tier }}</span>
                  </div>
                  <div class="detail-item" v-if="e.status">
                    <span class="detail-label">状态码</span>
                    <span class="mono">{{ e.status }}</span>
                  </div>
                  <div class="detail-item" v-if="e.error_code">
                    <span class="detail-label">错误类别</span>
                    <span class="mono">{{ e.error_code }}</span>
                  </div>
                  <div class="detail-item detail-full" v-if="e.error_message">
                    <span class="detail-label">完整错误信息</span>
                    <pre class="error-pre">{{ e.error_message }}</pre>
                  </div>
                  <div class="detail-item" v-if="e.request_id">
                    <span class="detail-label">请求 ID</span>
                    <router-link :to="`/request-detail/${e.request_id}`" target="_blank" class="mono req-link">
                      {{ e.request_id }}
                    </router-link>
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
        上一页
      </button>
      <span class="page-info">第 {{ currentPage + 1 }} / {{ totalPages }} 页</span>
      <button class="btn btn-sm" :disabled="(currentPage + 1) * PAGE_SIZE >= total || loading" @click="currentPage++; loadLog()">
        下一页
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
  padding: 4px 8px;
  font-size: 12px;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg);
  color: var(--text);
}

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
  padding: 60px;
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
  padding: 7px 10px;
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
.log-row:hover { background: var(--bg-subtle, rgba(128, 128, 128, 0.06)); }
.log-row.expanded { background: var(--bg-subtle, rgba(128, 128, 128, 0.08)); }

.row-state-change td:first-child { box-shadow: inset 3px 0 0 var(--accent); }

.mono { font-family: var(--font-mono, monospace); font-size: 11px; }
.muted { color: var(--muted); }

.model-cell { max-width: 220px; overflow: hidden; text-overflow: ellipsis; }

.kind-badge {
  display: inline-block;
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
}

.badge-kind-routing { background: rgba(59, 130, 246, 0.15); color: #3b82f6; }
.badge-kind-probe { background: color-mix(in srgb, var(--purple) 15%, transparent); color: var(--purple); }
.badge-kind-state { background: rgba(245, 158, 11, 0.15); color: #f59e0b; }

.status-pill {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 600;
}

.st-ok { background: rgba(16, 185, 129, 0.15); color: #10b981; }
.st-fail { background: rgba(239, 68, 68, 0.15); color: #ef4444; }

.change-recovered { background: rgba(16, 185, 129, 0.15); color: #10b981; }
.change-broke { background: rgba(239, 68, 68, 0.15); color: #ef4444; }
.change-manual { background: rgba(59, 130, 246, 0.15); color: #3b82f6; }

.detail-row-tr td {
  background: var(--bg-subtle, rgba(128, 128, 128, 0.05));
  white-space: normal;
}

.detail-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 8px 20px;
  padding: 6px 2px;
}

.detail-item { display: flex; flex-direction: column; gap: 2px; }
.detail-full { grid-column: 1 / -1; }

.detail-label {
  font-size: 10px;
  color: var(--muted);
  font-weight: 600;
  text-transform: uppercase;
}

.error-pre {
  margin: 0;
  padding: 8px;
  background: var(--bg, rgba(128, 128, 128, 0.08));
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

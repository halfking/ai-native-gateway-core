<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { ref, onMounted, computed } from 'vue'
import { req } from '../api/_core'
import { getLocale } from '../store'

const { t, te } = useI18n({ useScope: 'global' })

// 审计记录类型（与后端 admin/session_audit.go + domains/sessionaudit/types.go 对齐）
// 注意：后端分数均为 0-10，Threat.severity 是 int，Threat 字段为 evidence 而非 description。
type Threat = {
  type: string
  severity: number // 0-10
  evidence: string
  detected_at: string
}

type SessionAuditRecord = {
  id: number
  session_id: string
  tenant_id: string
  client_info: {
    ip: string
    user_agent: string
    model: string
    agent: string
    device_seed: string
  }
  summary?: {
    title: string
    key_points?: string[]
    content_hash: string
  }
  intent?: {
    type: string
    score: number
    reason: string
  }
  scores: {
    security: number  // 0-10
    danger: number    // 0-10
    trust: number     // 0-10
    sensitive: number // 0-10
  }
  detect_result?: {
    score: number // 0-10
    decision: string
    reason?: string
    threats?: Threat[]
    sensitive_words?: string[]
  }
  status: string
  approval_status?: string
  created_at: string
}

type SessionAuditListResponse = {
  records: SessionAuditRecord[]
  total: number
  limit: number
  offset: number
}

type SessionAuditStats = {
  total: number
  by_status: Record<string, number>
  by_approval: Record<string, number>
  avg_score: number
  top_threats: Array<{ type: string; count: number }>
}

const records = ref<SessionAuditRecord[]>([])
const stats = ref<SessionAuditStats | null>(null)
const total = ref(0)
const page = ref(1)
const size = ref(50)
const loading = ref(false)
const statsLoading = ref(false)
const statsError = ref('')
const error = ref('')

const filterTenantID = ref('')
const filterSessionID = ref('')
const filterStatus = ref('')

const detailVisible = ref(false)
const detailRecord = ref<SessionAuditRecord | null>(null)

const totalPages = computed(() => Math.max(1, Math.ceil(total.value / size.value)))
const offset = computed(() => (page.value - 1) * size.value)

async function loadStats() {
  statsLoading.value = true
  statsError.value = ''
  try {
    const params = new URLSearchParams()
    if (filterTenantID.value) params.append('tenant_id', filterTenantID.value)
    const url = `/api/admin/session-audit/stats${params.toString() ? '?' + params.toString() : ''}`
    const data = await req<SessionAuditStats>('GET', url)
    stats.value = data
  } catch (e: unknown) {
    statsError.value = e instanceof Error ? e.message : String(e)
    console.error('加载审计统计失败:', e)
  } finally {
    statsLoading.value = false
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const params = new URLSearchParams()
    if (filterTenantID.value) params.append('tenant_id', filterTenantID.value)
    if (filterSessionID.value) params.append('session_id', filterSessionID.value)
    if (filterStatus.value) params.append('status', filterStatus.value)
    params.append('limit', size.value.toString())
    params.append('offset', offset.value.toString())

    const url = `/api/admin/session-audit?${params.toString()}`
    const data = await req<SessionAuditListResponse>('GET', url)
    records.value = data.records || []
    total.value = data.total || 0
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : t('sessions.audit.loadFailed')
    records.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

function resetPageAndLoad() {
  page.value = 1
  load()
  loadStats()
}

function changePage(delta: number) {
  const next = page.value + delta
  if (next < 1 || next > totalPages.value) return
  page.value = next
  load()
}

function clearFilters() {
  filterTenantID.value = ''
  filterSessionID.value = ''
  filterStatus.value = ''
  resetPageAndLoad()
}

async function showDetail(record: SessionAuditRecord) {
  detailRecord.value = record
  detailVisible.value = true
}

function closeDetail() {
  detailVisible.value = false
  detailRecord.value = null
}

// 用显式 Map 代替动态键拼接，彻底避免 vue-i18n key 转换 bug
// （此前 `status${status.replace('_','')}` 会把 need_approval 转成
//  statusNeedapproval，而 locale 里是 statusNeedApproval，大小写不匹配）。
const STATUS_BADGE_CLASS: Record<string, string> = {
  pass: 'badge-green',
  warn: 'badge-yellow',
  blocked: 'badge-red',
  need_approval: 'badge-blue',
}
const STATUS_I18N_KEY: Record<string, string> = {
  pass: 'sessions.audit.statusPass',
  warn: 'sessions.audit.statusWarn',
  blocked: 'sessions.audit.statusBlocked',
  need_approval: 'sessions.audit.statusNeedApproval',
}
const APPROVAL_BADGE_CLASS: Record<string, string> = {
  pending: 'badge-blue',
  approved: 'badge-green',
  rejected: 'badge-red',
  timeout: 'badge-gray',
}
const APPROVAL_I18N_KEY: Record<string, string> = {
  pending: 'sessions.audit.approvalPending',
  approved: 'sessions.audit.approvalApproved',
  rejected: 'sessions.audit.approvalRejected',
  timeout: 'sessions.audit.approvalTimeout',
}

function statusBadgeClass(status: string): string {
  return STATUS_BADGE_CLASS[status] || 'badge-gray'
}
function statusLabel(status: string): string {
  const key = STATUS_I18N_KEY[status]
  // te() 检查键是否存在；不存在则回退到原始状态字符串（而非渲染 key 路径）
  return key && te(key) ? t(key) : status
}
function approvalBadgeClass(status: string): string {
  return APPROVAL_BADGE_CLASS[status] || 'badge-gray'
}
function approvalLabel(status: string): string {
  const key = APPROVAL_I18N_KEY[status]
  return key && te(key) ? t(key) : status || '-'
}

// 后端分数范围统一为 0-10（见 domains/sessionaudit/types.go 注释）。
// 阈值按 10 分制：>=8 绿、6-7 黄、<6 红。
function scoreColor(score: number, invert = false): string {
  const v = invert ? 10 - score : score
  if (v >= 8) return '#10b981'
  if (v >= 6) return '#f59e0b'
  return '#ef4444'
}

function fmtDate(s: string) {
  if (!s) return '-'
  // 跟随当前 i18n 语言区域，而非硬编码 zh-CN
  const locale = getLocale() || 'zh-CN'
  try {
    return new Date(s).toLocaleString(locale)
  } catch {
    return new Date(s).toLocaleString('zh-CN')
  }
}

onMounted(() => {
  load()
  loadStats()
})
</script>

<template>
  <div class="session-audit-view">
    <div class="view-header">
      <h2>{{ t('sessions.audit.title') }}</h2>
      <p class="view-subtitle">{{ t('sessions.audit.subtitle') }}</p>
    </div>

    <!-- 统计卡片 -->
    <div v-if="statsLoading" class="stats-loading" role="status" aria-live="polite">
      <span class="spinner" aria-hidden="true"></span>
      <span>{{ t('sessions.audit.statsLoading') }}</span>
    </div>
    <div v-else-if="statsError" class="error-banner" role="alert">
      <span class="error-icon" aria-hidden="true">⚠️</span>
      <span>{{ statsError }}</span>
    </div>
    <div v-else-if="stats" class="stats-grid">
      <div class="stat-card">
        <div class="stat-label">{{ t('sessions.audit.totalAudits') }}</div>
        <div class="stat-value">{{ stats.total }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('sessions.audit.averageScore') }}</div>
        <div class="stat-value" :style="{ color: scoreColor(stats.avg_score || 0) }">
          {{ (stats.avg_score || 0).toFixed(1) }}
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('sessions.audit.statusDistribution') }}</div>
        <div class="stat-breakdown">
          <span v-for="(count, status) in stats.by_status" :key="status" :class="statusBadgeClass(String(status))">
            {{ statusLabel(String(status)) }}: {{ count }}
          </span>
        </div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('sessions.audit.approvalStatus') }}</div>
        <div class="stat-breakdown">
          <span v-for="(count, status) in stats.by_approval" :key="status" :class="approvalBadgeClass(String(status))">
            {{ approvalLabel(String(status)) }}: {{ count }}
          </span>
        </div>
      </div>
    </div>

    <!-- 筛选区 -->
    <div class="filter-bar">
      <input
        v-model="filterTenantID"
        type="text"
        :placeholder="t('sessions.audit.filterTenantID')"
        :aria-label="t('sessions.audit.filterTenantID')"
        class="filter-input"
        @keyup.enter="resetPageAndLoad"
      />
      <input
        v-model="filterSessionID"
        type="text"
        :placeholder="t('sessions.audit.filterSessionID')"
        :aria-label="t('sessions.audit.filterSessionID')"
        class="filter-input"
        @keyup.enter="resetPageAndLoad"
      />
      <select
        v-model="filterStatus"
        class="filter-select"
        :aria-label="t('sessions.audit.filterStatus')"
        @change="resetPageAndLoad"
      >
        <option value="">{{ t('sessions.audit.allStatus') }}</option>
        <option value="pass">{{ t('sessions.audit.statusPass') }}</option>
        <option value="warn">{{ t('sessions.audit.statusWarn') }}</option>
        <option value="blocked">{{ t('sessions.audit.statusBlocked') }}</option>
        <option value="need_approval">{{ t('sessions.audit.statusNeedApproval') }}</option>
      </select>
      <button class="btn-primary" @click="resetPageAndLoad">{{ t('sessions.audit.search') }}</button>
      <button class="btn-secondary" @click="clearFilters">{{ t('sessions.audit.clear') }}</button>
    </div>

    <!-- 错误提示 -->
    <div v-if="error" class="error-banner" role="alert">
      <span class="error-icon" aria-hidden="true">⚠️</span>
      <span>{{ error }}</span>
      <button class="error-retry" @click="resetPageAndLoad" aria-label="重新加载">{{ t('sessions.audit.search') }}</button>
    </div>

    <!-- 记录列表 -->
    <div class="table-container">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('sessions.audit.tableHeaders.id') }}</th>
            <th>{{ t('sessions.audit.tableHeaders.sessionId') }}</th>
            <th>{{ t('sessions.audit.tableHeaders.tenant') }}</th>
            <th>{{ t('sessions.audit.tableHeaders.status') }}</th>
            <th>{{ t('sessions.audit.tableHeaders.approval') }}</th>
            <th>{{ t('sessions.audit.tableHeaders.securityScore') }}</th>
            <th>{{ t('sessions.audit.tableHeaders.dangerScore') }}</th>
            <th>{{ t('sessions.audit.tableHeaders.trustScore') }}</th>
            <th>{{ t('sessions.audit.tableHeaders.sensitiveScore') }}</th>
            <th>{{ t('sessions.audit.tableHeaders.detectScore') }}</th>
            <th>{{ t('sessions.audit.tableHeaders.threats') }}</th>
            <th>{{ t('sessions.audit.tableHeaders.createdAt') }}</th>
            <th>{{ t('sessions.audit.tableHeaders.actions') }}</th>
          </tr>
        </thead>
        <tbody v-if="loading">
          <tr>
            <td colspan="13" class="loading-cell" role="status" aria-live="polite">
              <span class="spinner" aria-hidden="true"></span>
              <span>{{ t('sessions.audit.loading') }}</span>
            </td>
          </tr>
        </tbody>
        <tbody v-else-if="records.length === 0">
          <tr>
            <td colspan="13" class="empty-cell">{{ t('sessions.audit.empty') }}</td>
          </tr>
        </tbody>
        <tbody v-else>
          <tr v-for="rec in records" :key="rec.id" class="data-row">
            <td>{{ rec.id }}</td>
            <td class="session-id">{{ rec.session_id }}</td>
            <td>{{ rec.tenant_id }}</td>
            <td>
              <span :class="statusBadgeClass(rec.status)">{{ statusLabel(rec.status) }}</span>
            </td>
            <td>
              <span v-if="rec.approval_status" :class="approvalBadgeClass(rec.approval_status)">
                {{ approvalLabel(rec.approval_status) }}
              </span>
              <span v-else>-</span>
            </td>
            <td :style="{ color: scoreColor(rec.scores.security) }">{{ rec.scores.security }}</td>
            <td :style="{ color: scoreColor(rec.scores.danger, true) }">{{ rec.scores.danger }}</td>
            <td :style="{ color: scoreColor(rec.scores.trust) }">{{ rec.scores.trust }}</td>
            <td :style="{ color: scoreColor(rec.scores.sensitive, true) }">{{ rec.scores.sensitive }}</td>
            <td v-if="rec.detect_result" :style="{ color: scoreColor(rec.detect_result.score) }">
              {{ rec.detect_result.score }}
            </td>
            <td v-else>-</td>
            <td>
              <span v-if="rec.detect_result?.threats" class="badge-red">
                {{ rec.detect_result.threats.length }}
              </span>
              <span v-else>0</span>
            </td>
            <td class="date-cell">{{ fmtDate(rec.created_at) }}</td>
            <td>
              <button class="btn-link" @click="showDetail(rec)">{{ t('sessions.audit.actionDetail') }}</button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 分页 -->
    <div class="pagination">
      <button class="btn-secondary" :disabled="page <= 1" @click="changePage(-1)">
        {{ t('sessions.audit.pagination.previous') }}
      </button>
      <span class="page-info">
        {{ t('sessions.audit.pagination.info', { current: page, total: totalPages, count: total }) }}
      </span>
      <button class="btn-secondary" :disabled="page >= totalPages" @click="changePage(1)">
        {{ t('sessions.audit.pagination.next') }}
      </button>
    </div>

    <!-- 详情弹窗 -->
    <div
      v-if="detailVisible"
      class="modal-overlay"
      role="dialog"
      aria-modal="true"
      :aria-labelledby="`audit-detail-title-${detailRecord?.id ?? ''}`"
      @click.self="closeDetail"
      @keydown.esc="closeDetail"
    >
      <div class="modal-content detail-modal">
        <div class="modal-header">
          <h3 :id="`audit-detail-title-${detailRecord?.id ?? ''}`">
            {{ t('sessions.audit.detail.title', { id: detailRecord?.id }) }}
          </h3>
          <button class="close-btn" :aria-label="t('sessions.audit.detail.close')" @click="closeDetail">✕</button>
        </div>
        <div v-if="detailRecord" class="modal-body">
          <div class="detail-section">
            <h4>{{ t('sessions.audit.detail.basicInfo') }}</h4>
            <div class="detail-grid">
              <div><strong>{{ t('sessions.audit.detail.sessionId') }}:</strong> {{ detailRecord.session_id }}</div>
              <div><strong>{{ t('sessions.audit.detail.tenantId') }}:</strong> {{ detailRecord.tenant_id }}</div>
              <div><strong>{{ t('sessions.audit.detail.status') }}:</strong> <span :class="statusBadgeClass(detailRecord.status)">{{ statusLabel(detailRecord.status) }}</span></div>
              <div v-if="detailRecord.approval_status">
                <strong>{{ t('sessions.audit.detail.approval') }}:</strong> <span :class="approvalBadgeClass(detailRecord.approval_status)">{{ approvalLabel(detailRecord.approval_status) }}</span>
              </div>
              <div><strong>{{ t('sessions.audit.detail.createdAt') }}:</strong> {{ fmtDate(detailRecord.created_at) }}</div>
            </div>
          </div>

          <div class="detail-section">
            <h4>{{ t('sessions.audit.detail.clientInfo') }}</h4>
            <div class="detail-grid">
              <div><strong>{{ t('sessions.audit.detail.ip') }}:</strong> {{ detailRecord.client_info.ip || '-' }}</div>
              <div><strong>{{ t('sessions.audit.detail.model') }}:</strong> {{ detailRecord.client_info.model || '-' }}</div>
              <div><strong>{{ t('sessions.audit.detail.userAgent') }}:</strong> {{ detailRecord.client_info.user_agent || '-' }}</div>
              <div><strong>{{ t('sessions.audit.detail.agent') }}:</strong> {{ detailRecord.client_info.agent || '-' }}</div>
              <div><strong>{{ t('sessions.audit.detail.deviceSeed') }}:</strong> <code>{{ detailRecord.client_info.device_seed || '-' }}</code></div>
            </div>
          </div>

          <div v-if="detailRecord.summary" class="detail-section">
            <h4>{{ t('sessions.audit.detail.contentSummary') }}</h4>
            <div><strong>{{ t('sessions.audit.detail.summaryTitle') }}:</strong> {{ detailRecord.summary.title }}</div>
            <div><strong>{{ t('sessions.audit.detail.contentHash') }}:</strong> <code>{{ detailRecord.summary.content_hash }}</code></div>
          </div>

          <div v-if="detailRecord.intent" class="detail-section">
            <h4>{{ t('sessions.audit.detail.intentAnalysis') }}</h4>
            <div class="detail-grid">
              <div><strong>{{ t('sessions.audit.detail.type') }}:</strong> {{ detailRecord.intent.type }}</div>
              <div><strong>{{ t('sessions.audit.detail.score') }}:</strong> {{ detailRecord.intent.score.toFixed(2) }}</div>
              <div style="grid-column: 1 / -1"><strong>{{ t('sessions.audit.detail.reason') }}:</strong> {{ detailRecord.intent.reason }}</div>
            </div>
          </div>

          <div class="detail-section">
            <h4>{{ t('sessions.audit.detail.scores') }}</h4>
            <div class="score-grid">
              <div class="score-item">
                <div class="score-label">{{ t('sessions.audit.detail.security') }}</div>
                <div class="score-value" :style="{ color: scoreColor(detailRecord.scores.security) }">
                  {{ detailRecord.scores.security }}
                </div>
              </div>
              <div class="score-item">
                <div class="score-label">{{ t('sessions.audit.detail.danger') }}</div>
                <div class="score-value" :style="{ color: scoreColor(detailRecord.scores.danger, true) }">
                  {{ detailRecord.scores.danger }}
                </div>
              </div>
              <div class="score-item">
                <div class="score-label">{{ t('sessions.audit.detail.trust') }}</div>
                <div class="score-value" :style="{ color: scoreColor(detailRecord.scores.trust) }">
                  {{ detailRecord.scores.trust }}
                </div>
              </div>
              <div class="score-item">
                <div class="score-label">{{ t('sessions.audit.detail.sensitive') }}</div>
                <div class="score-value" :style="{ color: scoreColor(detailRecord.scores.sensitive, true) }">
                  {{ detailRecord.scores.sensitive }}
                </div>
              </div>
            </div>
          </div>

          <div v-if="detailRecord.detect_result" class="detail-section">
            <h4>{{ t('sessions.audit.detail.detectResult') }}</h4>
            <div class="detail-grid">
              <div><strong>{{ t('sessions.audit.detail.detectScore') }}:</strong> <span :style="{ color: scoreColor(detailRecord.detect_result.score) }">{{ detailRecord.detect_result.score }}</span></div>
              <div><strong>{{ t('sessions.audit.detail.decision') }}:</strong> {{ detailRecord.detect_result.decision }}</div>
            </div>
            <div v-if="detailRecord.detect_result.threats && detailRecord.detect_result.threats.length > 0">
              <strong>{{ t('sessions.audit.detail.threats') }}:</strong>
              <ul class="threat-list">
                <li v-for="(threat, idx) in detailRecord.detect_result.threats" :key="idx">
                  <span class="badge-red">{{ threat.type }}</span>
                  <span class="badge-gray">{{ t('sessions.audit.detail.severity') }}: {{ threat.severity }}/10</span>
                  <span v-if="threat.evidence" class="threat-evidence">{{ threat.evidence }}</span>
                </li>
              </ul>
            </div>
            <div v-if="detailRecord.detect_result.sensitive_words && detailRecord.detect_result.sensitive_words.length > 0">
              <strong>{{ t('sessions.audit.detail.sensitiveWords') }}:</strong>
              <div class="sensitive-words">
                <span v-for="(word, idx) in detailRecord.detect_result.sensitive_words" :key="idx" class="badge-yellow">
                  {{ word }}
                </span>
              </div>
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn-secondary" @click="closeDetail">{{ t('sessions.audit.detail.close') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.session-audit-view {
  padding: 1.5rem;
  max-width: 1600px;
  margin: 0 auto;
}

.view-header h2 {
  margin: 0 0 0.5rem;
  font-size: 1.75rem;
  font-weight: 600;
}

.view-subtitle {
  color: #666;
  margin: 0 0 1.5rem;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 1rem;
  margin-bottom: 1.5rem;
}

.stat-card {
  background: white;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  padding: 1rem;
}

.stat-label {
  font-size: 0.875rem;
  color: #666;
  margin-bottom: 0.5rem;
}

.stat-value {
  font-size: 2rem;
  font-weight: 600;
  color: #111;
}

.stat-breakdown {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  margin-top: 0.5rem;
}

.filter-bar {
  display: flex;
  gap: 0.75rem;
  margin-bottom: 1.5rem;
  flex-wrap: wrap;
}

.filter-input,
.filter-select {
  padding: 0.5rem 0.75rem;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 0.875rem;
  min-width: 150px;
}

.filter-input:focus,
.filter-select:focus {
  outline: none;
  border-color: #3b82f6;
}

.error-banner {
  background: #fef2f2;
  border: 1px solid #fecaca;
  color: #dc2626;
  padding: 0.75rem 1rem;
  border-radius: 6px;
  margin-bottom: 1rem;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.error-icon { font-size: 16px; flex-shrink: 0; }

.error-retry {
  margin-left: auto;
  background: white;
  border: 1px solid #fecaca;
  color: #dc2626;
  padding: 0.25rem 0.75rem;
  border-radius: 4px;
  cursor: pointer;
  font-size: 0.8125rem;
}

.error-retry:hover { background: #fee2e2; }

.stats-loading {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 1rem;
  color: #666;
  font-size: 0.875rem;
}

.spinner {
  display: inline-block;
  width: 14px;
  height: 14px;
  border: 2px solid #e5e7eb;
  border-top-color: #3b82f6;
  border-radius: 50%;
  animation: audit-spin 0.8s linear infinite;
  flex-shrink: 0;
}

@keyframes audit-spin { to { transform: rotate(360deg); } }

.table-container {
  background: white;
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  overflow-x: auto;
  margin-bottom: 1rem;
}

.data-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.875rem;
}

.data-table th {
  background: #f9fafb;
  padding: 0.75rem 1rem;
  text-align: left;
  font-weight: 600;
  border-bottom: 1px solid #e5e7eb;
  white-space: nowrap;
}

.data-table td {
  padding: 0.75rem 1rem;
  border-bottom: 1px solid #f3f4f6;
}

.data-row:hover {
  background: #f9fafb;
}

.session-id {
  font-family: 'Monaco', 'Menlo', monospace;
  font-size: 0.8rem;
  max-width: 200px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.date-cell {
  white-space: nowrap;
  font-size: 0.8rem;
  color: #666;
}

.loading-cell,
.empty-cell {
  text-align: center;
  padding: 2rem;
  color: #999;
}

.loading-cell {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 10px;
}

.pagination {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 1rem;
}

.page-info {
  font-size: 0.875rem;
  color: #666;
}

/* Badges */
.badge-green,
.badge-yellow,
.badge-red,
.badge-blue,
.badge-gray {
  display: inline-block;
  padding: 0.25rem 0.5rem;
  border-radius: 4px;
  font-size: 0.75rem;
  font-weight: 500;
}

.badge-green {
  background: #d1fae5;
  color: #065f46;
}

.badge-yellow {
  background: #fef3c7;
  color: #92400e;
}

.badge-red {
  background: #fee2e2;
  color: #991b1b;
}

.badge-blue {
  background: #dbeafe;
  color: #1e40af;
}

.badge-gray {
  background: #f3f4f6;
  color: #374151;
}

/* Buttons */
.btn-primary,
.btn-secondary,
.btn-link {
  padding: 0.5rem 1rem;
  border: none;
  border-radius: 6px;
  cursor: pointer;
  font-size: 0.875rem;
  font-weight: 500;
  transition: all 0.2s;
}

.btn-primary {
  background: #3b82f6;
  color: white;
}

.btn-primary:hover {
  background: #2563eb;
}

.btn-secondary {
  background: white;
  color: #374151;
  border: 1px solid #d1d5db;
}

.btn-secondary:hover {
  background: #f9fafb;
}

.btn-secondary:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-link {
  background: none;
  color: #3b82f6;
  padding: 0.25rem 0.5rem;
}

.btn-link:hover {
  text-decoration: underline;
}

/* Modal */
.modal-overlay {
  position: fixed;
  top: 0;
  left: 0;
  right: 0;
  bottom: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
  padding: 1rem;
}

.modal-content {
  background: white;
  border-radius: 12px;
  max-width: 900px;
  width: 100%;
  max-height: 90vh;
  overflow: hidden;
  display: flex;
  flex-direction: column;
}

.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 1.5rem;
  border-bottom: 1px solid #e5e7eb;
}

.modal-header h3 {
  margin: 0;
  font-size: 1.25rem;
}

.close-btn {
  background: none;
  border: none;
  font-size: 1.5rem;
  cursor: pointer;
  color: #999;
  padding: 0;
  width: 2rem;
  height: 2rem;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 4px;
}

.close-btn:hover {
  background: #f3f4f6;
}

.modal-body {
  padding: 1.5rem;
  overflow-y: auto;
  flex: 1;
}

.modal-footer {
  padding: 1rem 1.5rem;
  border-top: 1px solid #e5e7eb;
  display: flex;
  justify-content: flex-end;
  gap: 0.75rem;
}

.detail-section {
  margin-bottom: 1.5rem;
}

.detail-section h4 {
  margin: 0 0 1rem;
  font-size: 1rem;
  font-weight: 600;
  color: #374151;
}

.detail-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
  gap: 0.75rem;
  font-size: 0.875rem;
}

.detail-grid strong {
  color: #666;
  font-weight: 500;
}

.detail-grid code {
  background: #f3f4f6;
  padding: 0.125rem 0.25rem;
  border-radius: 3px;
  font-size: 0.8rem;
}

.score-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 1rem;
}

.score-item {
  text-align: center;
  padding: 1rem;
  background: #f9fafb;
  border-radius: 8px;
}

.score-label {
  font-size: 0.875rem;
  color: #666;
  margin-bottom: 0.5rem;
}

.score-value {
  font-size: 1.5rem;
  font-weight: 600;
}

.threat-list {
  list-style: none;
  padding: 0;
  margin: 0.5rem 0 0;
}

.threat-list li {
  padding: 0.5rem;
  background: #fef2f2;
  border-radius: 4px;
  margin-bottom: 0.5rem;
  font-size: 0.875rem;
}

.threat-list li span {
  margin-right: 0.5rem;
}

.sensitive-words {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
  margin-top: 0.5rem;
}
</style>

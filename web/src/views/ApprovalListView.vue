<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { localeRef } from '../i18n'
import { useRouter } from 'vue-router'
import { getApprovalList, approveApproval, rejectApproval, getApprovalStats, type ApprovalItem, type ApprovalStats } from '../api/approval'
import { isSuperAdmin } from '../store'
import { confirmDialog } from '../composables/useConfirmDialog'
import AppSpinner from '../components/AppSpinner.vue'
import EmptyState from '../components/EmptyState.vue'

const { t } = useI18n()
const router = useRouter()

// State
const loading = ref(false)
const approvals = ref<ApprovalItem[]>([])
const stats = ref<ApprovalStats | null>(null)
const error = ref<string | null>(null)
const successMessage = ref<string | null>(null)

// Filters
const statusFilter = ref<string>('pending')
const riskLevelFilter = ref<string>('')
const searchQuery = ref('')
const dateRangeStart = ref('')
const dateRangeEnd = ref('')

// Pagination
const currentPage = ref(1)
const pageSize = ref(20)
const totalItems = ref(0)
const totalPages = ref(1)

// Sorting
const sortBy = ref('created_at')
const sortOrder = ref<'asc' | 'desc'>('desc')

// Real-time updates
let refreshInterval: number | undefined

const statusOptions = computed(() => [
  { value: '', label: t('approval.list.filter.allStatus') },
  { value: 'pending', label: t('approval.list.status.pending') },
  { value: 'approved', label: t('approval.list.status.approved') },
  { value: 'rejected', label: t('approval.list.status.rejected') },
  { value: 'timeout', label: t('approval.list.status.timeout') },
])

const riskLevelOptions = computed(() => [
  { value: '', label: t('approval.list.filter.allRisk') },
  { value: 'LOW', label: t('approval.list.risk.LOW') },
  { value: 'MEDIUM', label: t('approval.list.risk.MEDIUM') },
  { value: 'HIGH', label: t('approval.list.risk.HIGH') },
  { value: 'CRITICAL', label: t('approval.list.risk.CRITICAL') },
])

const filteredApprovals = computed(() => {
  let list = approvals.value

  // Search by session_id or request_id
  if (searchQuery.value) {
    const query = searchQuery.value.toLowerCase().trim()
    list = list.filter(item =>
      item.session_id.toLowerCase().includes(query) ||
      item.request_id.toLowerCase().includes(query)
    )
  }

  return list
})

function getRiskLevelColor(level: string): string {
  switch (level?.toUpperCase()) {
    case 'LOW': return 'green'
    case 'MEDIUM': return 'yellow'
    case 'HIGH': return 'orange'
    case 'CRITICAL': return 'red'
    default: return 'gray'
  }
}

function getRiskLevelLabel(level: string): string {
  const key = `approval.list.risk.${level?.toUpperCase()}`
  const mapped = level?.toUpperCase()
  if (mapped === 'LOW' || mapped === 'MEDIUM' || mapped === 'HIGH' || mapped === 'CRITICAL') {
    return t(key as 'approval.list.risk.LOW')
  }
  return level || '-'
}

function getStatusColor(status: string): string {
  switch (status) {
    case 'pending': return 'yellow'
    case 'approved': return 'green'
    case 'rejected': return 'red'
    case 'timeout': return 'gray'
    default: return 'gray'
  }
}

function getStatusLabel(status: string): string {
  const labels: Record<string, string> = {
    pending: t('approval.list.status.pending'),
    approved: t('approval.list.status.approved'),
    rejected: t('approval.list.status.rejected'),
    timeout: t('approval.list.status.timeout'),
  }
  return labels[status] || status
}

function formatDate(dateStr: string): string {
  const date = new Date(dateStr)
  const now = new Date()
  const diff = now.getTime() - date.getTime()
  const seconds = Math.floor(diff / 1000)
  const minutes = Math.floor(seconds / 60)
  const hours = Math.floor(minutes / 60)
  const days = Math.floor(hours / 24)

  if (days > 7) {
    return date.toLocaleDateString(localeRef.value, {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit'
    })
  }
  if (days > 0) return t('approval.list.relativeTime.daysAgo', { n: days })
  if (hours > 0) return t('approval.list.relativeTime.hoursAgo', { n: hours })
  if (minutes > 0) return t('approval.list.relativeTime.minutesAgo', { n: minutes })
  return t('approval.list.relativeTime.justNow')
}

function formatCost(cost?: number): string {
  if (!cost) return '-'
  return `¥${cost.toFixed(4)}`
}

async function loadApprovals() {
  loading.value = true
  error.value = null

  try {
    const params = {
      status: statusFilter.value || undefined,
      risk_level: riskLevelFilter.value || undefined,
      page: currentPage.value,
      page_size: pageSize.value,
      sort_by: sortBy.value,
      sort_order: sortOrder.value,
      created_after: dateRangeStart.value || undefined,
      created_before: dateRangeEnd.value || undefined,
    }

    const response = await getApprovalList(params)
    approvals.value = response.items || []
    totalItems.value = response.total
    totalPages.value = response.total_pages
    currentPage.value = response.page
  } catch (e: any) {
    error.value = e.message || t('approval.list.errors.loadListFailed')
  } finally {
    loading.value = false
  }
}

async function loadStats() {
  try {
    stats.value = await getApprovalStats()
  } catch (e: any) {
    console.error('Failed to load stats:', e)
  }
}

async function quickApprove(item: ApprovalItem) {
  if (!(await confirmDialog(t('approval.list.confirm.approve', { id: item.request_id })))) {
    return
  }

  try {
    await approveApproval(item.request_id)
    successMessage.value = t('approval.list.success.approved')
    setTimeout(() => successMessage.value = null, 3000)
    await loadApprovals()
    await loadStats()
  } catch (e: any) {
    error.value = e.message || t('approval.list.errors.approveFailed')
  }
}

async function quickReject(item: ApprovalItem) {
  const reason = prompt(t('approval.list.confirm.rejectPrompt'))
  if (!reason || !reason.trim()) {
    return
  }

  try {
    await rejectApproval(item.request_id, reason)
    successMessage.value = t('approval.list.success.rejected')
    setTimeout(() => successMessage.value = null, 3000)
    await loadApprovals()
    await loadStats()
  } catch (e: any) {
    error.value = e.message || t('approval.list.errors.rejectFailed')
  }
}

function viewDetail(item: ApprovalItem) {
  router.push(`/admin/approvals/${item.request_id}`)
}

function resetFilters() {
  statusFilter.value = 'pending'
  riskLevelFilter.value = ''
  searchQuery.value = ''
  dateRangeStart.value = ''
  dateRangeEnd.value = ''
  currentPage.value = 1
  loadApprovals()
}

function changePage(page: number) {
  currentPage.value = page
  loadApprovals()
}

function startAutoRefresh() {
  refreshInterval = window.setInterval(() => {
    if (statusFilter.value === 'pending') {
      loadApprovals()
      loadStats()
    }
  }, 30000) // Refresh every 30 seconds
}

function stopAutoRefresh() {
  if (refreshInterval) {
    clearInterval(refreshInterval)
    refreshInterval = undefined
  }
}

onMounted(() => {
  loadApprovals()
  loadStats()
  startAutoRefresh()
})

onBeforeUnmount(() => {
  stopAutoRefresh()
})

// Watch filters
watch([statusFilter, riskLevelFilter, dateRangeStart, dateRangeEnd], () => {
  currentPage.value = 1
  loadApprovals()
})
</script>

<template>
  <div class="approval-list-view">
    <!-- Header -->
    <div class="page-header">
      <div>
        <h1>{{ t('approval.list.title') }}</h1>
        <p class="page-description">{{ t('approval.list.description') }}</p>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary" @click="loadApprovals" :disabled="loading">
          {{ loading ? t('approval.list.refreshing') : t('approval.list.refresh') }}
        </button>
        <button class="btn btn-secondary" @click="router.push('/admin/approval-config')">
          {{ t('approval.list.config') }}
        </button>
      </div>
    </div>

    <!-- Messages -->
    <div v-if="error" class="message message-error">
      <span class="message-icon">❌</span>
      {{ error }}
      <button class="message-close" @click="error = null">×</button>
    </div>

    <div v-if="successMessage" class="message message-success">
      <span class="message-icon">✅</span>
      {{ successMessage }}
      <button class="message-close" @click="successMessage = null">×</button>
    </div>

    <!-- Stats Cards -->
    <div v-if="stats" class="stats-grid">
      <div class="stat-card">
        <div class="stat-label">{{ t('approval.list.stats.pending') }}</div>
        <div class="stat-value" :class="{ 'stat-highlight': stats.pending > 0 }">{{ stats.pending }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('approval.list.stats.todayTotal') }}</div>
        <div class="stat-value">{{ stats.today_total }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('approval.list.stats.approved') }}</div>
        <div class="stat-value stat-green">{{ stats.approved }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('approval.list.stats.rejected') }}</div>
        <div class="stat-value stat-red">{{ stats.rejected }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">{{ t('approval.list.stats.avgTime') }}</div>
        <div class="stat-value stat-small">{{ Math.round(stats.avg_approval_time_seconds / 60) }}m</div>
      </div>
    </div>

    <!-- Filters -->
    <div class="filters-section">
      <div class="filters-row">
        <div class="filter-group">
          <label>{{ t('approval.list.filter.status') }}</label>
          <select v-model="statusFilter" class="form-select">
            <option v-for="opt in statusOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </option>
          </select>
        </div>

        <div class="filter-group">
          <label>{{ t('approval.list.filter.riskLevel') }}</label>
          <select v-model="riskLevelFilter" class="form-select">
            <option v-for="opt in riskLevelOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </option>
          </select>
        </div>

        <div class="filter-group filter-group-grow">
          <label>{{ t('approval.list.filter.search') }}</label>
          <input
            v-model="searchQuery"
            type="text"
            class="form-input"
            :placeholder="t('approval.list.filter.searchPlaceholder')"
          />
        </div>

        <div class="filter-actions">
          <button class="btn btn-secondary btn-sm" @click="resetFilters">
            {{ t('approval.list.filter.reset') }}
          </button>
        </div>
      </div>
    </div>

    <!-- Table -->
    <div class="table-container">
      <AppSpinner v-if="loading && approvals.length === 0" :label="t('approval.list.loading')" />

      <EmptyState v-else-if="filteredApprovals.length === 0" padding="64px">
        <p>{{ t('approval.list.empty') }}</p>
      </EmptyState>

      <table v-else class="data-table">
        <thead>
          <tr>
            <th>{{ t('approval.list.table.requestId') }}</th>
            <th>{{ t('approval.list.table.sessionId') }}</th>
            <th>{{ t('approval.list.table.riskLevel') }}</th>
            <th>{{ t('approval.list.table.trigger') }}</th>
            <th>{{ t('approval.list.table.cost') }}</th>
            <th>{{ t('approval.list.table.createdAt') }}</th>
            <th>{{ t('approval.list.table.status') }}</th>
            <th class="actions-column">{{ t('approval.list.table.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in filteredApprovals" :key="item.id" class="table-row">
            <td>
              <button class="link-button" @click="viewDetail(item)">
                {{ item.request_id.slice(0, 12) }}...
              </button>
            </td>
            <td>
              <span class="text-mono">{{ item.session_id.slice(0, 12) }}...</span>
            </td>
            <td>
              <span class="badge" :class="`badge-${getRiskLevelColor(item.risk_level)}`">
                {{ getRiskLevelLabel(item.risk_level) }}
              </span>
            </td>
            <td>
              <span class="trigger-reason">{{ item.trigger_type || '-' }}</span>
            </td>
            <td>
              {{ formatCost(item.detect_result?.cost_estimation) }}
            </td>
            <td>
              <span class="text-secondary" :title="item.created_at">
                {{ formatDate(item.created_at) }}
              </span>
              <div v-if="item.time_left && item.status === 'pending'" class="time-left">
                {{ t('approval.list.timeLeft', { time: item.time_left }) }}
              </div>
            </td>
            <td>
              <span class="badge" :class="`badge-${getStatusColor(item.status)}`">
                {{ getStatusLabel(item.status) }}
              </span>
            </td>
            <td class="actions-cell">
              <div class="action-buttons">
                <button
                  v-if="item.status === 'pending'"
                  class="btn btn-success btn-xs"
                  @click="quickApprove(item)"
                  :title="t('approval.list.actions.approve')"
                >
                  ✓
                </button>
                <button
                  v-if="item.status === 'pending'"
                  class="btn btn-danger btn-xs"
                  @click="quickReject(item)"
                  :title="t('approval.list.actions.reject')"
                >
                  ✕
                </button>
                <button
                  class="btn btn-secondary btn-xs"
                  @click="viewDetail(item)"
                  :title="t('approval.list.actions.viewDetail')"
                >
                  👁
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- Pagination -->
    <div v-if="totalPages > 1" class="pagination">
      <button
        class="btn btn-secondary btn-sm"
        @click="changePage(currentPage - 1)"
        :disabled="currentPage === 1"
      >
        {{ t('approval.list.pagination.previous') }}
      </button>

      <div class="pagination-info">
        {{ t('approval.list.pagination.info', { page: currentPage, totalPages, total: totalItems }) }}
      </div>

      <button
        class="btn btn-secondary btn-sm"
        @click="changePage(currentPage + 1)"
        :disabled="currentPage === totalPages"
      >
        {{ t('approval.list.pagination.next') }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.approval-list-view {
  padding: 20px;
  max-width: 1600px;
  margin: 0 auto;
  color: var(--text-primary);
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  margin-bottom: 24px;
}

.page-header h1 {
  margin: 0 0 8px;
  font-size: 28px;
  font-weight: 600;
}

.page-description {
  margin: 0;
  font-size: 14px;
  color: var(--text-secondary);
}

.header-actions {
  display: flex;
  gap: 12px;
}

.message {
  padding: 12px 16px;
  border-radius: 6px;
  margin-bottom: 16px;
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  position: relative;
}

.message-icon {
  font-size: 16px;
}

.message-close {
  margin-left: auto;
  background: none;
  border: none;
  color: inherit;
  font-size: 20px;
  cursor: pointer;
  padding: 0 4px;
  opacity: 0.7;
}

.message-close:hover {
  opacity: 1;
}

.message-error {
  background: color-mix(in srgb, var(--danger) 12%, transparent);
  border: 1px solid color-mix(in srgb, var(--danger) 12%, transparent);
  color: var(--danger);
}

.message-success {
  background: var(--success-bg);
  border: 1px solid var(--success-bd);
  color: var(--success);
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 16px;
  margin-bottom: 24px;
}

.stat-card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
  text-align: center;
}

.stat-label {
  font-size: 13px;
  color: var(--text-secondary);
  margin-bottom: 8px;
}

.stat-value {
  font-size: 32px;
  font-weight: 600;
  color: var(--text-primary);
}

.stat-value.stat-small {
  font-size: 24px;
}

.stat-value.stat-highlight {
  color: var(--warning);
}

.stat-value.stat-green {
  color: var(--success);
}

.stat-value.stat-red {
  color: var(--danger);
}

.filters-section {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
  margin-bottom: 16px;
}

.filters-row {
  display: flex;
  gap: 12px;
  align-items: flex-end;
}

.filter-group {
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 150px;
}

.filter-group-grow {
  flex: 1;
}

.filter-group label {
  font-size: 13px;
  color: var(--text-secondary);
  font-weight: 500;
}

.filter-actions {
  display: flex;
  gap: 8px;
}

.form-select,
.form-input {
  padding: 8px 12px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text-primary);
  font-size: 14px;
}

.form-select:focus,
.form-input:focus {
  outline: none;
  border-color: var(--accent);
}

.table-container {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: hidden;
  margin-bottom: 16px;
}

.loading-state,
.empty-state {
  padding: 64px;
  text-align: center;
  color: var(--text-secondary);
}

.empty-icon {
  font-size: 48px;
  margin-bottom: 16px;
}

.data-table {
  width: 100%;
  border-collapse: collapse;
}

.data-table thead {
  background: var(--bg);
  border-bottom: 1px solid var(--border);
}

.data-table th {
  padding: 12px 16px;
  text-align: left;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-secondary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.data-table td {
  padding: 12px 16px;
  border-top: 1px solid var(--border);
  font-size: 14px;
}

.table-row:hover {
  background: var(--bg-hover);
}

.link-button {
  background: none;
  border: none;
  color: var(--accent);
  cursor: pointer;
  text-decoration: underline;
  font-size: 14px;
  padding: 0;
}

.link-button:hover {
  color: var(--accent);
}

.text-mono {
  font-family: 'Monaco', 'Menlo', monospace;
  font-size: 13px;
  color: var(--text-secondary);
}

.text-secondary {
  color: var(--text-secondary);
}

.time-left {
  font-size: 11px;
  color: var(--warning);
  margin-top: 2px;
}

.trigger-reason {
  font-size: 13px;
  color: var(--text-primary);
}

.badge {
  display: inline-block;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  text-transform: uppercase;
}

.badge-green {
  background: var(--success-bg);
  color: var(--success);
}

.badge-yellow {
  background: var(--warning-bg);
  color: var(--warning);
}

.badge-orange {
  background: var(--warning-bd);
  color: var(--warning);
}

.badge-red {
  background: color-mix(in srgb, var(--danger) 12%, transparent);
  color: var(--danger);
}

.badge-gray {
  background: var(--neutral-bg);
  color: var(--muted);
}

.actions-column {
  width: 140px;
}

.actions-cell {
  text-align: center;
}

.action-buttons {
  display: flex;
  gap: 6px;
  justify-content: center;
}

.pagination {
  display: flex;
  justify-content: center;
  align-items: center;
  gap: 16px;
  padding: 16px;
}

.pagination-info {
  font-size: 14px;
  color: var(--text-secondary);
}

.btn {
  padding: 8px 16px;
  border-radius: 6px;
  font-size: 14px;
  cursor: pointer;
  border: 1px solid transparent;
  transition: all 0.2s;
  font-weight: 500;
  background: var(--bg-card);
  color: var(--text-primary);
  border-color: var(--border);
}

.btn:hover:not(:disabled) {
  background: var(--bg);
  border-color: var(--accent);
}

.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-primary {
  background: var(--accent);
  color: var(--on-primary);
  border-color: var(--accent);
}

.btn-primary:hover:not(:disabled) {
  background: var(--accent);
}

.btn-secondary {
  background: var(--bg-card);
  border-color: var(--border);
}

.btn-success {
  background: var(--success-bg);
  color: var(--success);
  border-color: var(--success);
}

.btn-success:hover:not(:disabled) {
  background: var(--success-bd);
}

.btn-danger {
  background: color-mix(in srgb, var(--danger) 12%, transparent);
  color: var(--danger);
  border-color: var(--danger);
}

.btn-danger:hover:not(:disabled) {
  background: color-mix(in srgb, var(--danger) 12%, transparent);
}

.btn-sm {
  padding: 6px 12px;
  font-size: 13px;
}

.btn-xs {
  padding: 4px 8px;
  font-size: 12px;
  min-width: 32px;
}

@media (max-width: 1200px) {
  .stats-grid {
    grid-template-columns: repeat(3, 1fr);
  }
}

@media (max-width: 768px) {
  .page-header {
    flex-direction: column;
    gap: 16px;
  }

  .filters-row {
    flex-direction: column;
    align-items: stretch;
  }

  .filter-group {
    min-width: 0;
  }

  .stats-grid {
    grid-template-columns: repeat(2, 1fr);
  }

  .data-table {
    font-size: 12px;
  }

  .data-table th,
  .data-table td {
    padding: 8px;
  }
}
</style>

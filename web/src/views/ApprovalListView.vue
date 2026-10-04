<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { localeRef } from '../i18n'
import { useRouter } from 'vue-router'
import { getApprovalList, approveApproval, rejectApproval, getApprovalStats, type ApprovalItem, type ApprovalStats } from '../api/approval'
import { isSuperAdmin } from '../store'
import { confirmDialog } from '../composables/useConfirmDialog'
import { formatDateTime, formatRelativeTime } from '../utils/datetime'
import { useActionMessage } from '../composables/useActionMessage'
// 2026-10-04 H6 第三条切片：呈现形态与加载方式是**两个独立维度**（规范 03 §1 / 13 §1）。
//   桌面 → 表格 + 页码（既有逻辑与 DOM 逐字保留）
//   compact → 卡片 + 连续加载
import { useWindowClass } from '../composables/useWindowClass'
import { createHyperPages } from '../lib/shell/hyper/hyperPages'
import ResponsiveDataView from '../components/ui/ResponsiveDataView.vue'
import HyperLoadMore from '../components/ui/HyperLoadMore.vue'
import type { CardField } from '../components/ui/CardList.vue'

const { t } = useI18n()
const router = useRouter()
const { isCompact } = useWindowClass()

// State
const loading = ref(false)
const approvals = ref<ApprovalItem[]>([])
const stats = ref<ApprovalStats | null>(null)
// 审计 R3#10：操作反馈条统一走 useActionMessage（自动消失/计时器清理收敛）。
const { message: successMessage, error, notifySuccess, notifyError, clearMessage, clearError } = useActionMessage()

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

/**
 * 搜索是**当前结果集内**的客户端过滤（既有行为，服务端不分页搜索）。
 * 抽成函数是为了让页码路径与连续加载路径共用同一套语义 ——
 * 各写一份的话，改搜索条件会漏改其中一条路径。
 */
function applySearch(list: ApprovalItem[]): ApprovalItem[] {
  const query = searchQuery.value.toLowerCase().trim()
  if (!query) return list
  return list.filter(
    (item) => item.session_id.toLowerCase().includes(query) || item.request_id.toLowerCase().includes(query),
  )
}

const filteredApprovals = computed(() => applySearch(approvals.value))

/**
 * 筛选条件的**唯一真源**。页码路径与连续加载路径都从这里取 ——
 * 各写一份必然漂移，症状是「桌面筛了，compact 没筛」。
 * 不含 page / page_size：那是加载方式，不是筛选条件。
 */
function filterBody() {
  return {
    status: statusFilter.value || undefined,
    risk_level: riskLevelFilter.value || undefined,
    sort_by: sortBy.value,
    sort_order: sortOrder.value,
    created_after: dateRangeStart.value || undefined,
    created_before: dateRangeEnd.value || undefined,
  }
}

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

// 审计 R3#10：相对时间统一走 utils/datetime.formatRelativeTime
//（超过 7 天回退绝对时间，与原实现一致）。
function formatDate(dateStr: string): string {
  return formatRelativeTime(dateStr, {
    justNow: t('approval.list.relativeTime.justNow'),
    minutesAgo: (n) => t('approval.list.relativeTime.minutesAgo', { n }),
    hoursAgo: (n) => t('approval.list.relativeTime.hoursAgo', { n }),
    daysAgo: (n) => t('approval.list.relativeTime.daysAgo', { n }),
    older: (d) => formatDateTime(d, {
      locale: localeRef.value,
      options: {
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
      },
    }),
  })
}

function formatCost(cost?: number): string {
  if (!cost) return '-'
  return `¥${cost.toFixed(4)}`
}

/** 桌面页码路径。表格与分页条的既有行为逐字保留。 */
async function loadApprovals() {
  loading.value = true
  clearError()

  try {
    const response = await getApprovalList({
      ...filterBody(),
      page: currentPage.value,
      page_size: pageSize.value,
    })
    approvals.value = response.items || []
    totalItems.value = response.total
    totalPages.value = response.total_pages
    currentPage.value = response.page
  } catch (e: any) {
    notifyError(e.message || t('approval.list.errors.loadListFailed'))
  } finally {
    loading.value = false
  }
}

// ── compact 连续加载 ──────────────────────────────────────────────────────
// 独立于上面的页码状态机：两者不共享 ref、不互相写。
const continuous = createHyperPages<ApprovalItem>({
  pageSize: 20,
  // id 是后端主键且稳定。**不能用数组下标** —— 连续加载第 2 页的下标 0
  // 是另一行，去重会把它当成与第 1 页第 0 行相同，表现为「行随机消失」。
  rowKey: (item) => item.id,
  fetchPage: async (p) => {
    const response = await getApprovalList({ ...filterBody(), page: p, page_size: 20 })
    return { rows: response.items || [], total: response.total }
  },
})

/** 实际展示的行：按档位二选一，再套同一套客户端搜索。 */
const rows = computed<ApprovalItem[]>(() =>
  isCompact.value ? applySearch(continuous.rows.value) : filteredApprovals.value,
)

/**
 * compact 卡片的字段定义。**没有 `format` 就只能渲染原始字符串** ——
 * 风险等级/状态是枚举代码、费用要千分位与货币号、时间要本地化，
 * 所以这里必须逐个接格式化钩子，不能直接把 `item` 丢给 CardList。
 */
const cardFields = computed<CardField[]>(() => [
  {
    key: 'risk_level',
    label: t('approval.list.table.riskLevel'),
    type: 'badge',
    format: (v) => (v == null ? null : getRiskLevelLabel(String(v))),
  },
  {
    key: 'status',
    label: t('approval.list.table.status'),
    type: 'badge',
    format: (v) => (v == null ? null : getStatusLabel(String(v))),
  },
  { key: 'trigger_type', label: t('approval.list.table.trigger'), format: (v) => (v == null ? null : String(v)) },
  {
    key: 'session_id',
    label: t('approval.list.table.sessionId'),
    format: (v) => (v == null ? null : `${String(v).slice(0, 12)}...`),
  },
  {
    key: 'cost',
    label: t('approval.list.table.cost'),
    type: 'metric',
    align: 'end',
    // 费用在 detect_result.cost_estimation 上，不在行顶层 —— 用整行取。
    format: (_v, row) => formatCost((row.detect_result as { cost_estimation?: number } | undefined)?.cost_estimation),
  },
  {
    key: 'created_at',
    label: t('approval.list.table.createdAt'),
    format: (v) => (v == null ? null : formatDate(String(v))),
  },
])

/** 页码条：仅桌面。compact 走连续加载，两条路径不同时出现在屏幕上。 */
const showPager = computed(() => !isCompact.value && totalPages.value > 1)

/** 重新取数：按档位分派到两条路径，且 compact 下先作废在途结果再重取。 */
async function reload() {
  if (isCompact.value) {
    // 顺序不能反：先 invalidate（作废在途结果并提 revision），再 loadFirst，
    // 否则旧请求可能在新请求之后落地。
    continuous.invalidate()
    await continuous.loadFirst()
    return
  }
  currentPage.value = 1
  await loadApprovals()
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
    notifySuccess(t('approval.list.success.approved'))
    await reload()
    await loadStats()
  } catch (e: any) {
    notifyError(e.message || t('approval.list.errors.approveFailed'))
  }
}

async function quickReject(item: ApprovalItem) {
  const reason = prompt(t('approval.list.confirm.rejectPrompt'))
  if (!reason || !reason.trim()) {
    return
  }

  try {
    await rejectApproval(item.request_id, reason)
    notifySuccess(t('approval.list.success.rejected'))
    await reload()
    await loadStats()
  } catch (e: any) {
    notifyError(e.message || t('approval.list.errors.rejectFailed'))
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
  void reload()
}

function changePage(page: number) {
  currentPage.value = page
  loadApprovals()
}

function startAutoRefresh() {
  refreshInterval = window.setInterval(() => {
    if (statusFilter.value === 'pending') {
      void reload()
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
  void reload()
  loadStats()
  startAutoRefresh()
})

onBeforeUnmount(() => {
  stopAutoRefresh()
})

// Watch filters
watch([statusFilter, riskLevelFilter, dateRangeStart, dateRangeEnd], () => {
  // 筛选变化必须**从第 1 页重来**（compact 下同理：invalidate + loadFirst），
  // 否则页码会停在旧值上，筛完直接看到空白。
  void reload()
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
      <button class="message-close" @click="clearError">×</button>
    </div>

    <div v-if="successMessage" class="message message-success">
      <span class="message-icon">✅</span>
      {{ successMessage }}
      <button class="message-close" @click="clearMessage">×</button>
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

    <!--
      2026-10-04 H6：双模板 + 双加载方式。
      · 桌面 → 表格（表头/表体/徽章/行内按钮逐字未改）+ 页码
      · compact → 卡片 + 连续加载 + 底部 sentinel
      loading / empty 由 `ResponsiveDataView` 统一裁定，所以原模板里的
      AppSpinner 与 EmptyState 移出 table slot（否则 compact 下它们是死代码）。
      `emptyPadding="64px"` 保住桌面空态原有的 64px 内边距 —— 桌面像素不变。
    -->
    <ResponsiveDataView
      :rows="rows"
      title-key="request_id"
      :fields="cardFields"
      table-min-width="960px"
      :loading="loading && !isCompact"
      :empty="rows.length === 0 && !loading"
      :empty-text="t('approval.list.empty')"
      empty-padding="64px"
    >
      <template #table>
    <div class="table-container">
      <table class="data-table">
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
      </template>
    </ResponsiveDataView>

    <!-- 连续加载尾部：仅 compact。屏幕上不会同时出现两个「加载更多」语义。 -->
    <HyperLoadMore
      v-if="isCompact"
      :state="continuous.state.value"
      :has-more="continuous.hasMore.value"
      :loaded-count="continuous.loadedCount.value"
      @load-more="continuous.loadNext()"
      @retry="continuous.retry()"
    />

    <!-- Pagination -->
    <div v-if="showPager" class="pagination">
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
  /* width:auto 覆盖全局 input/select width:100%，筛选控件按内容宽度排布 */
  width: auto;
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

@media (max-width: 1024px) {
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

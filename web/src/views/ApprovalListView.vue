<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { getApprovalList, approveApproval, rejectApproval, getApprovalStats, type ApprovalItem, type ApprovalStats } from '../api/approval'
import { isSuperAdmin } from '../store'

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

const statusOptions = [
  { value: '', label: '全部状态' },
  { value: 'pending', label: '待审批' },
  { value: 'approved', label: '已批准' },
  { value: 'rejected', label: '已拒绝' },
  { value: 'timeout', label: '已超时' },
]

const riskLevelOptions = [
  { value: '', label: '全部风险等级' },
  { value: 'LOW', label: '低风险' },
  { value: 'MEDIUM', label: '中风险' },
  { value: 'HIGH', label: '高风险' },
  { value: 'CRITICAL', label: '严重风险' },
]

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
  switch (level?.toUpperCase()) {
    case 'LOW': return '低风险'
    case 'MEDIUM': return '中风险'
    case 'HIGH': return '高风险'
    case 'CRITICAL': return '严重'
    default: return level || '-'
  }
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
  switch (status) {
    case 'pending': return '待审批'
    case 'approved': return '已批准'
    case 'rejected': return '已拒绝'
    case 'timeout': return '已超时'
    default: return status
  }
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
    return date.toLocaleDateString('zh-CN', { 
      year: 'numeric', 
      month: '2-digit', 
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit'
    })
  }
  if (days > 0) return `${days} 天前`
  if (hours > 0) return `${hours} 小时前`
  if (minutes > 0) return `${minutes} 分钟前`
  return '刚刚'
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
    error.value = e.message || '加载审批列表失败'
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
  if (!confirm(`确认批准此请求？\n请求 ID: ${item.request_id}`)) {
    return
  }
  
  try {
    await approveApproval(item.request_id)
    successMessage.value = '审批请求已批准'
    setTimeout(() => successMessage.value = null, 3000)
    await loadApprovals()
    await loadStats()
  } catch (e: any) {
    error.value = e.message || '批准失败'
  }
}

async function quickReject(item: ApprovalItem) {
  const reason = prompt(`请输入拒绝原因：`)
  if (!reason || !reason.trim()) {
    return
  }
  
  try {
    await rejectApproval(item.request_id, reason)
    successMessage.value = '审批请求已拒绝'
    setTimeout(() => successMessage.value = null, 3000)
    await loadApprovals()
    await loadStats()
  } catch (e: any) {
    error.value = e.message || '拒绝失败'
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
import { watch } from 'vue'
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
        <h1>审批请求</h1>
        <p class="page-description">管理和处理审批请求</p>
      </div>
      <div class="header-actions">
        <button class="btn btn-secondary" @click="loadApprovals" :disabled="loading">
          🔄 {{ loading ? '刷新中...' : '刷新' }}
        </button>
        <button class="btn btn-secondary" @click="router.push('/admin/approval-config')">
          ⚙️ 审批配置
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
        <div class="stat-label">待审批</div>
        <div class="stat-value" :class="{ 'stat-highlight': stats.pending > 0 }">{{ stats.pending }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">今日新增</div>
        <div class="stat-value">{{ stats.today_total }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">已批准</div>
        <div class="stat-value stat-green">{{ stats.approved }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">已拒绝</div>
        <div class="stat-value stat-red">{{ stats.rejected }}</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">平均处理时间</div>
        <div class="stat-value stat-small">{{ Math.round(stats.avg_approval_time_seconds / 60) }}m</div>
      </div>
    </div>

    <!-- Filters -->
    <div class="filters-section">
      <div class="filters-row">
        <div class="filter-group">
          <label>状态</label>
          <select v-model="statusFilter" class="form-select">
            <option v-for="opt in statusOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </option>
          </select>
        </div>

        <div class="filter-group">
          <label>风险等级</label>
          <select v-model="riskLevelFilter" class="form-select">
            <option v-for="opt in riskLevelOptions" :key="opt.value" :value="opt.value">
              {{ opt.label }}
            </option>
          </select>
        </div>

        <div class="filter-group filter-group-grow">
          <label>搜索</label>
          <input
            v-model="searchQuery"
            type="text"
            class="form-input"
            placeholder="搜索 Session ID 或 Request ID..."
          />
        </div>

        <div class="filter-actions">
          <button class="btn btn-secondary btn-sm" @click="resetFilters">
            重置
          </button>
        </div>
      </div>
    </div>

    <!-- Table -->
    <div class="table-container">
      <div v-if="loading && approvals.length === 0" class="loading-state">
        <div class="loading-spinner">加载中...</div>
      </div>

      <div v-else-if="filteredApprovals.length === 0" class="empty-state">
        <div class="empty-icon">📋</div>
        <p>暂无审批请求</p>
      </div>

      <table v-else class="data-table">
        <thead>
          <tr>
            <th>请求 ID</th>
            <th>会话 ID</th>
            <th>风险等级</th>
            <th>触发原因</th>
            <th>预估成本</th>
            <th>创建时间</th>
            <th>状态</th>
            <th class="actions-column">操作</th>
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
                剩余: {{ item.time_left }}
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
                  title="批准"
                >
                  ✓
                </button>
                <button 
                  v-if="item.status === 'pending'" 
                  class="btn btn-danger btn-xs"
                  @click="quickReject(item)"
                  title="拒绝"
                >
                  ✕
                </button>
                <button 
                  class="btn btn-secondary btn-xs"
                  @click="viewDetail(item)"
                  title="查看详情"
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
        上一页
      </button>
      
      <div class="pagination-info">
        第 {{ currentPage }} / {{ totalPages }} 页 (共 {{ totalItems }} 条)
      </div>
      
      <button 
        class="btn btn-secondary btn-sm"
        @click="changePage(currentPage + 1)"
        :disabled="currentPage === totalPages"
      >
        下一页
      </button>
    </div>
  </div>
</template>

<style scoped>
.approval-list-view {
  padding: 20px;
  max-width: 1600px;
  margin: 0 auto;
  color: var(--text-primary, #e6edf3);
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
  color: var(--text-secondary, #8b949e);
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
  background: rgba(248, 113, 113, 0.1);
  border: 1px solid rgba(248, 113, 113, 0.3);
  color: #f87171;
}

.message-success {
  background: rgba(52, 211, 153, 0.1);
  border: 1px solid rgba(52, 211, 153, 0.3);
  color: #34d399;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(180px, 1fr));
  gap: 16px;
  margin-bottom: 24px;
}

.stat-card {
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  padding: 16px;
  text-align: center;
}

.stat-label {
  font-size: 13px;
  color: var(--text-secondary, #8b949e);
  margin-bottom: 8px;
}

.stat-value {
  font-size: 32px;
  font-weight: 600;
  color: var(--text-primary, #e6edf3);
}

.stat-value.stat-small {
  font-size: 24px;
}

.stat-value.stat-highlight {
  color: #fbbf24;
}

.stat-value.stat-green {
  color: #34d399;
}

.stat-value.stat-red {
  color: #f87171;
}

.filters-section {
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
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
  color: var(--text-secondary, #8b949e);
  font-weight: 500;
}

.filter-actions {
  display: flex;
  gap: 8px;
}

.form-select,
.form-input {
  padding: 8px 12px;
  background: var(--bg, #0f1117);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  color: var(--text-primary, #e6edf3);
  font-size: 14px;
}

.form-select:focus,
.form-input:focus {
  outline: none;
  border-color: var(--accent, #6366f1);
}

.table-container {
  background: var(--bg-card, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 8px;
  overflow: hidden;
  margin-bottom: 16px;
}

.loading-state,
.empty-state {
  padding: 64px;
  text-align: center;
  color: var(--text-secondary, #8b949e);
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
  background: var(--bg, #0f1117);
  border-bottom: 1px solid var(--border, #30363d);
}

.data-table th {
  padding: 12px 16px;
  text-align: left;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-secondary, #8b949e);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.data-table td {
  padding: 12px 16px;
  border-top: 1px solid var(--border, #30363d);
  font-size: 14px;
}

.table-row:hover {
  background: rgba(255, 255, 255, 0.02);
}

.link-button {
  background: none;
  border: none;
  color: var(--accent, #6366f1);
  cursor: pointer;
  text-decoration: underline;
  font-size: 14px;
  padding: 0;
}

.link-button:hover {
  color: #5558e3;
}

.text-mono {
  font-family: 'Monaco', 'Menlo', monospace;
  font-size: 13px;
  color: var(--text-secondary, #8b949e);
}

.text-secondary {
  color: var(--text-secondary, #8b949e);
}

.time-left {
  font-size: 11px;
  color: #fbbf24;
  margin-top: 2px;
}

.trigger-reason {
  font-size: 13px;
  color: var(--text-primary, #e6edf3);
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
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
}

.badge-yellow {
  background: rgba(251, 191, 36, 0.15);
  color: #fbbf24;
}

.badge-orange {
  background: rgba(251, 146, 60, 0.15);
  color: #fb923c;
}

.badge-red {
  background: rgba(248, 113, 113, 0.15);
  color: #f87171;
}

.badge-gray {
  background: rgba(139, 148, 158, 0.15);
  color: #8b949e;
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
  color: var(--text-secondary, #8b949e);
}

.btn {
  padding: 8px 16px;
  border-radius: 6px;
  font-size: 14px;
  cursor: pointer;
  border: 1px solid transparent;
  transition: all 0.2s;
  font-weight: 500;
  background: var(--bg-card, #161b22);
  color: var(--text-primary, #e6edf3);
  border-color: var(--border, #30363d);
}

.btn:hover:not(:disabled) {
  background: var(--bg, #0f1117);
  border-color: var(--accent, #6366f1);
}

.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn-primary {
  background: var(--accent, #6366f1);
  color: #fff;
  border-color: var(--accent, #6366f1);
}

.btn-primary:hover:not(:disabled) {
  background: #5558e3;
}

.btn-secondary {
  background: var(--bg-card, #161b22);
  border-color: var(--border, #30363d);
}

.btn-success {
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
  border-color: #34d399;
}

.btn-success:hover:not(:disabled) {
  background: rgba(52, 211, 153, 0.25);
}

.btn-danger {
  background: rgba(248, 113, 113, 0.15);
  color: #f87171;
  border-color: #f87171;
}

.btn-danger:hover:not(:disabled) {
  background: rgba(248, 113, 113, 0.25);
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

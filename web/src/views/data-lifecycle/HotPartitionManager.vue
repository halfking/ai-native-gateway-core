<template>
  <div class="hot-partition-manager">
    <div class="card">
      <h3 class="card-title">Hot 表数据迁移</h3>
      <p class="card-desc">
        将 hot 表中的数据迁移到月度分区表。迁移后需要执行 VACUUM FULL 才能真正释放磁盘空间。
      </p>

      <!-- Hot 表列表 -->
      <div class="hot-tables-grid">
        <div
          v-for="table in hotTables"
          :key="table.name"
          class="hot-table-card"
          :class="{ migrating: table.migrating }"
        >
          <div class="table-header">
            <h4 class="table-name">{{ table.label }}</h4>
            <span class="table-size" :class="getSizeClass(table.sizeBytes)">
              {{ table.sizeHuman }}
            </span>
          </div>

          <div class="table-stats">
            <div class="stat-item">
              <span class="stat-label">行数</span>
              <span class="stat-value">{{ formatNumber(table.rows) }}</span>
            </div>
            <div class="stat-item">
              <span class="stat-label">TOAST</span>
              <span class="stat-value">{{ table.toastHuman }}</span>
            </div>
          </div>

          <div class="migration-controls">
            <select v-model="table.retentionHours" class="retention-select" :disabled="table.migrating">
              <option :value="0">立即迁移全部</option>
              <option :value="24">保留 1 天</option>
              <option :value="168">保留 7 天</option>
              <option :value="720">保留 30 天</option>
            </select>

            <button
              class="btn btn-sm btn-primary"
              @click="promoteTable(table)"
              :disabled="table.migrating || loading"
            >
              {{ table.migrating ? '迁移中...' : '开始迁移' }}
            </button>
          </div>

          <!-- 迁移进度 -->
          <div v-if="table.progress" class="migration-progress">
            <div class="progress-bar">
              <div class="progress-fill" :style="{ width: table.progress.percent + '%' }"></div>
            </div>
            <div class="progress-text">
              已迁移 {{ formatNumber(table.progress.migrated) }} 行
              ({{ table.progress.batches }} 批次，{{ table.progress.duration }}s)
            </div>
          </div>

          <!-- 迁移结果 -->
          <div v-if="table.result" class="migration-result" :class="table.result.status">
            <div class="result-icon">
              {{ table.result.status === 'success' ? '✓' : table.result.status === 'partial' ? '⚠' : '✗' }}
            </div>
            <div class="result-content">
              <div class="result-message">{{ table.result.message }}</div>
              <div v-if="table.result.warning" class="result-warning">⚠️ {{ table.result.warning }}</div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 分区管理 -->
    <div class="card">
      <h3 class="card-title">月度分区管理</h3>
      <p class="card-desc">
        删除旧的月度分区以释放磁盘空间。<strong>操作不可恢复，请谨慎。</strong>
      </p>

      <div class="table-select">
        <label>选择表：</label>
        <select v-model="selectedPartitionTable" @change="loadPartitions">
          <option value="request_logs">request_logs (请求日志)</option>
          <option value="usage_ledger">usage_ledger (用量账本)</option>
          <option value="routing_decision_log">routing_decision_log (路由决策)</option>
          <option value="credential_model_index">credential_model_index (凭据索引)</option>
        </select>
        <button class="btn btn-sm btn-ghost" @click="loadPartitions" :disabled="loading">
          刷新
        </button>
      </div>

      <!-- 分区列表 -->
      <div v-if="partitions.length" class="partitions-table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>分区名</th>
              <th>存储类型</th>
              <th>大小</th>
              <th>行数</th>
              <th>月份</th>
              <th>操作</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="partition in partitions" :key="partition.name" :class="{ deleting: partition.deleting }">
              <td>
                <code>{{ partition.name }}</code>
              </td>
              <td>
                <span class="storage-badge" :class="partition.storage">
                  {{ partition.storage }}
                </span>
              </td>
              <td>{{ partition.sizeHuman }}</td>
              <td>{{ formatNumber(partition.rows) }}</td>
              <td>
                <span class="month-badge" :class="getMonthClass(partition.month)">
                  {{ partition.month }}
                </span>
              </td>
              <td>
                <button
                  class="btn btn-sm btn-danger"
                  @click="showDeleteConfirm(partition)"
                  :disabled="partition.deleting || loading"
                >
                  {{ partition.deleting ? '删除中...' : '删除' }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-else class="empty-hint">暂无分区数据</div>
    </div>

    <!-- 删除确认弹窗 -->
    <div v-if="deleteConfirm" class="modal-overlay" @click="deleteConfirm = null">
      <div class="modal-dialog" @click.stop>
        <div class="modal-header">
          <h3>确认删除分区</h3>
        </div>
        <div class="modal-body">
          <div class="warning-box">
            <div class="warning-icon">⚠️</div>
            <div class="warning-content">
              <p><strong>此操作不可恢复！</strong></p>
              <p>您即将删除分区：</p>
              <code>{{ deleteConfirm.name }}</code>
              <ul>
                <li>大小：{{ deleteConfirm.sizeHuman }}</li>
                <li>行数：{{ formatNumber(deleteConfirm.rows) }}</li>
                <li>月份：{{ deleteConfirm.month }}</li>
              </ul>
              <p>请输入分区名以确认：</p>
              <input
                v-model="deleteConfirmInput"
                type="text"
                class="confirm-input"
                :placeholder="deleteConfirm.name"
              />
            </div>
          </div>
        </div>
        <div class="modal-footer">
          <button class="btn btn-ghost" @click="deleteConfirm = null">取消</button>
          <button
            class="btn btn-danger"
            @click="executeDelete"
            :disabled="deleteConfirmInput !== deleteConfirm.name || deleting"
          >
            {{ deleting ? '删除中...' : '确认删除' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { api } from '@/utils/api'

interface HotTable {
  name: string
  label: string
  sizeHuman: string
  sizeBytes: number
  toastHuman: string
  rows: number
  retentionHours: number
  migrating: boolean
  progress?: {
    migrated: number
    batches: number
    duration: number
    percent: number
  }
  result?: {
    status: 'success' | 'partial' | 'failed'
    message: string
    warning?: string
  }
}

interface Partition {
  name: string
  storage: string
  sizeHuman: string
  sizeBytes: number
  rows: number
  month: string
  deleting: boolean
}

const loading = ref(false)
const deleting = ref(false)

// Hot 表数据
const hotTables = ref<HotTable[]>([
  {
    name: 'request_logs_hot',
    label: '请求日志',
    sizeHuman: '—',
    sizeBytes: 0,
    toastHuman: '—',
    rows: 0,
    retentionHours: 168,
    migrating: false,
  },
  {
    name: 'credential_model_index_hot',
    label: '凭据模型索引',
    sizeHuman: '—',
    sizeBytes: 0,
    toastHuman: '—',
    rows: 0,
    retentionHours: 168,
    migrating: false,
  },
  {
    name: 'usage_ledger_hot',
    label: '用量账本',
    sizeHuman: '—',
    sizeBytes: 0,
    toastHuman: '—',
    rows: 0,
    retentionHours: 168,
    migrating: false,
  },
  {
    name: 'routing_decision_log_hot',
    label: '路由决策日志',
    sizeHuman: '—',
    sizeBytes: 0,
    toastHuman: '—',
    rows: 0,
    retentionHours: 168,
    migrating: false,
  },
])

// 分区数据
const selectedPartitionTable = ref('request_logs')
const partitions = ref<Partition[]>([])
const deleteConfirm = ref<Partition | null>(null)
const deleteConfirmInput = ref('')

onMounted(() => {
  loadHotTableStats()
  loadPartitions()
})

async function loadHotTableStats() {
  try {
    loading.value = true
    const res = await api.get('/api/admin/data-lifecycle/storage/tables')
    
    for (const table of hotTables.value) {
      const stat = res.tables?.find((t: any) => t.table_name === table.name)
      if (stat) {
        table.sizeHuman = stat.total_size_human
        table.sizeBytes = stat.total_size_bytes
        table.toastHuman = stat.toast_size_human || '0 B'
        table.rows = stat.estimated_rows || 0
      }
    }
  } catch (err: any) {
    console.error('加载 hot 表统计失败:', err)
  } finally {
    loading.value = false
  }
}

async function promoteTable(table: HotTable) {
  if (!confirm(`确认迁移 ${table.label} 中超过 ${table.retentionHours === 0 ? '全部' : table.retentionHours + '小时'} 的数据？`)) {
    return
  }

  table.migrating = true
  table.progress = { migrated: 0, batches: 0, duration: 0, percent: 0 }
  table.result = undefined

  try {
    const res = await api.post('/api/admin/data-lifecycle/hot/promote', {
      table_name: table.name,
      retention_hours: table.retentionHours,
      batch_size: 1000,
      max_batches: 0,
    })

    table.result = {
      status: res.status,
      message: res.message,
      warning: res.warning,
    }

    table.progress = {
      migrated: res.total_migrated,
      batches: res.batches_executed,
      duration: res.duration_seconds,
      percent: 100,
    }

    // 刷新统计
    await loadHotTableStats()
  } catch (err: any) {
    table.result = {
      status: 'failed',
      message: err.response?.data?.error || err.message || '迁移失败',
    }
  } finally {
    table.migrating = false
  }
}

async function loadPartitions() {
  try {
    loading.value = true
    // 这里需要调用实际的分区列表 API
    // 暂时模拟数据
    const res = await api.get(`/api/admin/data-lifecycle/partitions?table=${selectedPartitionTable.value}`)
    
    partitions.value = (res.partitions || []).map((p: any) => ({
      name: p.partition_name,
      storage: p.is_columnar ? 'columnar' : 'heap',
      sizeHuman: p.size_human,
      sizeBytes: p.size_bytes,
      rows: p.row_count || 0,
      month: extractMonth(p.partition_name),
      deleting: false,
    }))
  } catch (err: any) {
    console.error('加载分区列表失败:', err)
    partitions.value = []
  } finally {
    loading.value = false
  }
}

function showDeleteConfirm(partition: Partition) {
  deleteConfirm.value = partition
  deleteConfirmInput.value = ''
}

async function executeDelete() {
  if (!deleteConfirm.value) return

  const partition = deleteConfirm.value
  partition.deleting = true
  deleting.value = true

  try {
    const res = await api.post('/api/admin/data-lifecycle/partitions/drop', {
      partition_name: partition.name,
      confirm: true,
    })

    alert(`删除成功：\n${res.message}\n释放空间：${res.space_freed_human}`)
    
    // 从列表中移除
    partitions.value = partitions.value.filter((p) => p.name !== partition.name)
    deleteConfirm.value = null
  } catch (err: any) {
    alert('删除失败：' + (err.response?.data?.error || err.message))
    partition.deleting = false
  } finally {
    deleting.value = false
  }
}

function extractMonth(partitionName: string): string {
  const match = partitionName.match(/(\d{4})_(\d{2})/)
  return match ? `${match[1]}-${match[2]}` : '—'
}

function getSizeClass(bytes: number): string {
  const gb = bytes / 1024 / 1024 / 1024
  if (gb > 5) return 'size-critical'
  if (gb > 1) return 'size-warning'
  return ''
}

function getMonthClass(month: string): string {
  const now = new Date()
  const current = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}`
  const monthDate = new Date(month + '-01')
  const currentDate = new Date(current + '-01')
  const diffMonths = (currentDate.getTime() - monthDate.getTime()) / (1000 * 60 * 60 * 24 * 30)
  
  if (diffMonths > 3) return 'month-old'
  if (diffMonths > 1) return 'month-medium'
  return 'month-recent'
}

function formatNumber(num: number): string {
  return num.toLocaleString()
}
</script>

<style scoped>
.hot-partition-manager {
  padding: 20px;
}

.card {
  background: #fff;
  border-radius: 8px;
  padding: 24px;
  margin-bottom: 24px;
  box-shadow: 0 1px 3px rgba(0,0,0,0.1);
}

.card-title {
  font-size: 18px;
  font-weight: 600;
  margin: 0 0 8px 0;
}

.card-desc {
  color: #666;
  margin: 0 0 20px 0;
  font-size: 14px;
}

/* Hot 表卡片网格 */
.hot-tables-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 16px;
}

.hot-table-card {
  border: 1px solid #e5e7eb;
  border-radius: 8px;
  padding: 16px;
  background: #f9fafb;
  transition: all 0.2s;
}

.hot-table-card.migrating {
  background: #fff7ed;
  border-color: #fb923c;
}

.table-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.table-name {
  font-size: 16px;
  font-weight: 600;
  margin: 0;
}

.table-size {
  font-size: 14px;
  font-weight: 600;
  padding: 4px 8px;
  border-radius: 4px;
  background: #e5e7eb;
}

.table-size.size-warning {
  background: #fef3c7;
  color: #92400e;
}

.table-size.size-critical {
  background: #fee2e2;
  color: #991b1b;
}

.table-stats {
  display: flex;
  gap: 16px;
  margin-bottom: 12px;
}

.stat-item {
  flex: 1;
  display: flex;
  flex-direction: column;
}

.stat-label {
  font-size: 12px;
  color: #6b7280;
  margin-bottom: 4px;
}

.stat-value {
  font-size: 14px;
  font-weight: 600;
}

.migration-controls {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
}

.retention-select {
  flex: 1;
  padding: 6px 10px;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 14px;
}

.migration-progress {
  margin-top: 12px;
}

.progress-bar {
  height: 6px;
  background: #e5e7eb;
  border-radius: 3px;
  overflow: hidden;
  margin-bottom: 6px;
}

.progress-fill {
  height: 100%;
  background: #3b82f6;
  transition: width 0.3s;
}

.progress-text {
  font-size: 12px;
  color: #6b7280;
}

.migration-result {
  margin-top: 12px;
  padding: 12px;
  border-radius: 6px;
  display: flex;
  gap: 8px;
}

.migration-result.success {
  background: #d1fae5;
  border: 1px solid #6ee7b7;
}

.migration-result.partial {
  background: #fef3c7;
  border: 1px solid #fcd34d;
}

.migration-result.failed {
  background: #fee2e2;
  border: 1px solid #fca5a5;
}

.result-icon {
  font-size: 18px;
  font-weight: bold;
}

.result-content {
  flex: 1;
}

.result-message {
  font-size: 14px;
  margin-bottom: 4px;
}

.result-warning {
  font-size: 12px;
  color: #92400e;
  background: #fef3c7;
  padding: 8px;
  border-radius: 4px;
  margin-top: 8px;
}

/* 分区管理 */
.table-select {
  display: flex;
  gap: 12px;
  align-items: center;
  margin-bottom: 16px;
}

.table-select label {
  font-weight: 500;
}

.table-select select {
  padding: 6px 12px;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 14px;
}

.partitions-table-wrap {
  overflow-x: auto;
}

.data-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 14px;
}

.data-table th {
  text-align: left;
  padding: 12px;
  background: #f9fafb;
  border-bottom: 2px solid #e5e7eb;
  font-weight: 600;
}

.data-table td {
  padding: 12px;
  border-bottom: 1px solid #e5e7eb;
}

.data-table tr.deleting {
  opacity: 0.5;
}

.storage-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 500;
}

.storage-badge.columnar {
  background: #dbeafe;
  color: #1e40af;
}

.storage-badge.heap {
  background: #e5e7eb;
  color: #374151;
}

.month-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 500;
}

.month-badge.month-recent {
  background: #d1fae5;
  color: #065f46;
}

.month-badge.month-medium {
  background: #fed7aa;
  color: #9a3412;
}

.month-badge.month-old {
  background: #e5e7eb;
  color: #374151;
}

/* 按钮 */
.btn {
  padding: 8px 16px;
  border: none;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.2s;
}

.btn-sm {
  padding: 6px 12px;
  font-size: 13px;
}

.btn-primary {
  background: #3b82f6;
  color: #fff;
}

.btn-primary:hover:not(:disabled) {
  background: #2563eb;
}

.btn-danger {
  background: #ef4444;
  color: #fff;
}

.btn-danger:hover:not(:disabled) {
  background: #dc2626;
}

.btn-ghost {
  background: transparent;
  color: #6b7280;
  border: 1px solid #d1d5db;
}

.btn-ghost:hover:not(:disabled) {
  background: #f9fafb;
}

.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* 模态框 */
.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.5);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
}

.modal-dialog {
  background: #fff;
  border-radius: 12px;
  width: 90%;
  max-width: 500px;
  box-shadow: 0 20px 25px -5px rgba(0, 0, 0, 0.1);
}

.modal-header {
  padding: 20px 24px;
  border-bottom: 1px solid #e5e7eb;
}

.modal-header h3 {
  margin: 0;
  font-size: 18px;
  font-weight: 600;
}

.modal-body {
  padding: 24px;
}

.modal-footer {
  padding: 16px 24px;
  border-top: 1px solid #e5e7eb;
  display: flex;
  justify-content: flex-end;
  gap: 12px;
}

.warning-box {
  display: flex;
  gap: 16px;
  background: #fef3c7;
  border: 1px solid #fcd34d;
  border-radius: 8px;
  padding: 16px;
}

.warning-icon {
  font-size: 24px;
}

.warning-content {
  flex: 1;
}

.warning-content p {
  margin: 0 0 12px 0;
}

.warning-content code {
  display: block;
  background: #fff;
  padding: 8px;
  border-radius: 4px;
  margin: 8px 0;
  font-family: monospace;
}

.warning-content ul {
  margin: 12px 0;
  padding-left: 20px;
}

.confirm-input {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 14px;
  font-family: monospace;
  margin-top: 8px;
}

.empty-hint {
  text-align: center;
  padding: 40px;
  color: #9ca3af;
}
</style>

<template>
  <div class="hot-partition-manager">
    <div class="card">
      <h3 class="card-title">{{ t('dataLifecycle.hotPartition.hotTableTitle') }}</h3>
      <p class="card-desc">{{ t('dataLifecycle.hotPartition.hotTableDesc') }}</p>

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
              <span class="stat-label">{{ t('dataLifecycle.hotPartition.labels.rows') }}</span>
              <span class="stat-value">{{ formatNumber(table.rows) }}</span>
            </div>
            <div class="stat-item">
              <span class="stat-label">{{ t('dataLifecycle.hotPartition.labels.toast') }}</span>
              <span class="stat-value">{{ table.toastHuman }}</span>
            </div>
          </div>

          <div class="migration-controls">
            <select v-model="table.retentionHours" class="retention-select" :disabled="table.migrating">
              <option :value="0">{{ t('dataLifecycle.hotPartition.retention.all') }}</option>
              <option :value="24">{{ t('dataLifecycle.hotPartition.retention.1day') }}</option>
              <option :value="168">{{ t('dataLifecycle.hotPartition.retention.7day') }}</option>
              <option :value="720">{{ t('dataLifecycle.hotPartition.retention.30day') }}</option>
            </select>

            <button
              class="btn btn-sm btn-primary"
              @click="promoteTable(table)"
              :disabled="table.migrating || loading"
            >
              {{ table.migrating ? t('dataLifecycle.hotPartition.migrating') : t('dataLifecycle.hotPartition.startMigrate') }}
            </button>
          </div>

          <!-- 迁移进度 -->
          <div v-if="table.progress" class="migration-progress">
            <div class="progress-bar">
              <div class="progress-fill" :style="{ width: table.progress.percent + '%' }"></div>
            </div>
            <div class="progress-text">
              {{ t('dataLifecycle.hotPartition.migrationProgress', {
                migrated: formatNumber(table.progress.migrated),
                batches: table.progress.batches,
                duration: table.progress.duration,
              }) }}
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
      <h3 class="card-title">{{ t('dataLifecycle.hotPartition.partitionTitle') }}</h3>
      <p class="card-desc">
        {{ t('dataLifecycle.hotPartition.partitionDesc') }}
        <strong>{{ t('dataLifecycle.hotPartition.partitionWarning') }}</strong>
      </p>

      <!-- 分区表列表 -->
      <div class="tables-header">
        <h4 class="section-title">{{ t('dataLifecycle.hotPartition.tables.title') }}</h4>
        <button class="btn btn-sm btn-ghost" @click="loadPartitionTables" :disabled="loading">
          {{ t('dataLifecycle.hotPartition.tables.refresh') }}
        </button>
      </div>
      <div class="partitioned-tables-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('dataLifecycle.hotPartition.tables.columns.name') }}</th>
              <th>{{ t('dataLifecycle.hotPartition.tables.columns.description') }}</th>
              <th>{{ t('dataLifecycle.hotPartition.tables.columns.totalSize') }}</th>
              <th>{{ t('dataLifecycle.hotPartition.tables.columns.rows') }}</th>
              <th>{{ t('dataLifecycle.hotPartition.tables.columns.partitions') }}</th>
              <th>{{ t('dataLifecycle.hotPartition.tables.columns.archivable') }}</th>
              <th>{{ t('dataLifecycle.hotPartition.tables.columns.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="pTable in partitionTables"
              :key="pTable.table_name"
              :class="{ active: selectedTable === pTable.table_name }"
            >
              <td>
                <code class="tbl-code">{{ pTable.table_name }}</code>
              </td>
              <td class="dim">{{ pTable.description }}</td>
              <td class="strong">{{ pTable.total_size_human }}</td>
              <td>{{ formatNumber(pTable.total_rows) }}</td>
              <td>{{ pTable.total_partitions }}</td>
              <td>
                <span class="pill warn" v-if="pTable.archivable_count > 0">
                  {{ formatNumber(pTable.archivable_count) }}
                </span>
                <span class="pill dim" v-else>—</span>
              </td>
              <td>
                <button
                  class="btn btn-sm btn-primary"
                  @click="selectTable(pTable.table_name)"
                  :disabled="loading"
                >
                  {{ t('dataLifecycle.hotPartition.tables.viewPartitions') }}
                </button>
              </td>
            </tr>
            <tr v-if="!partitionTables.length && !loading">
              <td :colspan="7" class="empty-row">
                {{ t('dataLifecycle.hotPartition.tables.noTables') }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- 当前选中表的分区列表 -->
      <div v-if="selectedTable" class="partition-list-section">
        <h4 class="section-title">
          {{ t('dataLifecycle.hotPartition.partitionList.title', { table: selectedTable }) }}
        </h4>
        <div v-if="partitions.length" class="partitions-table-wrap">
          <table class="data-table">
            <thead>
              <tr>
                <th>{{ t('dataLifecycle.hotPartition.partitionList.columns.name') }}</th>
                <th>{{ t('dataLifecycle.hotPartition.partitionList.columns.storageType') }}</th>
                <th>{{ t('dataLifecycle.hotPartition.partitionList.columns.size') }}</th>
                <th>{{ t('dataLifecycle.hotPartition.partitionList.columns.rows') }}</th>
                <th>{{ t('dataLifecycle.hotPartition.partitionList.columns.month') }}</th>
                <th>{{ t('dataLifecycle.hotPartition.partitionList.columns.action') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="partition in partitions" :key="partition.name" :class="{ deleting: partition.deleting }">
                <td>
                  <code class="tbl-code">{{ partition.name }}</code>
                </td>
                <td>
                  <span class="storage-badge" :class="partition.storage">
                    {{ partition.storageLabel }}
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
                    :disabled="partition.deleting || deleting"
                  >
                    {{ partition.deleting
                      ? t('dataLifecycle.hotPartition.partitionList.deleting')
                      : t('dataLifecycle.hotPartition.partitionList.delete') }}
                  </button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-else class="empty-hint">
          {{ t('dataLifecycle.hotPartition.partitionList.empty') }}
        </div>
      </div>
    </div>

    <!-- 删除确认弹窗 -->
    <div v-if="deleteConfirm" class="modal-overlay" @click="deleteConfirm = null">
      <div class="modal-dialog" @click.stop>
        <div class="modal-header">
          <h3>{{ t('dataLifecycle.hotPartition.deleteModal.title') }}</h3>
        </div>
        <div class="modal-body">
          <div class="warning-box">
            <div class="warning-icon">⚠️</div>
            <div class="warning-content">
              <p><strong>{{ t('dataLifecycle.hotPartition.deleteModal.warning') }}</strong></p>
              <p>{{ t('dataLifecycle.hotPartition.deleteModal.fields.name') }}：</p>
              <code>{{ deleteConfirm.name }}</code>
              <ul>
                <li>{{ t('dataLifecycle.hotPartition.deleteModal.fields.size') }}：{{ deleteConfirm.sizeHuman }}</li>
                <li>{{ t('dataLifecycle.hotPartition.deleteModal.fields.rows') }}：{{ formatNumber(deleteConfirm.rows) }}</li>
                <li>{{ t('dataLifecycle.hotPartition.deleteModal.fields.month') }}：{{ deleteConfirm.month }}</li>
              </ul>
              <p>{{ t('dataLifecycle.hotPartition.deleteModal.inputLabel') }}</p>
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
          <button class="btn btn-ghost" @click="deleteConfirm = null">
            {{ t('dataLifecycle.hotPartition.deleteModal.cancel') }}
          </button>
          <button
            class="btn btn-danger"
            @click="executeDelete"
            :disabled="deleteConfirmInput !== deleteConfirm.name || deleting"
          >
            {{ deleting
              ? t('dataLifecycle.hotPartition.deleteModal.deleting')
              : t('dataLifecycle.hotPartition.deleteModal.confirm') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { localeRef } from '@/i18n'
import { req } from '@/api/_core'

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
  storageLabel: string
  sizeHuman: string
  sizeBytes: number
  rows: number
  month: string
  deleting: boolean
}

interface PartitionTable {
  table_name: string
  description: string
  total_size_human: string
  total_size_bytes: number
  total_rows: number
  total_partitions: number
  archived_count: number
  archivable_count: number
  has_archive_func: boolean
  archive_table_name: string
  partitions: Array<{
    partition_name: string
    parent_table: string
    start_date?: string
    end_date?: string
    row_count: number
    size_bytes: number
    size_human: string
    is_archived: boolean
    is_columnar: boolean
    can_archive: boolean
  }>
}

const { t } = useI18n()

const loading = ref(false)
const deleting = ref(false)

// Hot 表数据
const hotTables = ref<HotTable[]>([
  {
    name: 'request_logs_hot',
    label: t('dataLifecycle.hotPartition.hotTableNames.requestLogs'),
    sizeHuman: '—',
    sizeBytes: 0,
    toastHuman: '—',
    rows: 0,
    retentionHours: 168,
    migrating: false,
  },
  {
    name: 'credential_model_index_hot',
    label: t('dataLifecycle.hotPartition.hotTableNames.credentialModelIndex'),
    sizeHuman: '—',
    sizeBytes: 0,
    toastHuman: '—',
    rows: 0,
    retentionHours: 168,
    migrating: false,
  },
  {
    name: 'usage_ledger_hot',
    label: t('dataLifecycle.hotPartition.hotTableNames.usageLedger'),
    sizeHuman: '—',
    sizeBytes: 0,
    toastHuman: '—',
    rows: 0,
    retentionHours: 168,
    migrating: false,
  },
  {
    name: 'routing_decision_log_hot',
    label: t('dataLifecycle.hotPartition.hotTableNames.routingDecisionLog'),
    sizeHuman: '—',
    sizeBytes: 0,
    toastHuman: '—',
    rows: 0,
    retentionHours: 168,
    migrating: false,
  },
])

// 分区表数据
const partitionTables = ref<PartitionTable[]>([])
const selectedTable = ref<string>('')
const partitions = ref<Partition[]>([])
const deleteConfirm = ref<Partition | null>(null)
const deleteConfirmInput = ref('')

onMounted(() => {
  loadHotTableStats()
  loadPartitionTables()
})

async function loadHotTableStats() {
  try {
    loading.value = true
    const res = await req<any>('GET', '/api/admin/data-lifecycle/storage/tables')

    for (const table of hotTables.value) {
      const stat = res.tables?.find((tt: any) => tt.table_name === table.name)
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

async function loadPartitionTables() {
  try {
    loading.value = true
    const res = await req<PartitionTable[]>('GET', '/api/admin/data-lifecycle/partitions')
    partitionTables.value = res || []
    // 如果当前选中的表不在新列表里，重置选择
    if (selectedTable.value && !partitionTables.value.find(t2 => t2.table_name === selectedTable.value)) {
      selectedTable.value = ''
      partitions.value = []
    }
    // 如果有表但还没选，默认选第一个
    if (!selectedTable.value && partitionTables.value.length > 0) {
      selectTable(partitionTables.value[0].table_name)
    } else if (selectedTable.value) {
      // 刷新当前选中表的分区数据
      loadPartitions()
    }
  } catch (err: any) {
    console.error('加载分区表列表失败:', err)
    partitionTables.value = []
  } finally {
    loading.value = false
  }
}

async function selectTable(tableName: string) {
  selectedTable.value = tableName
  await loadPartitions()
}

async function loadPartitions() {
  if (!selectedTable.value) {
    partitions.value = []
    return
  }
  try {
    loading.value = true
    const t2 = partitionTables.value.find(t3 => t3.table_name === selectedTable.value)
    if (!t2) {
      partitions.value = []
      return
    }
    partitions.value = (t2.partitions || []).map((p: any) => {
      const storage = p.is_columnar ? 'columnar' : (p.is_archived ? 'archive' : 'heap')
      const storageLabel =
        storage === 'columnar' ? t('dataLifecycle.hotPartition.partitionList.storage.columnar')
        : storage === 'archive' ? t('dataLifecycle.hotPartition.partitionList.storage.archive')
        : t('dataLifecycle.hotPartition.partitionList.storage.heap')
      return {
        name: p.partition_name,
        storage,
        storageLabel,
        sizeHuman: p.size_human,
        sizeBytes: p.size_bytes,
        rows: p.row_count || 0,
        month: extractMonth(p.partition_name),
        deleting: false,
      }
    })
  } catch (err: any) {
    console.error('加载分区列表失败:', err)
    partitions.value = []
  } finally {
    loading.value = false
  }
}

async function promoteTable(table: HotTable) {
  const hoursLabel =
    table.retentionHours === 0
      ? t('dataLifecycle.hotPartition.promoteAll')
      : t('dataLifecycle.hotPartition.hours', { n: table.retentionHours })
  try {
    await ElMessageBox.confirm(
      t('dataLifecycle.hotPartition.promoteConfirm', { label: table.label, hours: hoursLabel }),
      t('dataLifecycle.hotPartition.hotTableTitle'),
      { type: 'warning', confirmButtonText: t('dataLifecycle.hotPartition.startMigrate'), cancelButtonText: t('dataLifecycle.hotPartition.deleteModal.cancel') },
    )
  } catch {
    return // 用户取消
  }

  table.migrating = true
  table.progress = { migrated: 0, batches: 0, duration: 0, percent: 0 }
  table.result = undefined

  try {
    const res = await req<any>('POST', '/api/admin/data-lifecycle/hot/promote', {
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
      message: err.response?.data?.error || err.message || t('dataLifecycle.hotPartition.migrationFailed'),
    }
  } finally {
    table.migrating = false
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
    const res = await req<any>('POST', '/api/admin/data-lifecycle/partitions/drop', {
      partition_name: partition.name,
      confirm: true,
    })

    ElMessage.success({
      message: t('dataLifecycle.hotPartition.deleteModal.success', {
        message: res.message,
        size: res.space_freed_human,
      }),
      duration: 6000,
    })

    // 从列表中移除
    partitions.value = partitions.value.filter((p) => p.name !== partition.name)
    // 刷新分区表统计（archivable_count、total_partitions）
    await loadPartitionTables()
    deleteConfirm.value = null
  } catch (err: any) {
    const msg = err.response?.data?.error || err.message || ''
    ElMessage.error({
      message: t('dataLifecycle.hotPartition.deleteModal.failed', { msg }),
      duration: 8000,
    })
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
  if (month === '—') return ''
  const now = new Date()
  const current = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}`
  const monthDate = new Date(month + '-01')
  const currentDate = new Date(current + '-01')
  if (isNaN(monthDate.getTime())) return ''
  const diffMonths = (currentDate.getTime() - monthDate.getTime()) / (1000 * 60 * 60 * 24 * 30)

  if (diffMonths > 3) return 'month-old'
  if (diffMonths > 1) return 'month-medium'
  return 'month-recent'
}

function formatNumber(num: number): string {
  return num.toLocaleString(localeRef.value)
}
</script>

<style scoped>
.hot-partition-manager {
  padding: 20px;
}

/* 通用卡片 (与父视图风格一致) */
.card {
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 10px;
  padding: 20px;
  margin-bottom: 24px;
}

.card-title {
  font-size: 16px;
  font-weight: 600;
  margin: 0 0 8px 0;
  color: #e6edf3;
}

.card-desc {
  color: #8b949e;
  margin: 0 0 20px 0;
  font-size: 13px;
  line-height: 1.6;
}

.card-desc strong {
  color: #fbbf24;
}

/* Hot 表卡片网格 */
.hot-tables-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 16px;
}

.hot-table-card {
  border: 1px solid #30363d;
  border-radius: 8px;
  padding: 16px;
  background: #0f1117;
  transition: all 0.2s;
}

.hot-table-card.migrating {
  background: rgba(251, 191, 36, 0.08);
  border-color: rgba(251, 191, 36, 0.4);
}

.table-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.table-name {
  font-size: 15px;
  font-weight: 600;
  margin: 0;
  color: #e6edf3;
}

.table-size {
  font-size: 13px;
  font-weight: 600;
  padding: 4px 10px;
  border-radius: 4px;
  background: #21262d;
  color: #8b949e;
  border: 1px solid #30363d;
}

.table-size.size-warning {
  background: rgba(251, 191, 36, 0.15);
  color: #fbbf24;
  border-color: rgba(251, 191, 36, 0.3);
}

.table-size.size-critical {
  background: rgba(248, 113, 113, 0.15);
  color: #f87171;
  border-color: rgba(248, 113, 113, 0.3);
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
  font-size: 11px;
  color: #8b949e;
  margin-bottom: 4px;
}

.stat-value {
  font-size: 14px;
  font-weight: 600;
  color: #e6edf3;
  font-variant-numeric: tabular-nums;
}

.migration-controls {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
}

.retention-select {
  flex: 1;
  padding: 6px 10px;
  background: #0f1117;
  border: 1px solid #30363d;
  border-radius: 6px;
  color: #e6edf3;
  font-size: 13px;
}

.retention-select:focus {
  outline: none;
  border-color: #6366f1;
}

.migration-progress {
  margin-top: 12px;
}

.progress-bar {
  height: 6px;
  background: #21262d;
  border-radius: 3px;
  overflow: hidden;
  margin-bottom: 6px;
}

.progress-fill {
  height: 100%;
  background: linear-gradient(90deg, #6366f1, #818cf8);
  transition: width 0.3s;
}

.progress-text {
  font-size: 12px;
  color: #8b949e;
}

.migration-result {
  margin-top: 12px;
  padding: 10px 12px;
  border-radius: 6px;
  display: flex;
  gap: 8px;
  border: 1px solid transparent;
}

.migration-result.success {
  background: rgba(52, 211, 153, 0.1);
  border-color: rgba(52, 211, 153, 0.3);
}

.migration-result.partial {
  background: rgba(251, 191, 36, 0.1);
  border-color: rgba(251, 191, 36, 0.3);
}

.migration-result.failed {
  background: rgba(248, 113, 113, 0.1);
  border-color: rgba(248, 113, 113, 0.3);
}

.result-icon {
  font-size: 16px;
  font-weight: bold;
  color: inherit;
}

.migration-result.success .result-icon { color: #34d399; }
.migration-result.partial .result-icon { color: #fbbf24; }
.migration-result.failed .result-icon { color: #f87171; }

.result-content {
  flex: 1;
}

.result-message {
  font-size: 13px;
  color: #e6edf3;
  margin-bottom: 4px;
}

.result-warning {
  font-size: 12px;
  color: #fbbf24;
  background: rgba(251, 191, 36, 0.1);
  padding: 6px 8px;
  border-radius: 4px;
  margin-top: 6px;
  border: 1px solid rgba(251, 191, 36, 0.2);
}

/* ── 分区表列表 ── */
.tables-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.section-title {
  margin: 16px 0 10px 0;
  font-size: 14px;
  font-weight: 600;
  color: #e6edf3;
  border-left: 3px solid #6366f1;
  padding-left: 10px;
}

.partitioned-tables-wrap,
.partitions-table-wrap {
  overflow-x: auto;
}

.data-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}

.data-table th {
  text-align: left;
  padding: 10px 12px;
  background: #0f1117;
  border-bottom: 1px solid #30363d;
  font-weight: 500;
  color: #8b949e;
  white-space: nowrap;
}

.data-table td {
  padding: 10px 12px;
  border-bottom: 1px solid #30363d;
  color: #e6edf3;
  font-variant-numeric: tabular-nums;
}

.data-table tr:last-child td {
  border-bottom: none;
}

.data-table tr.active {
  background: rgba(99, 102, 241, 0.08);
}

.data-table tr.deleting {
  opacity: 0.5;
}

.data-table .empty-row {
  text-align: center;
  color: #8b949e;
  padding: 32px;
}

.tbl-code {
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 11px;
  padding: 2px 6px;
  background: #0f1117;
  border-radius: 4px;
  color: #818cf8;
}

.strong { color: #e6edf3; font-weight: 600; }
.dim { color: #8b949e; }

.pill {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 500;
}

.pill.warn {
  background: rgba(251, 191, 36, 0.15);
  color: #fbbf24;
}

.pill.dim { color: #6b7280; }

.storage-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 500;
  display: inline-block;
}

.storage-badge.columnar {
  background: rgba(99, 102, 241, 0.15);
  color: #818cf8;
}

.storage-badge.heap {
  background: #21262d;
  color: #8b949e;
}

.storage-badge.archive {
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
}

.month-badge {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 11px;
  font-weight: 500;
  font-variant-numeric: tabular-nums;
  display: inline-block;
}

.month-badge.month-recent {
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
}

.month-badge.month-medium {
  background: rgba(251, 191, 36, 0.15);
  color: #fbbf24;
}

.month-badge.month-old {
  background: #21262d;
  color: #8b949e;
}

.partition-list-section {
  margin-top: 8px;
}

/* 按钮 */
.btn {
  padding: 6px 14px;
  border: 1px solid transparent;
  border-radius: 6px;
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: all 0.15s;
  background: transparent;
  color: #e6edf3;
}

.btn-sm {
  padding: 5px 12px;
  font-size: 12px;
}

.btn-primary {
  background: #6366f1;
  border-color: #6366f1;
  color: #fff;
}

.btn-primary:hover:not(:disabled) {
  background: #818cf8;
  border-color: #818cf8;
}

.btn-danger {
  background: rgba(248, 113, 113, 0.15);
  border-color: rgba(248, 113, 113, 0.3);
  color: #f87171;
}

.btn-danger:hover:not(:disabled) {
  background: rgba(248, 113, 113, 0.25);
  border-color: #f87171;
}

.btn-ghost {
  background: transparent;
  border-color: #30363d;
  color: #8b949e;
}

.btn-ghost:hover:not(:disabled) {
  background: #21262d;
  border-color: #8b949e;
  color: #e6edf3;
}

.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

/* 删除确认弹窗 */
.modal-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.7);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 1000;
  padding: 20px;
}

.modal-dialog {
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 12px;
  width: 90%;
  max-width: 500px;
  box-shadow: 0 20px 25px -5px rgba(0, 0, 0, 0.5);
}

.modal-header {
  padding: 18px 24px;
  border-bottom: 1px solid #30363d;
}

.modal-header h3 {
  margin: 0;
  font-size: 16px;
  font-weight: 600;
  color: #e6edf3;
}

.modal-body {
  padding: 20px 24px;
}

.modal-footer {
  padding: 14px 24px;
  border-top: 1px solid #30363d;
  display: flex;
  justify-content: flex-end;
  gap: 12px;
}

.warning-box {
  display: flex;
  gap: 14px;
  background: rgba(251, 191, 36, 0.08);
  border: 1px solid rgba(251, 191, 36, 0.3);
  border-radius: 8px;
  padding: 16px;
}

.warning-icon {
  font-size: 22px;
  color: #fbbf24;
}

.warning-content {
  flex: 1;
}

.warning-content p {
  margin: 0 0 10px 0;
  color: #e6edf3;
  font-size: 13px;
  line-height: 1.6;
}

.warning-content p strong {
  color: #fbbf24;
}

.warning-content code {
  display: block;
  background: #0f1117;
  border: 1px solid #30363d;
  padding: 8px 10px;
  border-radius: 4px;
  margin: 8px 0;
  font-family: ui-monospace, SFMono-Regular, monospace;
  color: #818cf8;
  word-break: break-all;
}

.warning-content ul {
  margin: 10px 0;
  padding-left: 20px;
  color: #cbd5e1;
  font-size: 13px;
}

.warning-content ul li {
  margin: 4px 0;
}

.confirm-input {
  width: 100%;
  padding: 8px 12px;
  background: #0f1117;
  border: 1px solid #30363d;
  border-radius: 6px;
  color: #e6edf3;
  font-size: 13px;
  font-family: ui-monospace, SFMono-Regular, monospace;
  margin-top: 8px;
}

.confirm-input:focus {
  outline: none;
  border-color: #6366f1;
}

.empty-hint {
  text-align: center;
  padding: 32px;
  color: #8b949e;
  font-size: 13px;
}
</style>

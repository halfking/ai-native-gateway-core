<template>
  <div class="hot-partition-manager">
    <div v-if="globalError" class="alert alert-error">
      <span class="alert-icon">⚠️</span>
      {{ globalError }}
    </div>

    <!-- ── Hot 表迁移卡片网格 ────────────────────────────────── -->
    <div class="card">
      <div class="card-head">
        <div>
          <h3 class="card-title">{{ t('dataLifecycle.hotPartition.hotTableTitle') }}</h3>
          <p class="card-desc">{{ t('dataLifecycle.hotPartition.hotTableDesc') }}</p>
        </div>
        <div class="head-actions">
          <span class="retention-hint">
            默认保留
            <strong>{{ formatDays(defaultRetentionDays) }}</strong>
            ，后台每小时自动迁移
          </span>
          <button class="btn btn-sm btn-ghost" :disabled="loading" @click="loadAll">
            <span :class="['reload-icon', { spinning: loading }]">↻</span>
            刷新
          </button>
        </div>
      </div>

      <div class="hot-tables-grid">
        <div
          v-for="table in hotTables"
          :key="table.name"
          class="hot-table-card"
          :class="{ migrating: table.runningJobId !== null }"
        >
          <div class="table-header">
            <h4 class="table-name">{{ hotTableLabel(table.name) }}</h4>
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
            <el-radio-group
              v-model="table.retentionDays"
              class="retention-radio"
              :disabled="table.runningJobId !== null"
              :aria-label="hotTableLabel(table.name)"
              size="small"
            >
              <el-radio-button :label="1">
                {{ t('dataLifecycle.hotPartition.retention.1day') }}
              </el-radio-button>
              <el-radio-button :label="3">
                {{ t('dataLifecycle.hotPartition.retention.3day') }}
              </el-radio-button>
              <el-radio-button :label="7">
                {{ t('dataLifecycle.hotPartition.retention.7day') }}
              </el-radio-button>
              <el-radio-button :label="30">
                {{ t('dataLifecycle.hotPartition.retention.30day') }}
              </el-radio-button>
              <el-radio-button :label="0">
                {{ t('dataLifecycle.hotPartition.retention.all') }}
              </el-radio-button>
            </el-radio-group>

            <button
              type="button"
              class="btn btn-sm btn-primary start-migrate-btn"
              @click="promoteTable(table)"
              :disabled="table.runningJobId !== null"
            >
              <span v-if="table.runningJobId !== null" class="dot-pulse" />
              {{
                table.runningJobId !== null
                  ? t('dataLifecycle.hotPartition.migrating')
                  : t('dataLifecycle.hotPartition.startMigrate')
              }}
            </button>
          </div>

          <div
            v-if="table.runningJobId !== null && table.currentJob"
            class="migration-progress"
          >
            <div class="progress-bar">
              <div
                class="progress-fill"
                :style="{ width: progressPercent(table.currentJob) + '%' }"
              ></div>
            </div>
            <div class="progress-text">
              <span class="progress-message">
                {{
                  table.currentJob.progress?.message ||
                  t('dataLifecycle.hotPartition.migrationProgress', {
                    migrated: formatNumber(table.currentJob.progress?.done || 0),
                    batches: table.currentJob.progress?.batches || 0,
                    duration: Math.floor((table.currentJob.duration_ms || 0) / 1000),
                  })
                }}
              </span>
              <span class="progress-percent">{{ progressPercent(table.currentJob).toFixed(0) }}%</span>
            </div>
            <div class="progress-hint">
              <span class="live-tag">
                <span class="dot-pulse" />
                实时
              </span>
              每 2 秒刷新 · 关闭浏览器不影响后端执行
            </div>
          </div>

          <div
            v-else-if="table.lastResult && !table.runningJobId"
            class="migration-result"
            :class="table.lastResult.status"
          >
            <div class="result-icon">
              {{
                table.lastResult.status === 'succeeded'
                  ? '✓'
                  : table.lastResult.status === 'failed'
                  ? '✗'
                  : '⚠'
              }}
            </div>
            <div class="result-content">
              <div class="result-message">{{ table.lastResult.message }}</div>
              <div v-if="table.lastResult.warning" class="result-warning">
                ⚠️ {{ table.lastResult.warning }}
              </div>
              <div v-if="table.lastResult.migrated !== undefined" class="result-stats">
                迁移 <strong>{{ formatNumber(table.lastResult.migrated) }}</strong> 行 ·
                {{ table.lastResult.batches }} 批 ·
                {{ table.lastResult.duration }}s
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div v-if="recentJobs.length > 0" class="card">
      <div class="card-head">
        <h3 class="card-title">
          <span v-if="hasRunning" class="dot-pulse" />
          任务历史
        </h3>
        <span class="dim" style="font-size: 12px;">最近 {{ recentJobs.length }} 条</span>
      </div>

      <table class="data-table">
        <thead>
          <tr>
            <th>任务 ID</th>
            <th>类型</th>
            <th>目标</th>
            <th>状态</th>
            <th>进度</th>
            <th>耗时</th>
            <th>开始时间</th>
            <th>操作人</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="job in recentJobs"
            :key="job.run_id"
            :class="{ running: job.status === 'running' || job.status === 'queued' }"
          >
            <td>
              <code class="tbl-code">{{ shortId(job.run_id) }}</code>
            </td>
            <td>
              <span class="pill" :class="opPillClass(job.op)">{{ opLabel(job.op) }}</span>
            </td>
            <td class="dim">
              {{ job.params?.table_name || job.params?.partition_name || job.params?.table || '—' }}
            </td>
            <td>
              <span class="status-pill" :class="`status-${job.status}`">
                <span v-if="job.status === 'running'" class="dot-pulse" />
                {{ statusLabel(job.status) }}
              </span>
            </td>
            <td class="progress-cell">
              <div v-if="job.progress" class="mini-progress">
                <div
                  class="mini-progress-fill"
                  :style="{ width: (job.progress.percent || 0) + '%' }"
                ></div>
              </div>
              <span v-if="job.progress" class="dim">{{ job.progress.percent?.toFixed(0) }}%</span>
              <span v-else class="dim">—</span>
            </td>
            <td>{{ formatDuration(job.duration_ms) }}</td>
            <td class="dim">{{ job.started_at ? formatTime(job.started_at) : '—' }}</td>
            <td class="dim">{{ job.operator || '—' }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="card">
      <h3 class="card-title">{{ t('dataLifecycle.hotPartition.partitionTitle') }}</h3>
      <p class="card-desc">
        {{ t('dataLifecycle.hotPartition.partitionDesc') }}
        <strong>{{ t('dataLifecycle.hotPartition.partitionWarning') }}</strong>
      </p>

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
              <td><code class="tbl-code">{{ pTable.table_name }}</code></td>
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
              <tr
                v-for="partition in partitions"
                :key="partition.name"
                :class="{ deleting: partition.runningJobId !== null }"
              >
                <td><code class="tbl-code">{{ partition.name }}</code></td>
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
                    :disabled="partition.runningJobId !== null || deleting"
                  >
                    {{
                      partition.runningJobId !== null
                        ? t('dataLifecycle.hotPartition.partitionList.deleting')
                        : t('dataLifecycle.hotPartition.partitionList.delete')
                    }}
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
            :disabled="deleteConfirmInput !== deleteConfirm.name || deleteConfirm.runningJobId !== null"
          >
            {{
              deleteConfirm.runningJobId !== null
                ? t('dataLifecycle.hotPartition.deleteModal.deleting')
                : t('dataLifecycle.hotPartition.deleteModal.confirm')
            }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { ElMessage, ElMessageBox, ElRadioGroup, ElRadioButton } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { localeRef } from '@/i18n'
import { req } from '@/api/_core'
import {
  promoteHotTable, dropPartition, listLifecycleJobs, getLifecycleJob,
  type JobRun, type JobStatus,
} from '@/api/tuning'

interface HotTable {
  name: string
  label: string
  sizeHuman: string
  sizeBytes: number
  toastHuman: string
  rows: number
  /**
   * Retention in DAYS. Values: 1, 3, 7, 30, 0 (0 = migrate everything).
   * Converted to hours (×24) before sending to backend.
   * 修复历史 bug：原 UI 用 hours 但 label 显示“天”，admin 选“保留 1 天”时实际只保留 1 小时。
   */
  retentionDays: number
  runningJobId: string | null
  currentJob: JobRun | null
  lastResult: {
    status: 'succeeded' | 'failed' | 'partial'
    message: string
    warning?: string
    migrated?: number
    batches?: number
    duration?: number
    finishedAt?: number
  } | null
}

interface Partition {
  name: string
  storage: string
  storageLabel: string
  sizeHuman: string
  sizeBytes: number
  rows: number
  month: string
  runningJobId: string | null
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
const globalError = ref<string | null>(null)
const defaultRetentionDays = ref(1)

const hotTables = ref<HotTable[]>([
  { name: 'request_logs_hot', label: 'request_logs_hot', sizeHuman: '—', sizeBytes: 0, toastHuman: '—', rows: 0, retentionDays: 1, runningJobId: null, currentJob: null, lastResult: null },
  { name: 'credential_model_index_hot', label: 'credential_model_index_hot', sizeHuman: '—', sizeBytes: 0, toastHuman: '—', rows: 0, retentionDays: 1, runningJobId: null, currentJob: null, lastResult: null },
  { name: 'usage_ledger_hot', label: 'usage_ledger_hot', sizeHuman: '—', sizeBytes: 0, toastHuman: '—', rows: 0, retentionDays: 1, runningJobId: null, currentJob: null, lastResult: null },
  { name: 'routing_decision_log_hot', label: 'routing_decision_log_hot', sizeHuman: '—', sizeBytes: 0, toastHuman: '—', rows: 0, retentionDays: 1, runningJobId: null, currentJob: null, lastResult: null },
])

const partitionTables = ref<PartitionTable[]>([])
const selectedTable = ref<string>('')
const partitions = ref<Partition[]>([])
const deleteConfirm = ref<Partition | null>(null)
const deleteConfirmInput = ref('')

const recentJobs = ref<JobRun[]>([])
const hasRunning = computed(() =>
  hotTables.value.some((t2) => t2.runningJobId !== null) ||
  partitions.value.some((p) => p.runningJobId !== null) ||
  recentJobs.value.some((j) => j.status === 'running' || j.status === 'queued')
)
let pollTimer: ReturnType<typeof setInterval> | null = null

onMounted(async () => {
  await loadAll()
  startPolling()
})

onUnmounted(() => {
  stopPolling()
})

async function loadAll() {
  await Promise.all([
    loadHotTableStats(),
    loadPartitionTables(),
    loadJobHistory(),
  ])
}

async function loadHotTableStats() {
  try {
    loading.value = true
    const res = await req<any>('GET', '/api/admin/data-lifecycle/storage/tables')
    for (const table of hotTables.value) {
      const stat = res.tables?.find((t2: any) => t2.table === table.name)
      if (stat) {
        table.sizeHuman = stat.total_human
        table.sizeBytes = stat.total_bytes
        table.toastHuman = stat.toast_human || '0 B'
        table.rows = stat.rows || 0
      }
    }
  } catch (err: any) {
    console.error('加载 hot 表统计失败:', err)
    globalError.value = '加载 hot 表统计失败：' + (err.response?.data?.error || err.message)
  } finally {
    loading.value = false
  }
}

async function loadPartitionTables() {
  try {
    loading.value = true
    const res = await req<PartitionTable[]>('GET', '/api/admin/data-lifecycle/partitions')
    partitionTables.value = res || []
    if (selectedTable.value && !partitionTables.value.find((t2) => t2.table_name === selectedTable.value)) {
      selectedTable.value = ''
      partitions.value = []
    }
    if (!selectedTable.value && partitionTables.value.length > 0) {
      selectTable(partitionTables.value[0].table_name)
    } else if (selectedTable.value) {
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
    const t2 = partitionTables.value.find((t3) => t3.table_name === selectedTable.value)
    if (!t2) {
      partitions.value = []
      return
    }
    partitions.value = (t2.partitions || []).map((p: any) => ({
      name: p.partition_name,
      storage: p.is_columnar ? 'columnar' : p.is_archived ? 'archive' : 'heap',
      storageLabel:
        p.is_columnar ? t('dataLifecycle.hotPartition.partitionList.storage.columnar')
        : p.is_archived ? t('dataLifecycle.hotPartition.partitionList.storage.archive')
        : t('dataLifecycle.hotPartition.partitionList.storage.heap'),
      sizeHuman: p.size_human,
      sizeBytes: p.size_bytes,
      rows: p.row_count || 0,
      month: extractMonth(p.partition_name),
      runningJobId: null,
    }))
  } catch (err: any) {
    console.error('加载分区列表失败:', err)
    partitions.value = []
  } finally {
    loading.value = false
  }
}

async function promoteTable(table: HotTable) {
  const hoursLabel =
    table.retentionDays === 0
      ? t('dataLifecycle.hotPartition.promoteAll')
      : t('dataLifecycle.hotPartition.days', { n: table.retentionDays })
  try {
    await ElMessageBox.confirm(
      t('dataLifecycle.hotPartition.promoteConfirm', { label: hotTableLabel(table.name), hours: hoursLabel }),
      t('dataLifecycle.hotPartition.hotTableTitle'),
      {
        type: 'warning',
        confirmButtonText: t('dataLifecycle.hotPartition.startMigrate'),
        cancelButtonText: t('dataLifecycle.hotPartition.deleteModal.cancel'),
      }
    )
  } catch {
    return
  }

  // retentionDays=0 → 立即迁移全部（retention_hours=0）
  // 否则 days × 24 = hours
  const retentionHours = table.retentionDays === 0 ? 0 : table.retentionDays * 24

  try {
    const resp = await promoteHotTable({
      table_name: table.name,
      retention_hours: retentionHours,
      batch_size: 5000,
    })
    table.runningJobId = resp.run_id
    table.currentJob = {
      run_id: resp.run_id,
      op: 'promote_hot',
      status: 'queued',
      started_at: resp.started_at,
      duration_ms: 0,
      message: '已入队，等待开始…',
    }
    await loadJobHistory()
    if (pollTimer === null) startPolling()
  } catch (err: any) {
    ElMessage.error('启动迁移任务失败：' + (err.response?.data?.error || err.message))
  }
}

async function showDeleteConfirm(partition: Partition) {
  deleteConfirm.value = partition
  deleteConfirmInput.value = ''
}

async function executeDelete() {
  if (!deleteConfirm.value) return
  const partition = deleteConfirm.value
  if (partition.runningJobId !== null) return
  try {
    const resp = await dropPartition({
      partition_name: partition.name,
      confirm: true,
    })
    partition.runningJobId = resp.run_id
    ElMessage.info({
      message: `已调度删除任务 ${partition.name}`,
      duration: 4000,
    })
    deleteConfirm.value = null
  } catch (err: any) {
    const msg = err.response?.data?.error || err.message || ''
    ElMessage.error({ message: '提交删除任务失败：' + msg, duration: 8000 })
  }
}

async function loadJobHistory() {
  try {
    const res = await listLifecycleJobs(20)
    const allJobs = [...(res.running || []), ...(res.history || [])]
    recentJobs.value = allJobs
    syncJobToTableView(allJobs)
    syncJobToPartitionView(allJobs)
  } catch (err: any) {
    console.warn('loadJobHistory failed:', err)
  }
}

function syncJobToTableView(jobs: JobRun[]) {
  for (const table of hotTables.value) {
    const related = jobs.filter(
      (j) => j.op === 'promote_hot' && j.params?.table_name === table.name
    )
    if (related.length === 0) {
      if (table.runningJobId !== null) {
        table.runningJobId = null
        table.currentJob = null
      }
      continue
    }
    const running = related.find((j) => j.status === 'running' || j.status === 'queued')
    const latest = related[0]
    if (running) {
      table.runningJobId = running.run_id
      table.currentJob = running
      table.lastResult = null
    } else {
      table.runningJobId = null
      table.currentJob = null
      if (latest && (latest.status === 'succeeded' || latest.status === 'failed')) {
        const r = latest.result || {}
        const flagStatus =
          latest.status === 'succeeded'
            ? 'succeeded'
            : (r.total_migrated > 0 ? 'partial' : 'failed')
        table.lastResult = {
          status: flagStatus as any,
          message: latest.message || '',
          warning: r.warning,
          migrated: r.total_migrated,
          batches: r.batches_executed,
          duration: r.duration_seconds,
          finishedAt: latest.finished_at ? new Date(latest.finished_at).getTime() : Date.now(),
        }
      }
    }
  }
}

function syncJobToPartitionView(jobs: JobRun[]) {
  for (const partition of partitions.value) {
    const related = jobs.filter(
      (j) => j.op === 'drop_partition' && j.params?.partition_name === partition.name
    )
    if (related.length === 0) {
      partition.runningJobId = null
      continue
    }
    const running = related.find((j) => j.status === 'running' || j.status === 'queued')
    if (running) {
      partition.runningJobId = running.run_id
    } else {
      partition.runningJobId = null
    }
  }
}

function startPolling() {
  if (pollTimer) return
  pollTimer = setInterval(async () => {
    await loadJobHistory()
    if (!hasRunning.value) {
      await loadHotTableStats()
    }
  }, 2000)
}

function stopPolling() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}

function hotTableLabel(tableName: string): string {
  const labels: Record<string, string> = {
    request_logs_hot: 'requestLogs',
    credential_model_index_hot: 'credentialModelIndex',
    usage_ledger_hot: 'usageLedger',
    routing_decision_log_hot: 'routingDecisionLog',
  }
  return t(`dataLifecycle.hotPartition.hotTableNames.${labels[tableName] || tableName}`)
}

function shortId(id: string): string {
  return id.length > 22 ? id.slice(0, 18) + '…' : id
}

function opLabel(op: string): string {
  const map: Record<string, string> = {
    promote_hot: 'hot 迁移',
    drop_partition: '删除分区',
    vacuum: 'VACUUM',
    vacuum_full: 'VACUUM FULL',
    reindex: 'REINDEX',
  }
  return map[op] || op
}

function opPillClass(op: string): string {
  if (op === 'promote_hot') return 'pill-info'
  if (op === 'drop_partition') return 'pill-warn'
  return 'pill-dim'
}

function statusLabel(status: JobStatus): string {
  const map: Record<JobStatus, string> = {
    queued: '排队中',
    running: '运行中',
    succeeded: '成功',
    failed: '失败',
    cancelled: '已取消',
  }
  return map[status] || status
}

function formatDuration(ms: number): string {
  if (!ms || ms < 1000) return `${ms || 0}ms`
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  const rs = s % 60
  return `${m}m ${rs}s`
}

function formatTime(iso: string): string {
  return new Date(iso).toLocaleString(localeRef.value, {
    month: 'short', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit',
  })
}

function formatDays(d: number): string {
  if (d === 0) return '立即迁移全部'
  if (d < 1) return `${Math.round(d * 24)} 小时`
  return `${d} 天`
}

function progressPercent(job: JobRun | null): number {
  if (!job) return 0
  if (job.progress?.percent) return Math.min(100, job.progress.percent)
  return job.status === 'running' ? 5 : 0
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
  return (num || 0).toLocaleString(localeRef.value)
}
</script>

<style scoped>
.hot-partition-manager {
  padding: 20px;
}

.alert {
  display: flex;
  gap: 10px;
  align-items: flex-start;
  padding: 12px 16px;
  border-radius: 8px;
  margin-bottom: 16px;
  font-size: 13px;
}

.alert-error {
  background: rgba(248, 113, 113, 0.1);
  border: 1px solid rgba(248, 113, 113, 0.3);
  color: #f87171;
}

.alert-icon {
  font-size: 16px;
}

.card {
  background: #161b22;
  border: 1px solid #30363d;
  border-radius: 10px;
  padding: 20px;
  margin-bottom: 24px;
}

.card-head {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}

.card-head > div:first-child {
  flex: 1;
  min-width: 0;
}

.head-actions {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-shrink: 0;
}

.retention-hint {
  font-size: 12px;
  color: #8b949e;
  background: #0d1117;
  padding: 5px 10px;
  border-radius: 6px;
  border: 1px solid #30363d;
}

.retention-hint strong {
  color: #fbbf24;
  margin: 0 4px;
}

.reload-icon {
  display: inline-block;
  font-size: 14px;
  transition: transform 0.2s;
}

.reload-icon.spinning {
  animation: spin 1s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.card-title {
  font-size: 16px;
  font-weight: 600;
  margin: 0 0 6px 0;
  color: #e6edf3;
}

.card-desc {
  color: #8b949e;
  margin: 0 0 16px 0;
  font-size: 13px;
  line-height: 1.6;
}

.card-desc strong {
  color: #fbbf24;
}

.hot-tables-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(360px, 1fr));
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
  background: rgba(251, 191, 36, 0.06);
  border-color: rgba(251, 191, 36, 0.4);
  box-shadow: 0 0 0 1px rgba(251, 191, 36, 0.2);
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
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: 12px;
  align-items: center;
}

.retention-radio {
  flex: 1 1 100%;
  display: flex;
  flex-wrap: wrap;
  gap: 0;
}

.start-migrate-btn {
  flex: 0 0 auto;
  min-width: 100px;
}

:global(.retention-radio .el-radio-button__inner) {
  padding: 6px 12px;
  font-size: 12px;
  border-radius: 0;
  background: transparent;
  border-color: #30363d;
  color: #8b949e;
}

:global(.retention-radio .el-radio-button:first-child .el-radio-button__inner) {
  border-top-left-radius: 6px;
  border-bottom-left-radius: 6px;
}

:global(.retention-radio .el-radio-button:last-child .el-radio-button__inner) {
  border-top-right-radius: 6px;
  border-bottom-right-radius: 6px;
}

:global(.retention-radio .el-radio-button__original-radio:checked + .el-radio-button__inner) {
  background: rgba(99, 102, 241, 0.2);
  border-color: #6366f1;
  color: #c7d2fe;
  box-shadow: -1px 0 0 0 #6366f1;
}

:global(.retention-radio .el-radio-button.is-active .el-radio-button__inner) {
  background: rgba(99, 102, 241, 0.2);
  border-color: #6366f1;
  color: #c7d2fe;
}

:global(.retention-radio .el-radio-button__inner:hover) {
  color: #e6edf3;
}

.migration-progress {
  margin-top: 12px;
  padding: 10px;
  background: #0d1117;
  border-radius: 6px;
  border: 1px solid #30363d;
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
  transition: width 0.3s ease;
}

.progress-text {
  font-size: 12px;
  color: #cbd5e1;
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 8px;
}

.progress-message {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.progress-percent {
  flex-shrink: 0;
  color: #818cf8;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}

.progress-hint {
  font-size: 11px;
  color: #6b7280;
  margin-top: 4px;
  display: flex;
  align-items: center;
  gap: 6px;
}

.live-tag {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
  padding: 1px 6px;
  border-radius: 8px;
  font-weight: 500;
  font-size: 10px;
}

.dot-pulse {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
  animation: pulse 1.5s ease-in-out infinite;
}

@keyframes pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.3; }
}

.migration-result {
  margin-top: 12px;
  padding: 10px 12px;
  border-radius: 6px;
  display: flex;
  gap: 8px;
  border: 1px solid transparent;
}

.migration-result.succeeded {
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
}

.migration-result.succeeded .result-icon { color: #34d399; }
.migration-result.partial .result-icon { color: #fbbf24; }
.migration-result.failed .result-icon { color: #f87171; }

.result-content {
  flex: 1;
  min-width: 0;
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

.result-stats {
  font-size: 11px;
  color: #8b949e;
  margin-top: 4px;
  font-variant-numeric: tabular-nums;
}

.result-stats strong {
  color: #e6edf3;
  font-weight: 600;
}

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
  opacity: 0.6;
}

.data-table tr.running td {
  background: rgba(99, 102, 241, 0.04);
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

.pill-info {
  background: rgba(99, 102, 241, 0.15);
  color: #818cf8;
}

.pill-warn {
  background: rgba(251, 191, 36, 0.15);
  color: #fbbf24;
}

.pill-dim {
  background: #21262d;
  color: #6b7280;
}

.status-pill {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 500;
}

.status-queued {
  background: #21262d;
  color: #8b949e;
}

.status-running {
  background: rgba(99, 102, 241, 0.15);
  color: #818cf8;
}

.status-running .dot-pulse { background: #818cf8; }

.status-succeeded {
  background: rgba(52, 211, 153, 0.15);
  color: #34d399;
}

.status-failed {
  background: rgba(248, 113, 113, 0.15);
  color: #f87171;
}

.status-cancelled {
  background: #21262d;
  color: #8b949e;
}

.progress-cell {
  display: flex;
  align-items: center;
  gap: 8px;
}

.mini-progress {
  width: 80px;
  height: 4px;
  background: #21262d;
  border-radius: 2px;
  overflow: hidden;
}

.mini-progress-fill {
  height: 100%;
  background: linear-gradient(90deg, #6366f1, #818cf8);
  transition: width 0.3s ease;
}

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
  display: inline-flex;
  align-items: center;
  gap: 6px;
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

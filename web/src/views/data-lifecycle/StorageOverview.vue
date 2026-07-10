<template>
  <div class="storage-overview">
    <div class="grid-2">
      <!-- 数据库 -->
      <div class="card storage-card">
        <h3 class="card-title">
          {{ t('dataLifecycle.storageOverview.summary.databaseTitle') }}
          <span class="badge" v-if="data?.database.server_version">
            v{{ data.database.server_version.split(' ')[0] }}
          </span>
        </h3>
        <div class="big-stat">
          <div class="big-value">{{ data?.database.total_human || '—' }}</div>
          <div class="big-label">{{ t('dataLifecycle.storageOverview.summary.databaseSubtitle') }}</div>
        </div>
        <div class="metric-row">
          <span>Database</span>
          <span class="metric-val">{{ data?.database.database_human || '—' }}</span>
        </div>
        <div class="metric-row">
          <span>Tables</span>
          <span class="metric-val">{{ humanBytes(data?.database.tables_bytes) }}</span>
        </div>
        <div class="metric-row">
          <span>Indexes</span>
          <span class="metric-val">{{ humanBytes(data?.database.indexes_bytes) }}</span>
        </div>
        <div class="metric-row">
          <span>TOAST</span>
          <span class="metric-val">{{ humanBytes(data?.database.toast_bytes) }}</span>
        </div>
        <div class="bar-track" v-if="data">
          <div class="bar-seg tables" :style="{ width: pct(data.database.tables_bytes) + '%' }" title="Tables"></div>
          <div class="bar-seg indexes" :style="{ width: pct(data.database.indexes_bytes) + '%' }" title="Indexes"></div>
          <div class="bar-seg toast" :style="{ width: pct(data.database.toast_bytes) + '%' }" title="TOAST"></div>
        </div>
        <div class="legend">
          <span><i class="dot tables"></i>Tables</span>
          <span><i class="dot indexes"></i>Indexes</span>
          <span><i class="dot toast"></i>TOAST</span>
        </div>
      </div>

      <!-- 本机磁盘 -->
      <div class="card storage-card">
        <h3 class="card-title">
          {{ t('dataLifecycle.storageOverview.summary.diskTitle') }}
          <span class="badge" :class="diskBadge(data?.filesystem.used_percent || 0)">
            {{ data?.filesystem.used_percent || 0 }}%
          </span>
        </h3>
        <div class="big-stat">
          <div class="big-value">{{ data?.filesystem.used_human || '—' }}</div>
          <div class="big-label">{{ t('dataLifecycle.storageOverview.summary.diskUsedOf', { total: data?.filesystem.total_human || '—' }) }}</div>
        </div>
        <div class="metric-row">
          <span>{{ t('dataLifecycle.storageOverview.summary.path') }}</span>
          <span class="metric-val mono">{{ data?.filesystem.path || '—' }}</span>
        </div>
        <div class="metric-row">
          <span>{{ t('dataLifecycle.storageOverview.summary.used') }}</span>
          <span class="metric-val">{{ data?.filesystem.used_human }}</span>
        </div>
        <div class="metric-row">
          <span>{{ t('dataLifecycle.storageOverview.summary.free') }}</span>
          <span class="metric-val">{{ data?.filesystem.free_human }}</span>
        </div>
        <div class="bar-track">
          <div
            class="bar-seg"
            :class="diskClass(data?.filesystem.used_percent || 0)"
            :style="{ width: (data?.filesystem.used_percent || 0) + '%' }"
          ></div>
        </div>
      </div>
    </div>

    <!-- 列存 (citus_columnar) — 单独一行展示 -->
    <div class="card columnar-card">
      <h3 class="card-title">
        {{ t('dataLifecycle.storageOverview.summary.columnarTitle') }}
        <span v-if="data?.columnar.available" class="badge ok">{{ t('dataLifecycle.storageOverview.summary.enabled') }}</span>
        <span v-else class="badge warn">{{ t('dataLifecycle.storageOverview.summary.notInstalled') }}</span>
      </h3>
      <div v-if="data?.columnar.available" class="columnar-grid">
        <div class="columnar-stat">
          <div class="stat-label">{{ t('dataLifecycle.storageOverview.summary.columnarTableCount') }}</div>
          <div class="stat-value">{{ data.columnar.table_count }}</div>
          <div class="stat-meta">{{ t('dataLifecycle.storageOverview.summary.columnarTableCountMeta') }}</div>
        </div>
        <div class="columnar-stat">
          <div class="stat-label">{{ t('dataLifecycle.storageOverview.summary.columnarTotalColumns') }}</div>
          <div class="stat-value">{{ data.columnar.total_columns }}</div>
          <div class="stat-meta">{{ t('dataLifecycle.storageOverview.summary.columnarTotalColumnsMeta') }}</div>
        </div>
        <div class="columnar-stat">
          <div class="stat-label">{{ t('dataLifecycle.storageOverview.summary.columnarSize') }}</div>
          <div class="stat-value">{{ data.columnar.total_human || '—' }}</div>
          <div class="stat-meta">{{ t('dataLifecycle.storageOverview.summary.columnarSizeMeta') }}</div>
        </div>
        <div class="columnar-stat">
          <div class="stat-label">{{ t('dataLifecycle.storageOverview.summary.columnarDbShare') }}</div>
          <div class="stat-value">{{ columnarPctOfDB }}%</div>
          <div class="stat-meta">{{ t('dataLifecycle.storageOverview.summary.columnarDbShareMeta') }}</div>
        </div>
      </div>
      <div v-else class="empty-hint">
        ⚠️ {{ data?.columnar.note || t('dataLifecycle.storageOverview.summary.columnarNotInstalledFallback') }}
      </div>
      <p v-if="data?.columnar.available && data.columnar.note" class="columnar-note">
        {{ data.columnar.note }}
      </p>
    </div>

    <!-- 本机日志目录 -->
    <div v-if="data?.local_logs" class="card local-logs-card">
      <h3 class="card-title">
        {{ t('dataLifecycle.storageOverview.summary.localLogsTitle') }}
        <span v-if="!data.local_logs.exists" class="badge warn">{{ t('dataLifecycle.storageOverview.summary.notFound') }}</span>
      </h3>
      <div v-if="data.local_logs.exists" class="local-logs-grid">
        <div class="metric-row">
          <span>{{ t('dataLifecycle.storageOverview.summary.path') }}</span>
          <span class="metric-val mono">{{ data.local_logs.path }}</span>
        </div>
        <div class="metric-row">
          <span>{{ t('dataLifecycle.storageOverview.summary.fileCount') }}</span>
          <span class="metric-val">{{ fmtNum(data.local_logs.files) }}</span>
        </div>
        <div class="metric-row">
          <span>{{ t('dataLifecycle.storageOverview.summary.size') }}</span>
          <span class="metric-val">{{ data.local_logs.size_human }}</span>
        </div>
        <div class="metric-row">
          <span>{{ t('dataLifecycle.storageOverview.summary.oldest') }}</span>
          <span class="metric-val">{{ formatTime(data.local_logs.oldest_mtime) }}</span>
        </div>
        <div class="metric-row">
          <span>{{ t('dataLifecycle.storageOverview.summary.newest') }}</span>
          <span class="metric-val">{{ formatTime(data.local_logs.newest_mtime) }}</span>
        </div>
      </div>
      <div v-else class="empty-hint">{{ t('dataLifecycle.storageOverview.summary.localLogsMissingHint') }}</div>
    </div>

    <!-- 警告 -->
    <div v-if="data && data.warnings && data.warnings.length" class="warnings-card">
      <h3 class="card-title">⚠️ {{ t('dataLifecycle.storageOverview.summary.warningsTitle') }}</h3>
      <ul class="warnings-list">
        <li v-for="(w, i) in data.warnings" :key="i">{{ w }}</li>
      </ul>
    </div>

    <!-- 表级 Top-N -->
    <div class="card tables-card">
      <div class="card-header">
        <h3 class="card-title">{{ t('dataLifecycle.storageOverview.tablesTitle', { n: tables.length }) }}</h3>
        <div class="header-actions">
          <button class="btn btn-ghost btn-sm" @click="loadTables" :disabled="loadingTables">
            {{ loadingTables ? t('dataLifecycle.storageOverview.refreshLoading') : t('dataLifecycle.storageOverview.refresh') }}
          </button>
        </div>
      </div>
      <div class="tables-table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('dataLifecycle.storageOverview.columns.tableName') }}</th>
              <th>{{ t('dataLifecycle.storageOverview.columns.rows') }}</th>
              <th>{{ t('dataLifecycle.storageOverview.columns.totalSize') }}</th>
              <th>{{ t('dataLifecycle.storageOverview.columns.indexes') }}</th>
              <th>{{ t('dataLifecycle.storageOverview.columns.toast') }}</th>
              <th>{{ t('dataLifecycle.storageOverview.columns.percent') }}</th>
              <th>{{ t('dataLifecycle.storageOverview.columns.partition') }}</th>
              <th>{{ t('dataLifecycle.storageOverview.columns.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="tRow in tables" :key="tRow.table">
              <td>
                <code class="tbl-code">{{ tRow.schema }}.{{ tRow.table }}</code>
              </td>
              <td>{{ formatNumber(tRow.rows) }}</td>
              <td class="strong">{{ tRow.total_human }}</td>
              <td class="dim">{{ humanBytes(tRow.index_bytes) }}</td>
              <td class="dim">{{ humanBytes(tRow.toast_bytes) }}</td>
              <td>
                <div class="pct-track">
                  <div class="pct-fill" :style="{ width: tRow.percent_of_db + '%' }"></div>
                  <span class="pct-text">{{ tRow.percent_of_db }}%</span>
                </div>
              </td>
              <td>
                <span v-if="tRow.is_partitioned" class="pill warn">{{ t('dataLifecycle.storageOverview.partitionBadge') }}</span>
                <span v-else class="pill dim">{{ t('dataLifecycle.storageOverview.partitionNone') }}</span>
              </td>
              <td>
                <div class="row-actions">
                  <button
                    class="btn btn-vacuum btn-xs"
                    :disabled="busy[tRow.table] !== undefined"
                    :title="t('dataLifecycle.storageOverview.buttons.vacuumTitle', { table: tRow.table })"
                    @click="confirmAndRun('VACUUM', tRow)"
                  >{{ t('dataLifecycle.storageOverview.buttons.vacuum') }}</button>
                  <button
                    class="btn btn-vacuum-full btn-xs"
                    :disabled="busy[tRow.table] !== undefined"
                    :title="t('dataLifecycle.storageOverview.buttons.vacuumFullTitle', { table: tRow.table })"
                    @click="confirmAndRun('VACUUM FULL', tRow)"
                  >{{ t('dataLifecycle.storageOverview.buttons.vacuumFull') }}</button>
                  <button
                    class="btn btn-reindex btn-xs"
                    :disabled="busy[tRow.table] !== undefined"
                    :title="t('dataLifecycle.storageOverview.buttons.reindexTitle', { table: tRow.table })"
                    @click="confirmAndRun('REINDEX', tRow)"
                  >{{ t('dataLifecycle.storageOverview.buttons.reindex') }}</button>
                </div>
                <div v-if="busy[tRow.table]" class="row-status">
                  <span class="spinner"></span>
                  <span class="status-text">{{ busy[tRow.table] }}</span>
                </div>
                <div v-else-if="lastResult[tRow.table]" class="row-result" :class="lastResult[tRow.table]!.success ? 'ok' : 'err'">
                  <span v-if="lastResult[tRow.table]!.success">
                    ✓ {{
                      t('dataLifecycle.storageOverview.rowResult.success', {
                        op: lastResult[tRow.table]!.operation,
                        size: lastResult[tRow.table]!.size_saved_human,
                        pct: lastResult[tRow.table]!.reclaimed_pct,
                        ms: lastResult[tRow.table]!.duration_ms,
                      })
                    }}
                  </span>
                  <span v-else>
                    ✗ {{
                      t('dataLifecycle.storageOverview.rowResult.failed', {
                        op: lastResult[tRow.table]!.operation,
                        msg: lastResult[tRow.table]!.message,
                      })
                    }}
                  </span>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 风险确认弹窗 -->
    <div v-if="confirmOp" class="modal-backdrop" @click.self="cancelConfirm">
      <div class="modal modal-wide">
        <h3 class="modal-title">
          <span v-if="confirmOp.op === 'VACUUM'">{{ t('dataLifecycle.storageOverview.modal.vacuumTitle') }}</span>
          <span v-else-if="confirmOp.op === 'VACUUM FULL'">{{ t('dataLifecycle.storageOverview.modal.vacuumFullTitle') }}</span>
          <span v-else>{{ t('dataLifecycle.storageOverview.modal.reindexTitle') }}</span>
        </h3>
        <div class="modal-body">
          <p>
            {{
              t('dataLifecycle.storageOverview.modal.intro', {
                table: confirmOp.t.schema + '.' + confirmOp.t.table,
                op: confirmOp.op,
              })
            }}
          </p>
          <div class="modal-info">
            <div><strong>{{ t('dataLifecycle.storageOverview.modal.currentSize') }}</strong>{{ confirmOp.t.total_human }}</div>
            <div><strong>{{ t('dataLifecycle.storageOverview.modal.rowCount') }}</strong>{{ formatNumber(confirmOp.t.rows) }}</div>
            <div>
              <strong>{{ t('dataLifecycle.storageOverview.modal.partition') }}</strong>
              <span :class="confirmOp.t.is_partitioned ? 'pill warn' : 'pill dim'">
                {{ confirmOp.t.is_partitioned ? t('dataLifecycle.storageOverview.modal.partitionYes') : t('dataLifecycle.storageOverview.modal.partitionNo') }}
              </span>
            </div>
          </div>

          <div class="modal-section">
            <div class="modal-section-title">① {{ t('dataLifecycle.storageOverview.modal.flow') }}</div>
            <div class="modal-section-body">{{ flowText }}</div>
          </div>
          <div class="modal-section" :class="{ 'modal-section-impact': confirmOp.op !== 'VACUUM' }">
            <div class="modal-section-title">② {{ t('dataLifecycle.storageOverview.modal.impact') }}</div>
            <div class="modal-section-body">{{ impactText }}</div>
          </div>
          <div class="modal-section">
            <div class="modal-section-title">③ {{ t('dataLifecycle.storageOverview.modal.disk') }}</div>
            <div class="modal-section-body">{{ diskText }}</div>
          </div>
          <div class="modal-section">
            <div class="modal-section-title">④ {{ t('dataLifecycle.storageOverview.modal.time') }}</div>
            <div class="modal-section-body">{{ timeText }}</div>
          </div>
        </div>
        <div class="modal-actions">
          <button class="btn btn-ghost" @click="cancelConfirm">
            {{ t('dataLifecycle.storageOverview.modal.cancel') }}
          </button>
          <button
            class="btn"
            :class="confirmOp.op === 'VACUUM' ? 'btn-vacuum' : confirmOp.op === 'VACUUM FULL' ? 'btn-vacuum-full' : 'btn-reindex'"
            @click="executeOp"
          >{{ t('dataLifecycle.storageOverview.modal.confirm', { op: confirmOp.op }) }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, reactive } from 'vue'
import { ElMessage } from 'element-plus'
import { useI18n } from 'vue-i18n'
import { localeRef } from '../../i18n'
import {
  dataLifecycleStorage,
  dataLifecycleTableSizes,
  dataLifecycleTableVacuum,
  dataLifecycleTableVacuumFull,
  dataLifecycleTableReindex,
  type StorageOverview,
  type TableSizeInfo,
  type TableMaintenanceRequest,
  type TableMaintenanceResponse,
} from '../../api'

const { t } = useI18n()

const data = ref<StorageOverview | null>(null)
const tables = ref<TableSizeInfo[]>([])
const loadingTables = ref(false)

// ── 表级维护操作状态 ─────────────────────────────────────
// busy: 每张表正在执行的操作名（用于 spinner 展示 + 按钮 disable）
const busy = reactive<Record<string, string>>({})
// lastResult: 每张表最后一次操作的结果（成功/失败 + 节省空间）
const lastResult = reactive<Record<string, (TableMaintenanceResponse & { size_saved_human: string }) | null>>({})
// confirmOp: 风险确认弹窗的当前目标
const confirmOp = ref<{ op: 'VACUUM' | 'VACUUM FULL' | 'REINDEX'; t: TableSizeInfo } | null>(null)

// 列存占 DB 的比例
const columnarPctOfDB = computed(() => {
  if (!data.value?.columnar.available) return 0
  const col = data.value.columnar.total_bytes
  const db = data.value.database.total_bytes
  if (!db || db <= 0) return 0
  return ((col / db) * 100).toFixed(1)
})

// ── 弹窗：根据当前操作类型拉取 4 段文案 ────────────────────────
function opKey(op: 'VACUUM' | 'VACUUM FULL' | 'REINDEX' | undefined): 'vacuum' | 'vacuumFull' | 'reindex' | '' {
  if (op === 'VACUUM') return 'vacuum'
  if (op === 'VACUUM FULL') return 'vacuumFull'
  if (op === 'REINDEX') return 'reindex'
  return ''
}

const flowText = computed(() => {
  const k = opKey(confirmOp.value?.op)
  return k ? t(`dataLifecycle.storageOverview.op.${k}.flow`) : ''
})

const impactText = computed(() => {
  const k = opKey(confirmOp.value?.op)
  return k ? t(`dataLifecycle.storageOverview.op.${k}.impact`) : ''
})

const diskText = computed(() => {
  const k = opKey(confirmOp.value?.op)
  return k ? t(`dataLifecycle.storageOverview.op.${k}.disk`) : ''
})

const timeText = computed(() => {
  const k = opKey(confirmOp.value?.op)
  if (!k) return ''
  if (k === 'reindex') {
    return t(`dataLifecycle.storageOverview.op.${k}.time`, { size: humanBytes(confirmOp.value?.t.index_bytes) })
  }
  return t(`dataLifecycle.storageOverview.op.${k}.time`)
})

// ── 表级维护操作 handler ─────────────────────────────────
function confirmAndRun(op: 'VACUUM' | 'VACUUM FULL' | 'REINDEX', tRow: TableSizeInfo) {
  confirmOp.value = { op, t: tRow }
}

function cancelConfirm() {
  confirmOp.value = null
}

async function executeOp() {
  if (!confirmOp.value) return
  const { op, t: tRow } = confirmOp.value
  confirmOp.value = null

  const body: TableMaintenanceRequest = { schema: tRow.schema, table: tRow.table }
  busy[tRow.table] = op === 'VACUUM FULL' ? 'VACUUM FULL 中…' : `${op} 中…`

  try {
    let resp: TableMaintenanceResponse
    if (op === 'VACUUM') {
      resp = await dataLifecycleTableVacuum(body)
    } else if (op === 'VACUUM FULL') {
      resp = await dataLifecycleTableVacuumFull(body)
    } else {
      resp = await dataLifecycleTableReindex(body)
    }
    lastResult[tRow.table] = {
      ...resp,
      size_saved_human: humanBytes(resp.size_saved_bytes),
    }
    if (resp.success) {
      ElMessage.success({
        message: t('dataLifecycle.storageOverview.modal.success', {
          op,
          table: tRow.table,
          size: humanBytes(resp.size_saved_bytes),
          pct: resp.reclaimed_pct,
        }),
        duration: 5000,
      })
      // 刷新表格数据，反映新大小
      await loadTables()
    } else {
      ElMessage.error({
        message: t('dataLifecycle.storageOverview.modal.failed', { op, msg: resp.message }),
        duration: 8000,
      })
    }
  } catch (e: any) {
    const msg = e?.response?.data?.message || e?.message || String(e)
    lastResult[tRow.table] = {
      schema: tRow.schema, table: tRow.table, operation: op,
      success: false, message: msg, duration_ms: 0,
      size_before_bytes: 0, size_after_bytes: 0, size_saved_bytes: 0,
      reclaimed_pct: 0, started_at: '', finished_at: '',
      size_saved_human: '0 B',
    }
    ElMessage.error({
      message: t('dataLifecycle.storageOverview.modal.failed', { op, msg }),
      duration: 8000,
    })
  } finally {
    delete busy[tRow.table]
  }
}

async function load() {
  try {
    data.value = await dataLifecycleStorage()
  } catch (e) {
    console.error('storage load failed', e)
  }
}

async function loadTables() {
  loadingTables.value = true
  try {
    const r = await dataLifecycleTableSizes(20)
    tables.value = r.tables
  } catch (e) {
    console.error('table sizes load failed', e)
  } finally {
    loadingTables.value = false
  }
}

function pct(n: number | undefined): number {
  if (!n || !data.value) return 0
  const total = data.value.database.tables_bytes + data.value.database.indexes_bytes + data.value.database.toast_bytes
  if (total === 0) return 0
  return (n / total) * 100
}

function diskClass(p: number): string {
  if (p >= 90) return 'danger'
  if (p >= 75) return 'warn'
  return 'ok'
}

function diskBadge(p: number): string {
  if (p >= 90) return 'danger'
  if (p >= 75) return 'warn'
  return 'ok'
}

function humanBytes(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(1)} ${units[i]}`
}

function formatNumber(n: number): string {
  return n.toLocaleString(localeRef.value)
}

function formatTime(unix: number): string {
  if (!unix) return '—'
  return new Date(unix * 1000).toISOString().slice(0, 19).replace('T', ' ')
}

defineExpose({ load, loadTables })
onMounted(() => {
  load()
  loadTables()
})

function fmtNum(n: number) {
  return n.toLocaleString(localeRef.value)
}
</script>

<style scoped>
.storage-overview {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.grid-2 {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}

.storage-card {
  margin-bottom: 0;
}

.card-title {
  display: flex;
  align-items: center;
  justify-content: space-between;
}

.badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 500;
  background: rgba(99, 102, 241, 0.15);
  color: #818cf8;
}
.badge.warn { background: rgba(251, 191, 36, 0.15); color: #fbbf24; }
.badge.danger { background: rgba(248, 113, 113, 0.15); color: #f87171; }
.badge.ok { background: rgba(52, 211, 153, 0.15); color: #34d399; }

.big-stat {
  margin: 12px 0 16px;
}
.big-value {
  font-size: 28px;
  font-weight: 700;
  color: #e6edf3;
  font-variant-numeric: tabular-nums;
}
.big-label {
  font-size: 12px;
  color: #8b949e;
  margin-top: 2px;
}

.metric-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 6px 0;
  font-size: 13px;
  border-bottom: 1px solid rgba(48, 54, 61, 0.5);
}
.metric-row:last-child {
  border-bottom: none;
}
.metric-row > span:first-child {
  color: #8b949e;
}
.metric-val {
  color: #e6edf3;
  font-variant-numeric: tabular-nums;
}
.metric-val.mono {
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 11px;
  color: #818cf8;
  max-width: 70%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dim { color: #6b7280; }
.strong { color: #e6edf3; font-weight: 600; }

.bar-track {
  display: flex;
  height: 8px;
  background: #0f1117;
  border-radius: 4px;
  overflow: hidden;
  margin-top: 12px;
}
.bar-seg {
  height: 100%;
  transition: width 0.3s;
}
.bar-seg.tables { background: #6366f1; }
.bar-seg.indexes { background: #34d399; }
.bar-seg.toast { background: #fbbf24; }
.bar-seg.ok { background: #34d399; }
.bar-seg.warn { background: #fbbf24; }
.bar-seg.danger { background: #f87171; }

.legend {
  display: flex;
  gap: 16px;
  margin-top: 8px;
  font-size: 11px;
  color: #8b949e;
}
.legend i.dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-right: 4px;
  vertical-align: middle;
}
.legend i.dot.tables { background: #6366f1; }
.legend i.dot.indexes { background: #34d399; }
.legend i.dot.toast { background: #fbbf24; }

.local-logs-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px 24px;
}

.warnings-card {
  background: rgba(251, 191, 36, 0.08);
  border: 1px solid rgba(251, 191, 36, 0.3);
  border-radius: 10px;
  padding: 12px 16px;
}
.warnings-card .card-title { color: #fbbf24; }
.warnings-list {
  margin: 0;
  padding-left: 20px;
  font-size: 13px;
  color: #fbbf24;
}
.warnings-list li { margin: 4px 0; }

.card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.tables-table-wrap {
  overflow-x: auto;
}
.tbl-code {
  font-family: ui-monospace, SFMono-Regular, monospace;
  font-size: 11px;
  padding: 2px 6px;
  background: #0f1117;
  border-radius: 4px;
  color: #818cf8;
}

.pct-track {
  position: relative;
  width: 100%;
  height: 18px;
  background: #0f1117;
  border-radius: 4px;
  overflow: hidden;
}
.pct-fill {
  position: absolute;
  top: 0; left: 0; height: 100%;
  background: linear-gradient(90deg, rgba(99, 102, 241, 0.5), rgba(99, 102, 241, 0.8));
  transition: width 0.3s;
}
.pct-text {
  position: absolute;
  top: 0; left: 8px; line-height: 18px;
  font-size: 11px; color: #e6edf3;
  font-variant-numeric: tabular-nums;
}

.pill {
  display: inline-block;
  padding: 1px 8px;
  border-radius: 10px;
  font-size: 11px;
  font-weight: 500;
}
.pill.warn { background: rgba(251, 191, 36, 0.15); color: #fbbf24; }
.pill.dim { color: #6b7280; }

.empty-hint {
  text-align: center;
  padding: 16px;
  color: #8b949e;
  font-size: 13px;
}

/* ── 列存 (citus_columnar) ── */
.columnar-card {
  border-left: 3px solid rgba(99, 102, 241, 0.5);
}
.columnar-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 16px;
}
.columnar-stat {
  background: #0f1117;
  border: 1px solid #30363d;
  border-radius: 8px;
  padding: 12px 14px;
}
.columnar-stat .stat-label {
  font-size: 11px;
  color: #8b949e;
  margin-bottom: 6px;
  font-weight: 500;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.columnar-stat .stat-value {
  font-size: 22px;
  font-weight: 700;
  color: #e6edf3;
  font-variant-numeric: tabular-nums;
  line-height: 1.2;
}
.columnar-stat .stat-meta {
  font-size: 11px;
  color: #6b7280;
  margin-top: 4px;
}
.columnar-note {
  margin: 12px 0 0;
  padding: 8px 12px;
  background: rgba(99, 102, 241, 0.08);
  border-left: 2px solid #6366f1;
  border-radius: 4px;
  font-size: 12px;
  color: #818cf8;
}

@media (max-width: 800px) {
  .grid-2 { grid-template-columns: 1fr; }
  .local-logs-grid { grid-template-columns: 1fr; }
}

/* ── 表级维护操作（2026-07-08） ─────────────────────────── */
.header-actions {
  display: flex;
  gap: 8px;
}
.row-actions {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}
.btn-xs {
  padding: 3px 8px;
  font-size: 11px;
  border-radius: 4px;
  border: 1px solid transparent;
  cursor: pointer;
  white-space: nowrap;
  font-weight: 500;
  transition: all 0.15s ease;
}
.btn-xs:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.btn-vacuum {
  background: #064e3b;
  color: #6ee7b7;
  border-color: #047857;
}
.btn-vacuum:hover:not(:disabled) {
  background: #065f46;
  border-color: #10b981;
}
.btn-vacuum-full {
  background: #78350f;
  color: #fcd34d;
  border-color: #b45309;
}
.btn-vacuum-full:hover:not(:disabled) {
  background: #92400e;
  border-color: #d97706;
}
.btn-reindex {
  background: #1e3a8a;
  color: #93c5fd;
  border-color: #1d4ed8;
}
.btn-reindex:hover:not(:disabled) {
  background: #1e40af;
  border-color: #2563eb;
}
.row-status {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 4px;
  font-size: 11px;
  color: #fbbf24;
}
.row-result {
  margin-top: 4px;
  font-size: 11px;
  line-height: 1.4;
}
.row-result.ok { color: #6ee7b7; }
.row-result.err { color: #fca5a5; }
.spinner {
  display: inline-block;
  width: 10px;
  height: 10px;
  border: 2px solid #fbbf24;
  border-top-color: transparent;
  border-radius: 50%;
  animation: spin 0.6s linear infinite;
}
@keyframes spin {
  to { transform: rotate(360deg); }
}

/* ── 风险确认弹窗 ────────────────────────────────────── */
.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.7);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 9999;
  padding: 20px;
}
.modal {
  background: #1e293b;
  border-radius: 8px;
  padding: 24px;
  max-width: 540px;
  width: 100%;
  border: 1px solid #334155;
  box-shadow: 0 10px 40px rgba(0, 0, 0, 0.5);
}
.modal.modal-wide {
  max-width: 680px;
  max-height: 90vh;
  overflow-y: auto;
}
.modal-title {
  margin: 0 0 16px 0;
  color: #e2e8f0;
  font-size: 18px;
  font-weight: 600;
}
.modal-body p {
  color: #cbd5e1;
  margin: 0 0 12px 0;
  line-height: 1.6;
}
.modal-info {
  background: #0f172a;
  border: 1px solid #334155;
  border-radius: 4px;
  padding: 10px 12px;
  margin: 12px 0;
  color: #cbd5e1;
  font-size: 13px;
  line-height: 1.8;
}
.modal-info strong {
  color: #e6edf3;
  display: inline-block;
  min-width: 80px;
  margin-right: 8px;
}
.modal-section {
  background: #0f172a;
  border: 1px solid #334155;
  border-radius: 4px;
  padding: 10px 12px;
  margin: 10px 0;
  color: #cbd5e1;
  font-size: 13px;
  line-height: 1.6;
}
.modal-section-impact {
  background: #2a1505;
  border-color: #b45309;
  color: #fde68a;
}
.modal-section-title {
  font-size: 12px;
  font-weight: 600;
  color: #818cf8;
  margin-bottom: 6px;
  letter-spacing: 0.02em;
}
.modal-section-impact .modal-section-title {
  color: #fbbf24;
}
.modal-section-body {
  color: inherit;
  white-space: pre-wrap;
  word-break: break-word;
}
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 20px;
}
</style>

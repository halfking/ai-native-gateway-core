<template>
  <div class="storage-overview">
    <div class="grid-2">
      <!-- 数据库 -->
      <div class="card storage-card">
        <h3 class="card-title">
          PostgreSQL 数据库
          <span class="badge" v-if="data?.database.server_version">
            v{{ data.database.server_version.split(' ')[0] }}
          </span>
        </h3>
        <div class="big-stat">
          <div class="big-value">{{ data?.database.total_human || '—' }}</div>
          <div class="big-label">总占用（含 WAL / TOAST）</div>
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
          本机磁盘
          <span class="badge" :class="diskBadge(data?.filesystem.used_percent || 0)">
            {{ data?.filesystem.used_percent || 0 }}%
          </span>
        </h3>
        <div class="big-stat">
          <div class="big-value">{{ data?.filesystem.used_human || '—' }}</div>
          <div class="big-label">已用 / {{ data?.filesystem.total_human || '—' }}</div>
        </div>
        <div class="metric-row">
          <span>路径</span>
          <span class="metric-val mono">{{ data?.filesystem.path || '—' }}</span>
        </div>
        <div class="metric-row">
          <span>已用</span>
          <span class="metric-val">{{ data?.filesystem.used_human }}</span>
        </div>
        <div class="metric-row">
          <span>剩余</span>
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

    <!-- 本机日志目录 -->
    <div v-if="data?.local_logs" class="card local-logs-card">
      <h3 class="card-title">
        本机日志目录
        <span v-if="!data.local_logs.exists" class="badge warn">不存在</span>
      </h3>
      <div v-if="data.local_logs.exists" class="local-logs-grid">
        <div class="metric-row">
          <span>路径</span>
          <span class="metric-val mono">{{ data.local_logs.path }}</span>
        </div>
        <div class="metric-row">
          <span>文件数</span>
          <span class="metric-val">{{ data.local_logs.files.toLocaleString('zh-CN') }}</span>
        </div>
        <div class="metric-row">
          <span>大小</span>
          <span class="metric-val">{{ data.local_logs.size_human }}</span>
        </div>
        <div class="metric-row">
          <span>最旧</span>
          <span class="metric-val">{{ formatTime(data.local_logs.oldest_mtime) }}</span>
        </div>
        <div class="metric-row">
          <span>最新</span>
          <span class="metric-val">{{ formatTime(data.local_logs.newest_mtime) }}</span>
        </div>
      </div>
      <div v-else class="empty-hint">未找到日志目录，gzip 备份/轮转不会写入。</div>
    </div>

    <!-- 警告 -->
    <div v-if="data && data.warnings && data.warnings.length" class="warnings-card">
      <h3 class="card-title">⚠️ 提示</h3>
      <ul class="warnings-list">
        <li v-for="(w, i) in data.warnings" :key="i">{{ w }}</li>
      </ul>
    </div>

    <!-- 表级 Top-N -->
    <div class="card tables-card">
      <div class="card-header">
        <h3 class="card-title">数据库表 Top {{ tables.length }}</h3>
        <button class="btn btn-ghost btn-sm" @click="loadTables" :disabled="loadingTables">
          {{ loadingTables ? '加载中…' : '刷新' }}
        </button>
      </div>
      <div class="tables-table-wrap">
        <table class="data-table">
          <thead>
            <tr>
              <th>表名</th>
              <th>行数</th>
              <th>总大小</th>
              <th>索引</th>
              <th>TOAST</th>
              <th>占比</th>
              <th>分区</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="t in tables" :key="t.table">
              <td>
                <code class="tbl-code">{{ t.schema }}.{{ t.table }}</code>
              </td>
              <td>{{ formatNumber(t.rows) }}</td>
              <td class="strong">{{ t.total_human }}</td>
              <td class="dim">{{ humanBytes(t.index_bytes) }}</td>
              <td class="dim">{{ humanBytes(t.toast_bytes) }}</td>
              <td>
                <div class="pct-track">
                  <div class="pct-fill" :style="{ width: t.percent_of_db + '%' }"></div>
                  <span class="pct-text">{{ t.percent_of_db }}%</span>
                </div>
              </td>
              <td>
                <span v-if="t.is_partitioned" class="pill warn">分区</span>
                <span v-else class="pill dim">—</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { dataLifecycleStorage, dataLifecycleTableSizes, type StorageOverview, type TableSizeInfo } from '../../api'

const data = ref<StorageOverview | null>(null)
const tables = ref<TableSizeInfo[]>([])
const loadingTables = ref(false)

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
  return n.toLocaleString('zh-CN')
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

@media (max-width: 800px) {
  .grid-2 { grid-template-columns: 1fr; }
  .local-logs-grid { grid-template-columns: 1fr; }
}
</style>

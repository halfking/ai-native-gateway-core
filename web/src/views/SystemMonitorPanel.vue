<script setup lang="ts">
// SystemMonitorPanel.vue — 系统监测模块 UI（v2 Phase 2.4）
//
// 设计依据: docs/会话优化v2/32-系统监测模块设计.md §5.3
//
//   ┌─ StatsCardsRow ──── 队列 / running / concurrency / skipped_total_1h
//   ├─ ActionButtonRow ─ 开始全部 / 停止全部 / 按范围触发对话框
//   ├─ QueueTasksSwimLane ─ 按 task_type + automaticity 分泳道（SSE）
//   ├─ NetworkLatencySwimLane ─ http_ping 专用（latency_ms）
//   └─ RecentRunsTable ─ system_probe_runs 最近 50 条（轮询）
//
// 不依赖 Vue 组件库之外的复杂依赖；保持最小可用骨架以便 Phase 2 后期迭代。
import { ref, onMounted, onBeforeUnmount, watch, computed } from 'vue'
import {
  fetchSystemMonitorStats,
  fetchSystemMonitorRecentRuns,
  startAllSystemMonitorTasks,
  stopAllSystemMonitorTasks,
  startByCredential,
  startByProvider,
  startByModel,
  updateSystemMonitorConcurrency,
  submitSystemMonitorTask,
  openSystemMonitorStream,
  type SystemMonitorStats,
  type SystemMonitorRun,
  type SystemMonitorEvent,
} from '../api/api-system-monitor'

const stats = ref<SystemMonitorStats | null>(null)
const recentRuns = ref<SystemMonitorRun[]>([])
const sseTasks = ref<SystemMonitorEvent[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const triggerBusy = ref(false)
const concurrencyBusy = ref(false)
const showConcurrencyDialog = ref(false)
const editingConcurrency = ref(5)

// ── 范围触发对话框 ────────────────────────────────────────
const showScopeDialog = ref(false)
const scopeMode = ref<'credential' | 'provider' | 'model'>('provider')
const scopeId = ref<number | null>(null)
const scopeModelName = ref('')

let pollTimer: number | undefined
let sseCleanup: (() => void) | null = null

// ── 数据加载 ──────────────────────────────────────────────

async function loadStats() {
  try {
    stats.value = await fetchSystemMonitorStats()
  } catch (e) {
    error.value = (e as Error).message
  }
}

async function loadRecentRuns() {
  try {
    const r = await fetchSystemMonitorRecentRuns(50)
    recentRuns.value = r.runs
  } catch (e) {
    // 不覆盖现有 error.value（更严重的）
  }
}

async function loadAll() {
  loading.value = true
  await Promise.all([loadStats(), loadRecentRuns()])
  loading.value = false
}

// ── 操作 ────────────────────────────────────────────────

async function handleStartAll() {
  if (!confirm('确认开始全部探测任务？将扫描所有 active 凭据的 binding。')) return
  triggerBusy.value = true
  try {
    const r = await startAllSystemMonitorTasks()
    pushToast(`已触发 ${r.triggered} 任务, 失败 ${r.failed}`)
    await loadStats()
  } catch (e) {
    error.value = `start-all failed: ${(e as Error).message}`
  }
  triggerBusy.value = false
}

async function handleStopAll() {
  if (!confirm('确认停止全部探测任务？已 claimed 的任务仍会跑完。')) return
  triggerBusy.value = true
  try {
    const r = await stopAllSystemMonitorTasks()
    pushToast(`已清空队列 ${r.stopped} 条任务`)
    await loadStats()
  } catch (e) {
    error.value = `stop-all failed: ${(e as Error).message}`
  }
  triggerBusy.value = false
}

async function handleScopedTrigger() {
  if (scopeMode.value === 'credential') {
    if (!scopeId.value || scopeId.value <= 0) {
      error.value = '请输入凭据 ID'; return
    }
    triggerBusy.value = true
    try {
      const r = await startByCredential(scopeId.value)
      pushToast(`按凭据 ${scopeId.value} 触发 ${r.triggered}/${r.total} 任务`)
      showScopeDialog.value = false
    } catch (e) {
      error.value = `by-credential failed: ${(e as Error).message}`
    }
    triggerBusy.value = false
  } else if (scopeMode.value === 'provider') {
    if (!scopeId.value || scopeId.value <= 0) {
      error.value = '请输入供应商 ID'; return
    }
    triggerBusy.value = true
    try {
      const r = await startByProvider(scopeId.value)
      pushToast(`按供应商 ${scopeId.value} 触发 ${r.triggered}/${r.total} 任务 (direct + http_ping 1:1)`)
      showScopeDialog.value = false
    } catch (e) {
      error.value = `by-provider failed: ${(e as Error).message}`
    }
    triggerBusy.value = false
  } else {
    if (!scopeModelName.value.trim()) {
      error.value = '请输入模型名'; return
    }
    triggerBusy.value = true
    try {
      const r = await startByModel(scopeModelName.value.trim())
      pushToast(`按模型 ${scopeModelName.value} 触发 ${r.triggered}/${r.total} 任务`)
      showScopeDialog.value = false
    } catch (e) {
      error.value = `by-model failed: ${(e as Error).message}`
    }
    triggerBusy.value = false
  }
  await loadStats()
}

function openScopeDialog(mode: 'credential' | 'provider' | 'model') {
  scopeMode.value = mode
  scopeId.value = null
  scopeModelName.value = ''
  showScopeDialog.value = true
}

async function openConcurrencyDialog() {
  if (!stats.value) await loadStats()
  editingConcurrency.value = stats.value?.monitor_concurrency ?? 5
  showConcurrencyDialog.value = true
}

async function saveConcurrency() {
  if (editingConcurrency.value < 1 || editingConcurrency.value > 32) {
    error.value = 'concurrency 必须在 1-32 之间'
    return
  }
  concurrencyBusy.value = true
  try {
    await updateSystemMonitorConcurrency(editingConcurrency.value)
    pushToast(`concurrency 已更新为 ${editingConcurrency.value}`)
    showConcurrencyDialog.value = false
    await loadStats()
  } catch (e) {
    error.value = `update concurrency failed: ${(e as Error).message}`
  }
  concurrencyBusy.value = false
}

// ── 手动入队（按钮触发单任务测试用） ─────────────────────────
async function submitOne() {
  const credID = Number(prompt('credential_id?', ''))
  if (!credID || credID <= 0) return
  const model = prompt('raw_model?', '')
  if (!model) return
  triggerBusy.value = true
  try {
    const r = await submitSystemMonitorTask({
      credential_id: credID,
      raw_model: model,
      task_type: 'direct_ping',
      automaticity: 'mandatory',
    })
    pushToast(`已入队 task_id=${r.task_id}`)
  } catch (e) {
    error.value = `submit failed: ${(e as Error).message}`
  }
  triggerBusy.value = false
}

// ── SSE 流 ──────────────────────────────────────────────

function connectSSE() {
  sseCleanup?.()
  sseCleanup = openSystemMonitorStream(
    (env) => {
      // 仅保留最近 200 条 SSE 事件（避免内存膨胀）
      sseTasks.value.unshift(env)
      if (sseTasks.value.length > 200) sseTasks.value = sseTasks.value.slice(0, 200)
    },
    (e) => {
      // 不持续弹错，避免日志洪水
      console.warn('system_monitor SSE error', e)
    }
  )
}

// ── 任务分组（按 task_type + automaticity） ─────────────────────
const tasksByType = computed(() => {
  const map = new Map<string, { mandatory: number; automatic: number }>()
  for (const ev of sseTasks.value) {
    const tt = ev.task?.task_type ?? 'unknown'
    const cur = map.get(tt) ?? { mandatory: 0, automatic: 0 }
    if (ev.task?.automaticity === 'mandatory') cur.mandatory++
    else cur.automatic++
    map.set(tt, cur)
  }
  return Array.from(map.entries()).sort((a, b) => a[0].localeCompare(b[0]))
})

const httpPingLatencyStats = computed(() => {
  const entries = sseTasks.value
    .filter(e => e.task?.task_type === 'http_ping' && e.task.latency_ms != null)
    .slice(0, 30)
  return entries
})

// ── Toast ───────────────────────────────────────────────

const toasts = ref<{ msg: string; ts: number }[]>([])
let toastSeq = 0
function pushToast(msg: string) {
  const id = ++toastSeq
  toasts.value.push({ msg, ts: id })
  setTimeout(() => {
    toasts.value = toasts.value.filter(t => t.ts !== id)
  }, 3500)
}

function formatTime(s: string | null | undefined) {
  if (!s) return '—'
  const d = new Date(s)
  if (isNaN(d.getTime())) return s
  return d.toLocaleString()
}

// ── 生命周期 ─────────────────────────────────────────────

onMounted(() => {
  loadAll()
  connectSSE()
  pollTimer = window.setInterval(() => { void loadStats() }, 5000)
})

onBeforeUnmount(() => {
  if (pollTimer !== undefined) clearInterval(pollTimer)
  sseCleanup?.()
})

watch(() => error.value, (v) => {
  if (v) setTimeout(() => { error.value = null }, 6000)
})
</script>

<template>
  <div class="system-monitor-panel">
    <header class="sm-header">
      <h2>系统监测（Phase 2.4 v1）</h2>
      <p class="sm-subtitle">
        探测任务统一编排，自动性策略（mandatory / automatic）+ 5分钟请求成功跳过规则
        ｜
        <a href="/docs/会话优化v2/32-系统监测模块设计.md" target="_blank">设计文档 §32</a>
      </p>
    </header>

    <!-- 顶部错误条 -->
    <div v-if="error" class="sm-error">{{ error }}</div>

    <!-- 统计卡片 -->
    <section class="sm-stats">
      <div class="sm-card">
        <div class="sm-card-label">队列</div>
        <div class="sm-card-value" :class="{ alert: (stats?.queue_size ?? 0) > 50 }">
          {{ stats?.queue_size ?? '—' }}
        </div>
      </div>
      <div class="sm-card">
        <div class="sm-card-label">运行中</div>
        <div class="sm-card-value">
          {{ stats?.running_size ?? '—' }}
        </div>
      </div>
      <div class="sm-card">
        <div class="sm-card-label">并发上限</div>
        <div class="sm-card-value">{{ stats?.monitor_concurrency ?? '—' }}</div>
      </div>
      <div class="sm-card">
        <div class="sm-card-label">状态</div>
        <div class="sm-card-value" :class="{ alert: stats?.in_fallback }">
          {{ stats?.in_fallback ? '降级（Redis 不可达）' : '正常' }}
        </div>
      </div>
    </section>

    <!-- 操作按钮 -->
    <section class="sm-actions">
      <el-button type="primary" :disabled="triggerBusy" @click="handleStartAll">
        开始全部任务
      </el-button>
      <el-button type="warning" :disabled="triggerBusy" @click="handleStopAll">
        停止全部任务
      </el-button>
      <el-button @click="openScopeDialog('credential')">按凭据触发</el-button>
      <el-button type="success" @click="openScopeDialog('provider')">
        按供应商触发（direct + http_ping）
      </el-button>
      <el-button @click="openScopeDialog('model')">按模型触发</el-button>
      <el-button @click="openConcurrencyDialog">调整并发</el-button>
      <el-button size="small" @click="submitOne">手动入队单任务</el-button>
    </section>

    <!-- 并发调整对话框 -->
    <el-dialog v-model="showConcurrencyDialog" title="调整 monitor_concurrency" width="400px">
      <div>
        <p>范围：1-32（self_check_settings.monitor_concurrency CHECK）</p>
        <el-input-number v-model="editingConcurrency" :min="1" :max="32" :step="1" />
      </div>
      <template #footer>
        <el-button @click="showConcurrencyDialog = false">取消</el-button>
        <el-button type="primary" :disabled="concurrencyBusy" @click="saveConcurrency">
          保存
        </el-button>
      </template>
    </el-dialog>

    <!-- 范围触发对话框 -->
    <el-dialog v-model="showScopeDialog" title="范围触发" width="500px">
      <div>
        <el-radio-group v-model="scopeMode">
          <el-radio-button label="provider">供应商</el-radio-button>
          <el-radio-button label="credential">凭据</el-radio-button>
          <el-radio-button label="model">模型</el-radio-button>
        </el-radio-group>
        <div v-if="scopeMode === 'provider' || scopeMode === 'credential'" style="margin-top: 16px;">
          <el-input-number
            v-if="scopeMode === 'provider'"
            v-model="scopeId"
            :min="1"
            placeholder="provider_id"
          />
          <el-input-number
            v-else
            v-model="scopeId"
            :min="1"
            placeholder="credential_id"
          />
        </div>
        <div v-else style="margin-top: 16px;">
          <el-input v-model="scopeModelName" placeholder="raw_model (如 glm-5.2)" />
        </div>
      </div>
      <template #footer>
        <el-button @click="showScopeDialog = false">取消</el-button>
        <el-button type="primary" :disabled="triggerBusy" @click="handleScopedTrigger">
          触发
        </el-button>
      </template>
    </el-dialog>

    <!-- 队列任务分组泳道 -->
    <section class="sm-section">
      <h3>队列任务分组（SSE 实时，{{ sseTasks.length }} 条事件）</h3>
      <div v-if="tasksByType.length === 0" class="sm-empty">
        暂无事件。点击"开始全部任务"或等待自动跳过规则触发。
      </div>
      <div v-else class="sm-swimlane">
        <div v-for="[tt, counts] in tasksByType" :key="tt" class="sm-swimlane-row">
          <div class="sm-swimlane-label">{{ tt }}</div>
          <div class="sm-swimlane-bars">
            <div class="sm-bar mandatory" :style="{ width: `${counts.mandatory * 8}px` }">
              mandatory {{ counts.mandatory }}
            </div>
            <div class="sm-bar automatic" :style="{ width: `${counts.automatic * 8}px` }">
              automatic {{ counts.automatic }}
            </div>
          </div>
        </div>
      </div>
    </section>

    <!-- http_ping 网络延时泳道 -->
    <section class="sm-section">
      <h3>网络延时泳道（http_ping，最近 30 条）</h3>
      <div v-if="httpPingLatencyStats.length === 0" class="sm-empty">暂无 http_ping 事件</div>
      <table v-else class="sm-table">
        <thead>
          <tr>
            <th>时间</th>
            <th>credential_id</th>
            <th>model</th>
            <th>latency_ms</th>
            <th>status</th>
            <th>error</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="(e, idx) in httpPingLatencyStats" :key="idx" :class="{ 'slow-latency': (e.task?.latency_ms ?? 0) > 1000 }">
            <td>{{ formatTime(e.ts) }}</td>
            <td>{{ e.task?.credential_id ?? '—' }}</td>
            <td>{{ e.task?.raw_model ?? '—' }}</td>
            <td>{{ e.task?.latency_ms ?? '—' }}</td>
            <td>{{ e.task?.http_status ?? '—' }}</td>
            <td>{{ e.task?.err_code ?? '' }}</td>
          </tr>
        </tbody>
      </table>
    </section>

    <!-- system_probe_runs 最近 50 条 -->
    <section class="sm-section">
      <h3>最近探测执行（system_probe_runs，最近 50 条）</h3>
      <el-button size="small" @click="loadRecentRuns">刷新</el-button>
      <table v-if="recentRuns.length > 0" class="sm-table">
        <thead>
          <tr>
            <th>时间</th>
            <th>task_type</th>
            <th>auto</th>
            <th>credential_id</th>
            <th>model</th>
            <th>status</th>
            <th>latency_ms</th>
            <th>http_status</th>
            <th>skip_reason</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in recentRuns" :key="r.id" :class="{ 'row-skipped': r.status === 'skipped', 'row-failed': r.status === 'failed' }">
            <td>{{ formatTime(r.started_at) }}</td>
            <td>{{ r.task_type }}</td>
            <td>{{ r.automaticity === 'automatic' ? 'auto' : '强制' }}</td>
            <td>{{ r.credential_id }}</td>
            <td>{{ r.raw_model }}</td>
            <td>{{ r.status }}</td>
            <td>{{ r.latency_ms ?? '—' }}</td>
            <td>{{ r.http_status ?? '—' }}</td>
            <td>{{ r.skip_reason || '—' }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else class="sm-empty">暂无 system_probe_runs 行</div>
    </section>

    <!-- Toast 列表 -->
    <div class="sm-toasts">
      <div v-for="t in toasts" :key="t.ts" class="sm-toast">{{ t.msg }}</div>
    </div>
  </div>
</template>

<style scoped>
.system-monitor-panel {
  padding: 16px 24px;
  max-width: 1280px;
  margin: 0 auto;
}
.sm-header h2 {
  margin: 0 0 4px 0;
  font-size: 20px;
}
.sm-subtitle {
  margin: 0 0 16px 0;
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
.sm-error {
  background: var(--el-color-danger-light-9);
  color: var(--el-color-danger);
  padding: 8px 12px;
  border-radius: 4px;
  margin-bottom: 12px;
}
.sm-stats {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
  margin-bottom: 16px;
}
.sm-card {
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
  padding: 12px;
  background: var(--el-fill-color-blank);
}
.sm-card-label {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.sm-card-value {
  font-size: 24px;
  font-weight: 600;
  margin-top: 4px;
}
.sm-card-value.alert {
  color: var(--el-color-danger);
}
.sm-actions {
  margin-bottom: 16px;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
.sm-section {
  margin-bottom: 20px;
  padding: 16px;
  border: 1px solid var(--el-border-color);
  border-radius: 6px;
  background: var(--el-fill-color-blank);
}
.sm-section h3 {
  margin: 0 0 12px 0;
  font-size: 15px;
}
.sm-empty {
  color: var(--el-text-color-secondary);
  font-size: 13px;
  padding: 12px 0;
}
.sm-swimlane-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 8px;
}
.sm-swimlane-label {
  width: 140px;
  font-family: monospace;
  font-size: 12px;
}
.sm-swimlane-bars {
  display: flex;
  gap: 4px;
  flex: 1;
}
.sm-bar {
  height: 22px;
  padding: 2px 8px;
  color: white;
  font-size: 11px;
  border-radius: 4px;
  display: flex;
  align-items: center;
  white-space: nowrap;
}
.sm-bar.mandatory { background: var(--el-color-primary); }
.sm-bar.automatic { background: var(--el-color-warning); }

.sm-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
  margin-top: 8px;
}
.sm-table th, .sm-table td {
  padding: 6px 8px;
  border-bottom: 1px solid var(--el-border-color-lighter);
  text-align: left;
}
.sm-table th {
  background: var(--el-fill-color-light);
  font-weight: 600;
}
.sm-table tr.slow-latency {
  background: var(--el-color-danger-light-9);
}
.sm-table tr.row-skipped {
  background: var(--el-color-warning-light-9);
}
.sm-table tr.row-failed {
  background: var(--el-color-danger-light-9);
}
.sm-toasts {
  position: fixed;
  bottom: 24px;
  right: 24px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  z-index: 100;
}
.sm-toast {
  background: var(--el-color-primary);
  color: white;
  padding: 8px 12px;
  border-radius: 4px;
  font-size: 13px;
  box-shadow: 0 4px 12px rgba(0,0,0,0.15);
}
</style>
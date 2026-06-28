<script setup lang="ts">
import { ref, onMounted, computed, onUnmounted } from 'vue'
import ModelPicker from '../components/ModelPicker.vue'

// ── Types ────────────────────────────────────────────────────────────────

interface ModelHealthSummary {
  provider_model_id: number
  raw_model_name: string
  outbound_model_name: string
  protocol: string
  provider_name: string
  
  total_credentials: number
  healthy_count: number
  suspicious_count: number
  failing_count: number
  probing_count: number
  healthy_percentage: number
  failing_percentage: number
  
  urgent_count: number
  suspicious_priority_count: number
  failing_priority_count: number
  watchdog_count: number
  
  avg_success_rate_7d: number
  avg_verification_hours: number
  avg_consecutive_successes: number
  
  total_real_success_24h: number
  total_real_failure_24h: number
  real_success_rate_24h?: number
  
  last_verified_at?: string
  last_real_request_at?: string
  next_probe_at?: string
  
  critical_nodes: number
  pending_probes_5min: number
  overall_health: 'critical' | 'warning' | 'degraded' | 'healthy' | 'unknown'
}

interface ProbeQueueSnapshot {
  probe_priority: string
  state: string
  queue_size: number
  ready_now: number
  ready_1min: number
  ready_5min: number
  earliest_retry_at?: string
  latest_retry_at?: string
  avg_wait_seconds?: number
  max_wait_seconds?: number
}

interface ProbeSystemHealth {
  total_nodes: number
  healthy_nodes: number
  failing_nodes: number
  suspicious_nodes: number
  probing_nodes: number
  
  urgent_queue_size: number
  suspicious_queue_size: number
  failing_queue_size: number
  watchdog_queue_size: number
  
  ready_probes: number
  current_probing: number
  credentials_being_probed: number
  
  avg_success_rate_7d?: number
  last_probe_at?: string
  last_real_request_at?: string
  
  total_real_success_24h: number
  total_real_failure_24h: number
  
  critical_nodes: number
  pending_probes_5min: number
  
  snapshot_at: string
}

interface ModelNodeDetail {
  raw_model_name: string
  outbound_model_name: string
  probe_priority: string
  state: string
  
  credential_id: number
  credential_label: string
  provider_name: string
  
  last_verified_at?: string
  next_retry_at?: string
  marked_suspicious_at?: string
  probing_started_at?: string
  
  consecutive_successes: number
  consecutive_failures: number
  consecutive_watchdog_successes: number
  success_rate_7d?: number
  verification_interval: string
  
  real_success_24h: number
  real_failure_24h: number
  last_real_request_at?: string
  
  last_unavailable_reason?: string
  last_err_code?: string
  
  retry_in: string
  state_duration_minutes: number
}

// ── State ────────────────────────────────────────────────────────────────

const loading = ref(false)
const systemHealth = ref<ProbeSystemHealth | null>(null)
const models = ref<ModelHealthSummary[]>([])
const queues = ref<ProbeQueueSnapshot[]>([])
const selectedModel = ref<ModelHealthSummary | null>(null)
const modelNodes = ref<ModelNodeDetail[]>([])
const nodesLoading = ref(false)

const modelFilter = ref('')
const healthFilter = ref<string>('')
const autoRefresh = ref(true)
let refreshTimer: number | null = null

// ── API ──────────────────────────────────────────────────────────────────

async function fetchSystemHealth() {
  try {
    const resp = await fetch('/api/admin/probe/system-health', {
      credentials: 'include'
    })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    systemHealth.value = await resp.json()
  } catch (err) {
    console.error('Failed to fetch system health:', err)
  }
}

async function fetchModels() {
  loading.value = true
  try {
    const url = modelFilter.value 
      ? `/api/admin/probe/dashboard?model=${encodeURIComponent(modelFilter.value)}`
      : '/api/admin/probe/dashboard'
    
    const resp = await fetch(url, { credentials: 'include' })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    
    const data = await resp.json()
    models.value = data.models || []
  } catch (err) {
    console.error('Failed to fetch models:', err)
    models.value = []
  } finally {
    loading.value = false
  }
}

async function fetchQueues() {
  try {
    const resp = await fetch('/api/admin/probe/queue-snapshot', {
      credentials: 'include'
    })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    
    const data = await resp.json()
    queues.value = data.queues || []
  } catch (err) {
    console.error('Failed to fetch queues:', err)
    queues.value = []
  }
}

async function fetchModelNodes(modelName: string) {
  nodesLoading.value = true
  try {
    const resp = await fetch(`/api/admin/probe/model/${encodeURIComponent(modelName)}/nodes`, {
      credentials: 'include'
    })
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`)
    
    const data = await resp.json()
    modelNodes.value = data.nodes || []
  } catch (err) {
    console.error('Failed to fetch model nodes:', err)
    modelNodes.value = []
  } finally {
    nodesLoading.value = false
  }
}

function selectModel(model: ModelHealthSummary) {
  selectedModel.value = model
  fetchModelNodes(model.raw_model_name)
}

function closeDetail() {
  selectedModel.value = null
  modelNodes.value = []
}

async function refreshAll() {
  await Promise.all([
    fetchSystemHealth(),
    fetchModels(),
    fetchQueues()
  ])
}

// ── Computed ─────────────────────────────────────────────────────────────

const filteredModels = computed(() => {
  let result = models.value
  
  if (healthFilter.value) {
    result = result.filter(m => m.overall_health === healthFilter.value)
  }
  
  return result
})

const priorityQueueTotals = computed(() => {
  const totals = {
    urgent: 0,
    suspicious: 0,
    failing: 0,
    watchdog: 0
  }
  
  queues.value.forEach(q => {
    const priority = q.probe_priority as keyof typeof totals
    if (priority in totals) {
      totals[priority] += q.queue_size
    }
  })
  
  return totals
})

function getHealthBadge(health: string): string {
  switch (health) {
    case 'critical': return 'badge-red'
    case 'warning': return 'badge-yellow'
    case 'degraded': return 'badge-yellow'
    case 'healthy': return 'badge-green'
    default: return 'badge-gray'
  }
}

function getPriorityBadge(priority: string): string {
  switch (priority) {
    case 'urgent': return 'badge-red'
    case 'suspicious': return 'badge-yellow'
    case 'failing': return 'badge-yellow'
    case 'recovering': return 'badge-blue'
    case 'watchdog': return 'badge-green'
    default: return 'badge-gray'
  }
}

function getStateColor(state: string): string {
  switch (state) {
    case 'healthy':
    case 'healthy_confirmed':
    case 'available':
      return 'var(--success)'
    case 'recovering':
      return 'var(--accent-h)'
    case 'suspicious':
    case 'unavailable':
      return 'var(--warning)'
    case 'failing':
    case 'broken_confirmed':
      return 'var(--danger)'
    case 'probing':
    case 'unknown':
      return 'var(--accent-h)'
    default:
      return 'var(--muted)'
  }
}

function formatTime(ts?: string): string {
  if (!ts) return '—'
  const d = new Date(ts)
  return d.toLocaleString('zh-CN', { 
    month: '2-digit', 
    day: '2-digit', 
    hour: '2-digit', 
    minute: '2-digit' 
  })
}

function formatDuration(minutes: number): string {
  if (minutes < 60) return `${Math.round(minutes)}分钟`
  const hours = Math.floor(minutes / 60)
  const mins = Math.round(minutes % 60)
  return mins > 0 ? `${hours}小时${mins}分钟` : `${hours}小时`
}

// ── Lifecycle ────────────────────────────────────────────────────────────

onMounted(() => {
  refreshAll()
  
  if (autoRefresh.value) {
    refreshTimer = window.setInterval(refreshAll, 30000)
  }
})

onUnmounted(() => {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
})

</script>

<template>
  <div class="page-container">
    <!-- Header -->
    <div class="top-bar">
      <router-link to="/routing-v2" class="back-link">← 路由全景</router-link>
      <h1>探测健康度</h1>
      <div class="spacer"></div>
      <label class="auto-refresh-label">
        <input type="checkbox" v-model="autoRefresh" />
        自动刷新 (30s)
      </label>
      <button @click="refreshAll" class="btn btn-sm btn-ghost">刷新</button>
    </div>

    <!-- System Health Card -->
    <div v-if="systemHealth" class="card stats-grid">
      <div class="stat-item">
        <div class="stat-label">总节点</div>
        <div class="stat-value">{{ systemHealth.total_nodes }}</div>
      </div>
      <div class="stat-item stat-success">
        <div class="stat-label">健康</div>
        <div class="stat-value">{{ systemHealth.healthy_nodes }}</div>
      </div>
      <div class="stat-item stat-danger">
        <div class="stat-label">失败</div>
        <div class="stat-value">{{ systemHealth.failing_nodes }}</div>
      </div>
      <div class="stat-item stat-warning">
        <div class="stat-label">可疑</div>
        <div class="stat-value">{{ systemHealth.suspicious_nodes }}</div>
      </div>
      <div class="stat-item stat-accent">
        <div class="stat-label">探测中</div>
        <div class="stat-value">{{ systemHealth.probing_nodes }}</div>
      </div>
      <div class="stat-item stat-danger" v-if="systemHealth.critical_nodes > 0">
        <div class="stat-label">危急</div>
        <div class="stat-value">{{ systemHealth.critical_nodes }}</div>
      </div>
    </div>

    <!-- Priority Queues -->
    <div class="card queue-grid">
      <div class="queue-item queue-urgent">
        <div class="queue-label">P0 Urgent</div>
        <div class="queue-value">{{ priorityQueueTotals.urgent }}</div>
      </div>
      <div class="queue-item queue-suspicious">
        <div class="queue-label">P1 Suspicious</div>
        <div class="queue-value">{{ priorityQueueTotals.suspicious }}</div>
      </div>
      <div class="queue-item queue-failing">
        <div class="queue-label">P2 Failing</div>
        <div class="queue-value">{{ priorityQueueTotals.failing }}</div>
      </div>
      <div class="queue-item queue-watchdog">
        <div class="queue-label">P3 Watchdog</div>
        <div class="queue-value">{{ priorityQueueTotals.watchdog }}</div>
      </div>
    </div>

    <!-- Filters -->
    <div class="filter-bar">
      <ModelPicker
        v-model="modelFilter"
        mode="single"
        placeholder="选择模型..."
        class="model-picker-wrapper"
      />
      
      <select v-model="healthFilter" class="filter-input">
        <option value="">全部健康度</option>
        <option value="healthy">健康</option>
        <option value="degraded">降级</option>
        <option value="warning">警告</option>
        <option value="critical">危急</option>
      </select>

      <button @click="fetchModels" class="btn btn-sm btn-secondary">应用筛选</button>
    </div>

    <!-- Models Table -->
    <div class="card">
      <table class="data-table">
        <thead>
          <tr>
            <th>模型</th>
            <th>Provider</th>
            <th class="text-center">总数</th>
            <th class="text-center">健康</th>
            <th class="text-center">可疑</th>
            <th class="text-center">失败</th>
            <th class="text-center">优先级</th>
            <th class="text-center">成功率(7d)</th>
            <th class="text-center">健康度</th>
            <th class="text-center">操作</th>
          </tr>
        </thead>
        <tbody>
          <tr 
            v-for="model in filteredModels" 
            :key="model.provider_model_id"
          >
            <td>
              <div class="model-name">{{ model.raw_model_name }}</div>
              <div class="model-sub">{{ model.outbound_model_name }}</div>
            </td>
            <td>{{ model.provider_name }}</td>
            <td class="text-center">{{ model.total_credentials }}</td>
            <td class="text-center">
              <span class="rate-good">{{ model.healthy_count }}</span>
              <span class="muted-text"> ({{ model.healthy_percentage.toFixed(0) }}%)</span>
            </td>
            <td class="text-center">
              <span class="rate-warn">{{ model.suspicious_count }}</span>
            </td>
            <td class="text-center">
              <span class="rate-bad">{{ model.failing_count }}</span>
              <span class="muted-text"> ({{ model.failing_percentage.toFixed(0) }}%)</span>
            </td>
            <td>
              <div class="priority-list">
                <span v-if="model.urgent_count > 0" class="badge badge-red">U:{{ model.urgent_count }}</span>
                <span v-if="model.suspicious_priority_count > 0" class="badge badge-yellow">S:{{ model.suspicious_priority_count }}</span>
                <span v-if="model.failing_priority_count > 0" class="badge badge-yellow">F:{{ model.failing_priority_count }}</span>
              </div>
            </td>
            <td class="text-center">
              <span class="rate-value">{{ model.avg_success_rate_7d.toFixed(1) }}%</span>
            </td>
            <td class="text-center">
              <span :class="['badge', getHealthBadge(model.overall_health)]">
                {{ model.overall_health }}
              </span>
            </td>
            <td class="text-center">
              <button 
                @click="selectModel(model)"
                class="btn btn-sm btn-ghost"
              >
                详情
              </button>
            </td>
          </tr>
        </tbody>
      </table>
      
      <div v-if="loading" class="empty-state">加载中...</div>
      <div v-else-if="filteredModels.length === 0" class="empty-state">暂无数据</div>
    </div>

    <!-- Model Detail Modal -->
    <div 
      v-if="selectedModel" 
      class="modal-overlay"
      @click.self="closeDetail"
    >
      <div class="modal-content">
        <div class="modal-header">
          <div>
            <h2>{{ selectedModel.raw_model_name }}</h2>
            <p class="muted-text">{{ selectedModel.provider_name }}</p>
          </div>
          <button 
            @click="closeDetail"
            class="btn-close"
          >
            ×
          </button>
        </div>

        <div class="modal-body">
          <div v-if="nodesLoading" class="empty-state">加载节点详情...</div>
          
          <div v-else class="nodes-list">
            <div 
              v-for="node in modelNodes" 
              :key="node.credential_id"
              class="node-card"
            >
              <div class="node-header">
                <div>
                  <div class="node-label">{{ node.credential_label }}</div>
                  <div class="muted-text small">Credential #{{ node.credential_id }}</div>
                </div>
                <div class="node-badges">
                  <span :class="['badge', getPriorityBadge(node.probe_priority)]">
                    {{ node.probe_priority }}
                  </span>
                  <span class="badge" :style="{color: getStateColor(node.state), background: 'rgba(255,255,255,0.1)'}">
                    {{ node.state }}
                  </span>
                </div>
              </div>

              <div class="node-stats">
                <div class="node-stat">
                  <div class="muted-text small">连续成功</div>
                  <div>{{ node.consecutive_successes }}</div>
                </div>
                <div class="node-stat">
                  <div class="muted-text small">连续失败</div>
                  <div class="rate-bad">{{ node.consecutive_failures }}</div>
                </div>
                <div class="node-stat">
                  <div class="muted-text small">成功率(7d)</div>
                  <div>{{ node.success_rate_7d?.toFixed(1) || '—' }}%</div>
                </div>
                <div class="node-stat">
                  <div class="muted-text small">验证间隔</div>
                  <div>{{ node.verification_interval }}</div>
                </div>
                <div class="node-stat">
                  <div class="muted-text small">实际成功(24h)</div>
                  <div class="rate-good">{{ node.real_success_24h }}</div>
                </div>
                <div class="node-stat">
                  <div class="muted-text small">实际失败(24h)</div>
                  <div class="rate-bad">{{ node.real_failure_24h }}</div>
                </div>
                <div class="node-stat">
                  <div class="muted-text small">下次探测</div>
                  <div>{{ node.retry_in }}</div>
                </div>
                <div class="node-stat">
                  <div class="muted-text small">状态持续</div>
                  <div>{{ formatDuration(node.state_duration_minutes) }}</div>
                </div>
              </div>

              <div v-if="node.last_unavailable_reason" class="node-error">
                <div class="muted-text small">最后错误</div>
                <div class="error-text">{{ node.last_unavailable_reason }}</div>
              </div>
            </div>

            <div v-if="modelNodes.length === 0" class="empty-state">暂无节点数据</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page-container {
  padding: 20px;
}

.top-bar {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 16px;
}

.back-link {
  color: var(--muted);
  font-size: 13px;
}
.back-link:hover {
  color: var(--text);
}

h1 {
  font-size: 20px;
  font-weight: 600;
}

.spacer {
  flex: 1;
}

.auto-refresh-label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--muted);
}

/* Stats Grid */
.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
  gap: 16px;
  margin-bottom: 16px;
}

.stat-item {
  text-align: center;
}

.stat-label {
  font-size: 11px;
  color: var(--muted);
  text-transform: uppercase;
  margin-bottom: 4px;
}

.stat-value {
  font-size: 24px;
  font-weight: 700;
}

.stat-success .stat-value { color: var(--success); }
.stat-danger .stat-value { color: var(--danger); }
.stat-warning .stat-value { color: var(--warning); }
.stat-accent .stat-value { color: var(--accent-h); }

/* Queue Grid */
.queue-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(150px, 1fr));
  gap: 16px;
  margin-bottom: 16px;
}

.queue-item {
  padding: 12px;
  border-radius: var(--radius);
  text-align: center;
}

.queue-urgent { background: rgba(248,81,73,.1); }
.queue-suspicious { background: rgba(210,153,34,.1); }
.queue-failing { background: rgba(210,153,34,.1); }
.queue-watchdog { background: rgba(63,185,80,.1); }

.queue-label {
  font-size: 11px;
  font-weight: 600;
  text-transform: uppercase;
  margin-bottom: 4px;
}

.queue-urgent .queue-label { color: var(--danger); }
.queue-suspicious .queue-label { color: var(--warning); }
.queue-failing .queue-label { color: var(--warning); }
.queue-watchdog .queue-label { color: var(--success); }

.queue-value {
  font-size: 20px;
  font-weight: 700;
}

.queue-urgent .queue-value { color: var(--danger); }
.queue-suspicious .queue-value { color: var(--warning); }
.queue-failing .queue-value { color: var(--warning); }
.queue-watchdog .queue-value { color: var(--success); }

/* Filter Bar */
.filter-bar {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 16px;
}

.model-picker-wrapper {
  flex: 1;
  max-width: 400px;
}

.filter-input {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text);
  padding: 6px 10px;
  font-size: 13px;
  outline: none;
}

.filter-input:focus {
  border-color: var(--accent);
}

.filter-input:nth-child(2) {
  width: 140px;
}

/* Table */
.data-table {
  font-size: 13px;
}

.data-table thead th {
  padding: 8px 12px;
  font-size: 11px;
  text-transform: uppercase;
  font-weight: 600;
}

.data-table tbody td {
  padding: 10px 12px;
  border-bottom: 1px solid var(--border);
}

.data-table tbody tr:hover {
  background: rgba(255, 255, 255, 0.02);
}

.text-center {
  text-align: center;
}

.model-name {
  font-weight: 600;
}

.model-sub {
  font-size: 11px;
  color: var(--muted);
}

.muted-text {
  color: var(--muted);
}

.small {
  font-size: 11px;
}

.rate-good { color: var(--success); font-weight: 600; }
.rate-warn { color: var(--warning); font-weight: 600; }
.rate-bad { color: var(--danger); font-weight: 600; }
.rate-value { font-weight: 600; }

.priority-list {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}

.empty-state {
  padding: 40px;
  text-align: center;
  color: var(--muted);
}

/* Modal */
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

.modal-content {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  max-width: 1200px;
  width: 100%;
  max-height: 90vh;
  display: flex;
  flex-direction: column;
}

.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 20px;
  border-bottom: 1px solid var(--border);
}

.modal-header h2 {
  font-size: 18px;
  font-weight: 600;
}

.btn-close {
  background: none;
  border: none;
  font-size: 32px;
  color: var(--muted);
  cursor: pointer;
  padding: 0;
  width: 32px;
  height: 32px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 4px;
}
.btn-close:hover {
  background: rgba(255, 255, 255, 0.05);
  color: var(--text);
}

.modal-body {
  flex: 1;
  overflow-y: auto;
  padding: 20px;
}

.nodes-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.node-card {
  background: var(--bg-subtle);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 16px;
}

.node-card:hover {
  background: rgba(255, 255, 255, 0.02);
}

.node-header {
  display: flex;
  justify-content: space-between;
  align-items: start;
  margin-bottom: 12px;
}

.node-label {
  font-weight: 600;
}

.node-badges {
  display: flex;
  gap: 6px;
}

.node-stats {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
  gap: 12px;
}

.node-stat {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.node-error {
  margin-top: 12px;
  padding-top: 12px;
  border-top: 1px solid var(--border);
}

.error-text {
  font-size: 12px;
  color: var(--danger);
  font-family: monospace;
  margin-top: 4px;
}
</style>

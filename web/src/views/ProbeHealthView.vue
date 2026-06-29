<script setup lang="ts">
import { ref, onMounted, computed, onUnmounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import ModelPicker from '../components/ModelPicker.vue'
import { req } from '../api/_core'

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

// ── State ────────────────────────────────────────────────────────────────

const loading = ref(false)
const systemHealth = ref<ProbeSystemHealth | null>(null)
const models = ref<ModelHealthSummary[]>([])
const queues = ref<ProbeQueueSnapshot[]>([])
const router = useRouter()

const modelFilter = ref('')
const healthFilter = ref<string>('')
const autoRefresh = ref(true)
let refreshTimer: number | null = null
let filterDebounceTimer: number | null = null

watch(modelFilter, () => {
  if (filterDebounceTimer) clearTimeout(filterDebounceTimer)
  filterDebounceTimer = window.setTimeout(() => {
    fetchModels()
  }, 400)
})

// ── API ──────────────────────────────────────────────────────────────────

async function fetchSystemHealth() {
  try {
    systemHealth.value = await req<ProbeSystemHealth>('GET', '/api/admin/probe/system-health')
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
    
    const data = await req<{ models: ModelHealthSummary[]; total: number }>('GET', url)
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
    const data = await req<{ queues: ProbeQueueSnapshot[]; total: number }>('GET', '/api/admin/probe/queue-snapshot')
    queues.value = data.queues || []
  } catch (err) {
    console.error('Failed to fetch queues:', err)
    queues.value = []
  }
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
  if (filterDebounceTimer) {
    clearTimeout(filterDebounceTimer)
    filterDebounceTimer = null
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
          </tr>
        </thead>
        <tbody>
          <tr 
            v-for="model in filteredModels" 
            :key="model.provider_model_id"
            style="cursor:pointer"
            @click="router.push({ path: '/probe-health/detail', query: { model: model.raw_model_name } })"
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
          </tr>
        </tbody>
      </table>
      
      <div v-if="loading" class="empty-state">加载中...</div>
      <div v-else-if="filteredModels.length === 0" class="empty-state">暂无数据</div>
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

</style>

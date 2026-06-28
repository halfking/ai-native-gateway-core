<script setup lang="ts">
import { ref, onMounted, computed, onUnmounted } from 'vue'
import StatusBadge from '../components/StatusBadge.vue'

// ── Types ────────────────────────────────────────────────────────────────

interface ModelHealthSummary {
  provider_model_id: number
  raw_model_name: string
  outbound_model_name: string
  protocol: string
  provider_name: string
  
  // State distribution
  total_credentials: number
  healthy_count: number
  suspicious_count: number
  failing_count: number
  probing_count: number
  healthy_percentage: number
  failing_percentage: number
  
  // Priority distribution
  urgent_count: number
  suspicious_priority_count: number
  failing_priority_count: number
  watchdog_count: number
  
  // Health metrics
  avg_success_rate_7d: number
  avg_verification_hours: number
  avg_consecutive_successes: number
  
  // Real request stats (24h)
  total_real_success_24h: number
  total_real_failure_24h: number
  real_success_rate_24h?: number
  
  // Timestamps
  last_verified_at?: string
  last_real_request_at?: string
  next_probe_at?: string
  
  // Alerts
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

function getHealthColor(health: string): string {
  switch (health) {
    case 'critical': return 'text-red-600 bg-red-50'
    case 'warning': return 'text-yellow-600 bg-yellow-50'
    case 'degraded': return 'text-orange-600 bg-orange-50'
    case 'healthy': return 'text-green-600 bg-green-50'
    default: return 'text-gray-600 bg-gray-50'
  }
}

function getPriorityColor(priority: string): string {
  switch (priority) {
    case 'urgent': return 'text-red-600 bg-red-50'
    case 'suspicious': return 'text-yellow-600 bg-yellow-50'
    case 'failing': return 'text-orange-600 bg-orange-50'
    case 'recovering': return 'text-blue-600 bg-blue-50'
    case 'watchdog': return 'text-green-600 bg-green-50'
    default: return 'text-gray-600 bg-gray-50'
  }
}

function getStateColor(state: string): string {
  switch (state) {
    case 'healthy': return 'text-green-600'
    case 'suspicious': return 'text-yellow-600'
    case 'failing': return 'text-red-600'
    case 'probing': return 'text-blue-600'
    default: return 'text-gray-600'
  }
}

function formatTime(ts?: string): string {
  if (!ts) return '-'
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
    refreshTimer = window.setInterval(refreshAll, 30000) // 30s
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
  <div class="probe-health-view p-6">
    <!-- Header -->
    <div class="mb-6">
      <h1 class="text-2xl font-semibold text-gray-900 mb-2">探测系统健康度总览</h1>
      <p class="text-sm text-gray-600">
        统一探测调度器实时监控 - 模型状态、优先级队列、节点详情
      </p>
    </div>

    <!-- System Health Card -->
    <div v-if="systemHealth" class="bg-white rounded-lg shadow mb-6 p-6">
      <h2 class="text-lg font-semibold mb-4">系统概览</h2>
      <div class="grid grid-cols-2 md:grid-cols-4 lg:grid-cols-6 gap-4">
        <div>
          <div class="text-sm text-gray-600">总节点数</div>
          <div class="text-2xl font-bold">{{ systemHealth.total_nodes }}</div>
        </div>
        <div>
          <div class="text-sm text-gray-600">健康节点</div>
          <div class="text-2xl font-bold text-green-600">{{ systemHealth.healthy_nodes }}</div>
        </div>
        <div>
          <div class="text-sm text-gray-600">失败节点</div>
          <div class="text-2xl font-bold text-red-600">{{ systemHealth.failing_nodes }}</div>
        </div>
        <div>
          <div class="text-sm text-gray-600">可疑节点</div>
          <div class="text-2xl font-bold text-yellow-600">{{ systemHealth.suspicious_nodes }}</div>
        </div>
        <div>
          <div class="text-sm text-gray-600">探测中</div>
          <div class="text-2xl font-bold text-blue-600">{{ systemHealth.probing_nodes }}</div>
        </div>
        <div>
          <div class="text-sm text-gray-600">危急节点</div>
          <div class="text-2xl font-bold text-red-700">{{ systemHealth.critical_nodes }}</div>
        </div>
      </div>

      <!-- Priority Queues -->
      <div class="mt-6 pt-6 border-t">
        <h3 class="text-sm font-semibold text-gray-700 mb-3">优先级队列</h3>
        <div class="grid grid-cols-2 md:grid-cols-4 gap-4">
          <div class="p-3 rounded-lg bg-red-50">
            <div class="text-xs text-red-600 font-medium">P0 - Urgent</div>
            <div class="text-xl font-bold text-red-700">{{ priorityQueueTotals.urgent }}</div>
          </div>
          <div class="p-3 rounded-lg bg-yellow-50">
            <div class="text-xs text-yellow-600 font-medium">P1 - Suspicious</div>
            <div class="text-xl font-bold text-yellow-700">{{ priorityQueueTotals.suspicious }}</div>
          </div>
          <div class="p-3 rounded-lg bg-orange-50">
            <div class="text-xs text-orange-600 font-medium">P2 - Failing</div>
            <div class="text-xl font-bold text-orange-700">{{ priorityQueueTotals.failing }}</div>
          </div>
          <div class="p-3 rounded-lg bg-green-50">
            <div class="text-xs text-green-600 font-medium">P3 - Watchdog</div>
            <div class="text-xl font-bold text-green-700">{{ priorityQueueTotals.watchdog }}</div>
          </div>
        </div>
      </div>

      <!-- Stats -->
      <div class="mt-6 pt-6 border-t grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
        <div>
          <span class="text-gray-600">平均成功率(7天):</span>
          <span class="ml-2 font-medium">{{ systemHealth.avg_success_rate_7d?.toFixed(1) || '-' }}%</span>
        </div>
        <div>
          <span class="text-gray-600">就绪探测数:</span>
          <span class="ml-2 font-medium">{{ systemHealth.ready_probes }}</span>
        </div>
        <div>
          <span class="text-gray-600">当前探测中:</span>
          <span class="ml-2 font-medium">{{ systemHealth.current_probing }}</span>
        </div>
        <div>
          <span class="text-gray-600">探测凭据数:</span>
          <span class="ml-2 font-medium">{{ systemHealth.credentials_being_probed }}</span>
        </div>
      </div>
    </div>

    <!-- Filters -->
    <div class="bg-white rounded-lg shadow mb-6 p-4">
      <div class="flex flex-wrap gap-4 items-center">
        <input
          v-model="modelFilter"
          @keyup.enter="fetchModels"
          type="text"
          placeholder="搜索模型名..."
          class="px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
        />
        
        <select 
          v-model="healthFilter"
          class="px-3 py-2 border border-gray-300 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-blue-500"
        >
          <option value="">全部健康度</option>
          <option value="healthy">健康</option>
          <option value="degraded">降级</option>
          <option value="warning">警告</option>
          <option value="critical">危急</option>
        </select>

        <button
          @click="refreshAll"
          class="px-4 py-2 bg-blue-600 text-white rounded-lg text-sm hover:bg-blue-700 transition-colors"
        >
          刷新
        </button>

        <label class="flex items-center gap-2 text-sm text-gray-700">
          <input type="checkbox" v-model="autoRefresh" class="rounded" />
          自动刷新 (30s)
        </label>
      </div>
    </div>

    <!-- Models Table -->
    <div class="bg-white rounded-lg shadow overflow-hidden">
      <div class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="bg-gray-50 border-b">
            <tr>
              <th class="px-4 py-3 text-left font-medium text-gray-700">模型</th>
              <th class="px-4 py-3 text-left font-medium text-gray-700">Provider</th>
              <th class="px-4 py-3 text-center font-medium text-gray-700">总节点</th>
              <th class="px-4 py-3 text-center font-medium text-gray-700">健康</th>
              <th class="px-4 py-3 text-center font-medium text-gray-700">可疑</th>
              <th class="px-4 py-3 text-center font-medium text-gray-700">失败</th>
              <th class="px-4 py-3 text-center font-medium text-gray-700">优先级</th>
              <th class="px-4 py-3 text-center font-medium text-gray-700">成功率(7d)</th>
              <th class="px-4 py-3 text-center font-medium text-gray-700">健康度</th>
              <th class="px-4 py-3 text-center font-medium text-gray-700">操作</th>
            </tr>
          </thead>
          <tbody>
            <tr 
              v-for="model in filteredModels" 
              :key="model.provider_model_id"
              class="border-b hover:bg-gray-50 transition-colors"
            >
              <td class="px-4 py-3">
                <div class="font-medium text-gray-900">{{ model.raw_model_name }}</div>
                <div class="text-xs text-gray-500">{{ model.outbound_model_name }}</div>
              </td>
              <td class="px-4 py-3 text-gray-700">{{ model.provider_name }}</td>
              <td class="px-4 py-3 text-center font-medium">{{ model.total_credentials }}</td>
              <td class="px-4 py-3 text-center">
                <span class="text-green-600 font-medium">{{ model.healthy_count }}</span>
                <span class="text-xs text-gray-500 ml-1">({{ model.healthy_percentage.toFixed(0) }}%)</span>
              </td>
              <td class="px-4 py-3 text-center">
                <span class="text-yellow-600 font-medium">{{ model.suspicious_count }}</span>
              </td>
              <td class="px-4 py-3 text-center">
                <span class="text-red-600 font-medium">{{ model.failing_count }}</span>
                <span class="text-xs text-gray-500 ml-1">({{ model.failing_percentage.toFixed(0) }}%)</span>
              </td>
              <td class="px-4 py-3">
                <div class="flex flex-col gap-1 text-xs">
                  <div v-if="model.urgent_count > 0" class="text-red-600">Urgent: {{ model.urgent_count }}</div>
                  <div v-if="model.suspicious_priority_count > 0" class="text-yellow-600">Suspicious: {{ model.suspicious_priority_count }}</div>
                  <div v-if="model.failing_priority_count > 0" class="text-orange-600">Failing: {{ model.failing_priority_count }}</div>
                </div>
              </td>
              <td class="px-4 py-3 text-center">
                <span class="font-medium">{{ model.avg_success_rate_7d.toFixed(1) }}%</span>
              </td>
              <td class="px-4 py-3 text-center">
                <span :class="['px-2 py-1 rounded text-xs font-medium', getHealthColor(model.overall_health)]">
                  {{ model.overall_health }}
                </span>
              </td>
              <td class="px-4 py-3 text-center">
                <button 
                  @click="selectModel(model)"
                  class="px-3 py-1 text-xs bg-blue-600 text-white rounded hover:bg-blue-700 transition-colors"
                >
                  详情
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      
      <div v-if="loading" class="p-8 text-center text-gray-500">
        加载中...
      </div>
      
      <div v-else-if="filteredModels.length === 0" class="p-8 text-center text-gray-500">
        暂无数据
      </div>
    </div>

    <!-- Model Detail Modal -->
    <div 
      v-if="selectedModel" 
      class="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center p-4 z-50"
      @click.self="closeDetail"
    >
      <div class="bg-white rounded-lg shadow-xl max-w-6xl w-full max-h-[90vh] overflow-hidden flex flex-col">
        <!-- Modal Header -->
        <div class="px-6 py-4 border-b flex items-center justify-between">
          <div>
            <h2 class="text-xl font-semibold">{{ selectedModel.raw_model_name }}</h2>
            <p class="text-sm text-gray-600">{{ selectedModel.provider_name }}</p>
          </div>
          <button 
            @click="closeDetail"
            class="p-2 hover:bg-gray-100 rounded-lg transition-colors"
          >
            <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"/>
            </svg>
          </button>
        </div>

        <!-- Modal Body -->
        <div class="flex-1 overflow-y-auto p-6">
          <div v-if="nodesLoading" class="text-center text-gray-500 py-8">
            加载节点详情...
          </div>
          
          <div v-else class="space-y-4">
            <div 
              v-for="node in modelNodes" 
              :key="node.credential_id"
              class="border rounded-lg p-4 hover:bg-gray-50 transition-colors"
            >
              <div class="flex items-start justify-between mb-3">
                <div>
                  <div class="font-medium text-gray-900">{{ node.credential_label }}</div>
                  <div class="text-xs text-gray-500">Credential #{{ node.credential_id }}</div>
                </div>
                <div class="flex gap-2">
                  <span :class="['px-2 py-1 rounded text-xs font-medium', getPriorityColor(node.probe_priority)]">
                    {{ node.probe_priority }}
                  </span>
                  <span :class="['px-2 py-1 rounded text-xs font-medium', getStateColor(node.state)]">
                    {{ node.state }}
                  </span>
                </div>
              </div>

              <div class="grid grid-cols-2 md:grid-cols-4 gap-3 text-sm">
                <div>
                  <div class="text-gray-600 text-xs">连续成功</div>
                  <div class="font-medium">{{ node.consecutive_successes }}</div>
                </div>
                <div>
                  <div class="text-gray-600 text-xs">连续失败</div>
                  <div class="font-medium text-red-600">{{ node.consecutive_failures }}</div>
                </div>
                <div>
                  <div class="text-gray-600 text-xs">成功率(7d)</div>
                  <div class="font-medium">{{ node.success_rate_7d?.toFixed(1) || '-' }}%</div>
                </div>
                <div>
                  <div class="text-gray-600 text-xs">验证间隔</div>
                  <div class="font-medium">{{ node.verification_interval }}</div>
                </div>
                <div>
                  <div class="text-gray-600 text-xs">实际成功(24h)</div>
                  <div class="font-medium text-green-600">{{ node.real_success_24h }}</div>
                </div>
                <div>
                  <div class="text-gray-600 text-xs">实际失败(24h)</div>
                  <div class="font-medium text-red-600">{{ node.real_failure_24h }}</div>
                </div>
                <div>
                  <div class="text-gray-600 text-xs">下次探测</div>
                  <div class="font-medium">{{ node.retry_in }}</div>
                </div>
                <div>
                  <div class="text-gray-600 text-xs">状态持续</div>
                  <div class="font-medium">{{ formatDuration(node.state_duration_minutes) }}</div>
                </div>
              </div>

              <div v-if="node.last_unavailable_reason" class="mt-3 pt-3 border-t">
                <div class="text-xs text-gray-600 mb-1">最后错误</div>
                <div class="text-sm text-red-600 font-mono">{{ node.last_unavailable_reason }}</div>
              </div>
            </div>

            <div v-if="modelNodes.length === 0" class="text-center text-gray-500 py-8">
              暂无节点数据
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.probe-health-view {
  min-height: 100vh;
  background: #f9fafb;
}
</style>

<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { req } from '../api/_core'
import { resolveRouting } from '../api/routing'
import { getDecisions } from '../api/routing'
import { getRequestLogs } from '../api/logs'
import { getSlidingWindow } from '../api/credential-monitor'
import { store } from '../store'

const route = useRoute()
const router = useRouter()
const modelName = ref((route.query.model as string) || '')

type TabId = 'nodes' | 'routing' | 'decisions' | 'monitor' | 'logs' | 'probe' | 'pricing'
const tabs: { id: TabId; label: string }[] = [
  { id: 'nodes', label: '节点概览' },
  { id: 'routing', label: '路由路径' },
  { id: 'decisions', label: '决策日志' },
  { id: 'monitor', label: '请求监控' },
  { id: 'logs', label: '请求记录' },
  { id: 'probe', label: '探活操作' },
  { id: 'pricing', label: '价格管理' },
]
const activeTab = ref<TabId>('nodes')
const loadedTabs = ref(new Set<TabId>())
const loading = ref(false)
const isAdmin = computed(() => store.userInfo?.role === 'super_admin')

function switchTab(tab: TabId) {
  activeTab.value = tab
  if (!loadedTabs.value.has(tab)) {
    loadedTabs.value.add(tab)
    loadTabData(tab)
  }
}

const tabData = ref<Record<string, any>>({})

async function loadTabData(tab: TabId) {
  loading.value = true
  try {
    switch (tab) {
      case 'nodes': await loadNodes(); break
      case 'routing': await loadRouting(); break
      case 'decisions': await loadDecisions(); break
      case 'monitor': await loadMonitor(); break
      case 'logs': await loadLogs(); break
      case 'probe': /* probe tab loads on demand via button click */ break
      case 'pricing': await loadPricing(); break
    }
  } catch (err) {
    console.error(`Failed to load tab ${tab}:`, err)
  } finally {
    loading.value = false
  }
}

interface ModelNode {
  credential_id: number
  credential_label: string
  provider_id: number
  provider_name: string
  state: string
  probe_priority: string
  consecutive_successes: number
  consecutive_failures: number
  success_rate_7d?: number
  last_verified_at?: string
  next_retry_at?: string
  last_unavailable_reason?: string
  state_duration_minutes: number
}

const nodes = ref<ModelNode[]>([])

async function loadNodes() {
  const data = await req<{ nodes: ModelNode[] }>('GET', `/api/admin/probe/model/${encodeURIComponent(modelName.value)}/nodes`)
  nodes.value = data.nodes || []
}

interface RoutingResolveResult {
  candidates: any[]
  resolution_path: string
  raw_models: string[]
  plan_order: Array<{ credential_id: number; provider_id: number; raw_model: string; tier: number }>
}

const routingResult = ref<RoutingResolveResult | null>(null)

async function loadRouting() {
  routingResult.value = await resolveRouting(modelName.value)
}

interface Decision {
  ts: string
  request_id: string
  chosen_credential_id: number | null
  chosen_provider_id: number | null
  success: boolean
  latency_ms: number | null
  error_class: string | null
}

const decisions = ref<Decision[]>([])

async function loadDecisions() {
  const data = await getDecisions({ model: modelName.value, limit: 50 })
  decisions.value = data.decisions || []
}

const monitorEntries = ref<any[]>([])
const monitorStats = ref<any>(null)

async function loadMonitor() {
  monitorEntries.value = []
  monitorStats.value = null
  for (const node of nodes.value) {
    try {
      const data = await getSlidingWindow(node.credential_id, modelName.value, 60)
      monitorStats.value = data.stats
      monitorEntries.value = data.entries || []
      return
    } catch {
      continue
    }
  }
}

interface RequestLog {
  ts: string
  request_id: string
  success: boolean
  latency_ms: number | null
  error_kind: string | null
  credential_label?: string
  prompt_tokens: number | null
  completion_tokens: number | null
}

const requestLogs = ref<RequestLog[]>([])

async function loadLogs() {
  const data = await getRequestLogs({ model: modelName.value, page_size: 50 })
  requestLogs.value = (data.items || []) as RequestLog[]
}

const pricingData = ref<any[]>([])

async function loadPricing() {
  const data = await req<any[]>('GET', `/api/pricing?model=${encodeURIComponent(modelName.value)}`)
  pricingData.value = Array.isArray(data) ? data : []
}

async function triggerProbe(node: ModelNode) {
  try {
    await req('POST', `/api/providers/${node.provider_id}/probe-history/trigger`, {
      credential_id: node.credential_id,
      raw_model_name: modelName.value,
    })
    alert('探活已触发')
  } catch (err) {
    console.error('Trigger probe failed:', err)
    alert('触发失败')
  }
}

function goBack() {
  router.push({ path: '/probe-health' })
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

function formatTime(ts?: string): string {
  if (!ts) return '—'
  return new Date(ts).toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
}

function formatDuration(minutes: number): string {
  if (minutes < 60) return `${Math.round(minutes)}分钟`
  const hours = Math.floor(minutes / 60)
  const mins = Math.round(minutes % 60)
  return mins > 0 ? `${hours}小时${mins}分钟` : `${hours}小时`
}

onMounted(() => {
  switchTab('nodes')
})
</script>

<template>
  <div class="detail-container">
    <div class="detail-header">
      <button @click="goBack" class="btn btn-sm btn-ghost">← 返回列表</button>
      <h1>{{ modelName }}</h1>
    </div>

    <div class="tabs">
      <button
        v-for="tab in tabs"
        :key="tab.id"
        :class="['tab-btn', { active: activeTab === tab.id }]"
        @click="switchTab(tab.id)"
      >
        {{ tab.label }}
      </button>
    </div>

    <div class="tab-content card">
      <div v-if="loading" class="empty-state">加载中...</div>

      <!-- Tab: 节点概览 -->
      <div v-else-if="activeTab === 'nodes'">
        <div v-if="nodes.length === 0" class="empty-state">暂无节点数据</div>
        <div v-else class="node-list">
          <div v-for="node in nodes" :key="node.credential_id" class="node-card">
            <div class="node-header">
              <div>
                <div class="node-label">{{ node.credential_label }}</div>
                <div class="muted-text small">Credential #{{ node.credential_id }} · {{ node.provider_name }}</div>
              </div>
              <div class="node-badges">
                <span :class="['badge', getPriorityBadge(node.probe_priority)]">{{ node.probe_priority }}</span>
                <span class="badge" :style="{ color: getStateColor(node.state), background: 'rgba(255,255,255,0.1)' }">{{ node.state }}</span>
              </div>
            </div>
            <div class="node-stats">
              <div class="node-stat"><span class="muted-text small">连续成功</span><div>{{ node.consecutive_successes }}</div></div>
              <div class="node-stat"><span class="muted-text small">连续失败</span><div class="rate-bad">{{ node.consecutive_failures }}</div></div>
              <div class="node-stat"><span class="muted-text small">成功率(7d)</span><div>{{ node.success_rate_7d?.toFixed(1) || '—' }}%</div></div>
              <div class="node-stat"><span class="muted-text small">上次验证</span><div>{{ formatTime(node.last_verified_at) }}</div></div>
              <div class="node-stat"><span class="muted-text small">下次重试</span><div>{{ formatTime(node.next_retry_at) }}</div></div>
              <div class="node-stat"><span class="muted-text small">状态持续</span><div>{{ formatDuration(node.state_duration_minutes) }}</div></div>
            </div>
            <div v-if="node.last_unavailable_reason" class="node-error">
              <span class="muted-text small">最后错误</span>
              <div class="error-text">{{ node.last_unavailable_reason }}</div>
            </div>
          </div>
        </div>
      </div>

      <!-- Tab: 路由路径 -->
      <div v-else-if="activeTab === 'routing'">
        <div v-if="!routingResult" class="empty-state">暂无路由数据</div>
        <div v-else>
          <div class="info-row"><span class="info-label">解析路径</span><span>{{ routingResult.resolution_path }}</span></div>
          <div class="info-row"><span class="info-label">原始模型</span><span>{{ routingResult.raw_models?.join(', ') || '—' }}</span></div>
          <h3 style="margin-top:16px">候选凭据</h3>
          <table class="data-table">
            <thead>
              <tr>
                <th>层级</th>
                <th>Provider</th>
                <th>凭据</th>
                <th>成功率</th>
                <th>延迟P95</th>
                <th>可用</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in routingResult.candidates" :key="c.credential_id">
                <td>T{{ c.tier }}</td>
                <td>{{ c.provider_name }}</td>
                <td>{{ c.credential_label || '#' + c.credential_id }}</td>
                <td>{{ (c.success_rate * 100).toFixed(1) }}%</td>
                <td>{{ c.p95_latency_ms }}ms</td>
                <td>
                  <span :class="['badge', c.routable ? 'badge-green' : 'badge-gray']">
                    {{ c.routable ? '是' : '否' }}
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Tab: 决策日志 -->
      <div v-else-if="activeTab === 'decisions'">
        <div v-if="decisions.length === 0" class="empty-state">暂无决策记录</div>
        <table v-else class="data-table">
          <thead>
            <tr>
              <th>时间</th>
              <th>请求ID</th>
              <th>凭据</th>
              <th>结果</th>
              <th>延迟</th>
              <th>错误</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="d in decisions" :key="d.request_id">
              <td>{{ formatTime(d.ts) }}</td>
              <td class="mono">{{ d.request_id.slice(0, 12) }}…</td>
              <td>#{{ d.chosen_credential_id }}</td>
              <td>
                <span :class="['badge', d.success ? 'badge-green' : 'badge-red']">
                  {{ d.success ? '成功' : '失败' }}
                </span>
              </td>
              <td>{{ d.latency_ms ?? '—' }}ms</td>
              <td>{{ d.error_class || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Tab: 请求监控 -->
      <div v-else-if="activeTab === 'monitor'">
        <div v-if="monitorStats" class="stats-row">
          <div class="stat-mini"><span class="stat-mini-label">请求总数</span><span class="stat-mini-val">{{ monitorStats.total }}</span></div>
          <div class="stat-mini"><span class="stat-mini-label">成功</span><span class="stat-mini-val rate-good">{{ monitorStats.success }}</span></div>
          <div class="stat-mini"><span class="stat-mini-label">失败</span><span class="stat-mini-val rate-bad">{{ monitorStats.failed }}</span></div>
          <div class="stat-mini"><span class="stat-mini-label">失败率</span><span class="stat-mini-val rate-bad">{{ (monitorStats.failure_rate * 100).toFixed(1) }}%</span></div>
        </div>
        <div v-if="monitorEntries.length === 0" class="empty-state">暂无监控数据</div>
        <table v-else class="data-table">
          <thead>
            <tr>
              <th>时间</th>
              <th>结果</th>
              <th>延迟</th>
              <th>错误</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="e in monitorEntries.slice(0, 100)" :key="e.rid">
              <td>{{ new Date(e.ts).toLocaleString('zh-CN') }}</td>
              <td>
                <span :class="['badge', e.ok ? 'badge-green' : 'badge-red']">{{ e.ok ? '成功' : '失败' }}</span>
              </td>
              <td>{{ e.lat }}ms</td>
              <td>{{ e.err || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Tab: 请求记录 -->
      <div v-else-if="activeTab === 'logs'">
        <div v-if="requestLogs.length === 0" class="empty-state">暂无请求记录</div>
        <table v-else class="data-table">
          <thead>
            <tr>
              <th>时间</th>
              <th>请求ID</th>
              <th>结果</th>
              <th>延迟</th>
              <th>Token</th>
              <th>错误</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="log in requestLogs" :key="log.request_id">
              <td>{{ formatTime(log.ts) }}</td>
              <td class="mono">{{ log.request_id.slice(0, 12) }}…</td>
              <td>
                <span :class="['badge', log.success ? 'badge-green' : 'badge-red']">{{ log.success ? '成功' : '失败' }}</span>
              </td>
              <td>{{ log.latency_ms ?? '—' }}ms</td>
              <td>{{ (log.prompt_tokens ?? 0) + (log.completion_tokens ?? 0) }}</td>
              <td>{{ log.error_kind || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <!-- Tab: 探活操作 -->
      <div v-else-if="activeTab === 'probe'">
        <div v-if="nodes.length === 0" class="empty-state">请先在「节点概览」标签页加载数据</div>
        <div v-else>
          <p class="muted-text" style="margin-bottom:12px">点击「触发探活」手动对指定凭据发起探测：</p>
          <div v-for="node in nodes" :key="node.credential_id" class="probe-row">
            <div>
              <strong>{{ node.credential_label }}</strong>
              <span class="muted-text small"> · #{{ node.credential_id }} · {{ node.provider_name }}</span>
            </div>
            <button @click="triggerProbe(node)" class="btn btn-sm btn-primary">触发探活</button>
          </div>
        </div>
      </div>

      <!-- Tab: 价格管理 -->
      <div v-else-if="activeTab === 'pricing'">
        <div v-if="!isAdmin" class="empty-state">仅超级管理员可查看价格</div>
        <div v-else-if="pricingData.length === 0" class="empty-state">暂无价格数据</div>
        <table v-else class="data-table">
          <thead>
            <tr>
              <th>Provider</th>
              <th>凭据</th>
              <th>输入价格</th>
              <th>输出价格</th>
              <th>币种</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in pricingData" :key="p.id || p.credential_id">
              <td>{{ p.provider_name || p.provider_id }}</td>
              <td>{{ p.credential_label || '#' + p.credential_id }}</td>
              <td>{{ p.price_in_per_1m ?? '—' }}</td>
              <td>{{ p.price_out_per_1m ?? '—' }}</td>
              <td>{{ p.currency || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.detail-container {
  padding: 20px;
}

.detail-header {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 16px;
}

.detail-header h1 {
  font-size: 20px;
  font-weight: 600;
  margin: 0;
}

.tabs {
  display: flex;
  gap: 4px;
  margin-bottom: 16px;
  border-bottom: 1px solid var(--border);
  padding-bottom: 0;
}

.tab-btn {
  background: none;
  border: none;
  color: var(--muted);
  padding: 8px 16px;
  font-size: 13px;
  cursor: pointer;
  border-bottom: 2px solid transparent;
  margin-bottom: -1px;
}

.tab-btn:hover {
  color: var(--text);
}

.tab-btn.active {
  color: var(--accent);
  border-bottom-color: var(--accent);
}

.tab-content {
  padding: 20px;
}

.empty-state {
  padding: 40px;
  text-align: center;
  color: var(--muted);
}

.node-list {
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

.info-row {
  display: flex;
  gap: 8px;
  padding: 6px 0;
}

.info-label {
  color: var(--muted);
  min-width: 80px;
  font-size: 13px;
}

.data-table {
  font-size: 13px;
  width: 100%;
}

.data-table thead th {
  padding: 8px 12px;
  font-size: 11px;
  text-transform: uppercase;
  font-weight: 600;
  text-align: left;
  border-bottom: 1px solid var(--border);
}

.data-table tbody td {
  padding: 8px 12px;
  border-bottom: 1px solid var(--border);
}

.data-table tbody tr:hover {
  background: rgba(255, 255, 255, 0.02);
}

.mono {
  font-family: monospace;
  font-size: 12px;
}

.stats-row {
  display: flex;
  gap: 24px;
  margin-bottom: 16px;
}

.stat-mini {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.stat-mini-label {
  font-size: 11px;
  color: var(--muted);
  text-transform: uppercase;
}

.stat-mini-val {
  font-size: 20px;
  font-weight: 700;
}

.probe-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 10px 0;
  border-bottom: 1px solid var(--border);
}

.probe-row:last-child {
  border-bottom: none;
}

.badge-green { color: var(--success); border: 1px solid var(--success); }
.badge-red { color: var(--danger); border: 1px solid var(--danger); }
.badge-yellow { color: var(--warning); border: 1px solid var(--warning); }
.badge-blue { color: var(--accent-h); border: 1px solid var(--accent-h); }
.badge-gray { color: var(--muted); border: 1px solid var(--muted); }
.badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
}

.muted-text { color: var(--muted); }
.small { font-size: 11px; }
.rate-good { color: var(--success); font-weight: 600; }
.rate-bad { color: var(--danger); font-weight: 600; }
</style>

<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
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
    void loadTabData(tab)
  }
}

watch(modelName, () => {
  loadedTabs.value = new Set()
  switchTab('nodes')
})

async function loadTabData(tab: TabId) {
  loading.value = true
  try {
    switch (tab) {
      case 'nodes': await loadNodes(); break
      case 'routing': await loadRouting(); break
      case 'decisions': await loadDecisions(); break
      case 'monitor': await loadMonitor(); break
      case 'logs': await loadLogs(); break
      case 'probe': break
      case 'pricing': await loadPricing(); break
    }
  } catch (err) {
    console.error(`Failed to load tab ${tab}:`, err)
    setTabError(tab, err instanceof Error ? err.message : String(err))
  } finally {
    loading.value = false
  }
}

const tabErrors = ref<Record<string, string>>({})
function setTabError(tab: string, msg: string) {
  tabErrors.value = { ...tabErrors.value, [tab]: msg }
}

// ── Tab: 节点概览 ─────────────────────────────────────────────────────────
interface ModelNode {
  raw_model_name: string
  outbound_model_name: string
  probe_priority: string
  state: string
  credential_id: number
  credential_label: string
  provider_id: number
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
const nodes = ref<ModelNode[]>([])
const nodeSummary = ref<{ total: number; healthy: number; failing: number; suspicious: number; probing: number } | null>(null)

async function loadNodes() {
  const data = await req<{ model: string; nodes: ModelNode[]; total: number }>(
    'GET',
    `/api/admin/probe/model/${encodeURIComponent(modelName.value)}/nodes`,
  )
  nodes.value = data.nodes || []
  try {
    const sum = await req<{ state_distribution?: Record<string, number> }>(
      'GET',
      `/api/admin/probe/model/${encodeURIComponent(modelName.value)}/state-summary`,
    )
    const dist = sum.state_distribution || {}
    nodeSummary.value = {
      total: nodes.value.length,
      healthy: (dist.healthy || 0) + (dist.healthy_confirmed || 0) + (dist.available || 0),
      failing: (dist.failing || 0) + (dist.broken_confirmed || 0),
      suspicious: dist.suspicious || 0,
      probing: dist.probing || 0,
    }
  } catch {
    nodeSummary.value = null
  }
}

// ── Tab: 路由路径 ─────────────────────────────────────────────────────────
import type { RoutingResolveResponse } from '../api/routing'
const routingResult = ref<RoutingResolveResponse | null>(null)

async function loadRouting() {
  routingResult.value = await resolveRouting(modelName.value)
}

// ── Tab: 决策日志 ─────────────────────────────────────────────────────────
interface Decision {
  ts: string
  request_id: string
  chosen_credential_id: number | null
  chosen_provider_id: number | null
  tier: number | null
  success: boolean
  latency_ms: number | null
  error_class: string | null
  prompt_tokens?: number | null
  completion_tokens?: number | null
  cost_usd?: number | string | null
}
const decisions = ref<Decision[]>([])

async function loadDecisions() {
  const data = await getDecisions({
    model: modelName.value,
    limit: 100,
    since_minutes: 60 * 24 * 7,
  })
  decisions.value = data.decisions || []
}

// ── Tab: 请求监控 ─────────────────────────────────────────────────────────
interface MonitorEntry {
  rid: string
  ts: number
  ok: boolean
  lat: number
  err?: string
}
interface MonitorStats {
  total: number
  success: number
  failed: number
  failure_rate: number
  error_kinds: Record<string, number>
}
const monitorEntries = ref<MonitorEntry[]>([])
const monitorStats = ref<MonitorStats | null>(null)
const monitorWindow = ref<number>(60)

async function loadMonitor() {
  if (nodes.value.length === 0) {
    await loadNodes()
  }
  monitorEntries.value = []
  monitorStats.value = null
  const allEntries: MonitorEntry[] = []
  let totalSuccess = 0
  let totalFailed = 0
  const errorKinds: Record<string, number> = {}
  let hitAny = false

  for (const node of nodes.value) {
    try {
      const data = await getSlidingWindow(node.credential_id, modelName.value, monitorWindow.value)
      if (!data || !data.entries) continue
      allEntries.push(...data.entries)
      totalSuccess += data.stats?.success || 0
      totalFailed += data.stats?.failed || 0
      const k = data.stats?.error_kinds || {}
      for (const [k2, v] of Object.entries(k)) errorKinds[k2] = (errorKinds[k2] || 0) + v
      hitAny = true
    } catch {
      // Skip credentials with no live data
    }
  }
  if (hitAny) {
    allEntries.sort((a, b) => b.ts - a.ts)
    monitorEntries.value = allEntries
    const total = totalSuccess + totalFailed
    monitorStats.value = {
      total,
      success: totalSuccess,
      failed: totalFailed,
      failure_rate: total > 0 ? totalFailed / total : 0,
      error_kinds: errorKinds,
    }
  } else {
    monitorStats.value = {
      total: 0,
      success: 0,
      failed: 0,
      failure_rate: 0,
      error_kinds: {},
    }
  }
}

// ── Tab: 请求记录 ─────────────────────────────────────────────────────────
interface RequestLog {
  ts: string
  request_id: string
  success: boolean
  latency_ms: number | null
  error_kind: string | null
  credential_id: number | null
  credential_label: string | null
  provider_name: string | null
  prompt_tokens: number | null
  completion_tokens: number | null
  cost_usd: number | string | null
  cost_display?: number | string | null
  cost_currency: string | null
}
const requestLogs = ref<RequestLog[]>([])

async function loadLogs() {
  const data = await getRequestLogs({
    model: modelName.value,
    page_size: 100,
    chrono: true,
  })
  requestLogs.value = (data.items || []) as RequestLog[]
}

// ── Tab: 探活操作 ─────────────────────────────────────────────────────────
const probeTriggering = ref<Set<number>>(new Set())
const flashMsg = ref<string>('')

async function triggerProbe(node: ModelNode) {
  if (probeTriggering.value.has(node.credential_id)) return
  const next = new Set(probeTriggering.value)
  next.add(node.credential_id)
  probeTriggering.value = next
  try {
    await req('POST', `/api/providers/${node.provider_id}/probe-history/trigger`, {
      credential_id: node.credential_id,
      raw_model_name: modelName.value,
    })
    flash(`已触发凭据 #${node.credential_id} 的探活`)
  } catch (err) {
    flash(`触发失败: ${err instanceof Error ? err.message : String(err)}`)
  } finally {
    setTimeout(() => {
      const after = new Set(probeTriggering.value)
      after.delete(node.credential_id)
      probeTriggering.value = after
    }, 1000)
  }
}

async function triggerAllProbes() {
  if (!confirm(`对模型 ${modelName.value} 的全部 ${nodes.value.length} 个凭据触发探活?`)) return
  for (const node of nodes.value) {
    await triggerProbe(node)
  }
}

let flashTimer: number | null = null
function flash(message: string) {
  flashMsg.value = message
  if (flashTimer) clearTimeout(flashTimer)
  flashTimer = window.setTimeout(() => (flashMsg.value = ''), 3000)
}

// ── Tab: 价格管理 ─────────────────────────────────────────────────────────
interface PricingRow {
  canonical_name: string
  raw_model_name: string
  offer_id: number
  unit_price_in_per_1m: number | null
  unit_price_out_per_1m: number | null
  cache_read_price_per_1m: number | null
  cache_write_price_per_1m: number | null
  currency: string | null
  billing_mode: string | null
  pricing_source: string | null
  available: boolean
  credential_id: number
  credential_label: string
  provider_name: string
}
const pricingData = ref<PricingRow[]>([])

async function loadPricing() {
  try {
    const data = await req<{ rows: PricingRow[]; count: number }>(
      'GET',
      `/api/pricing/table?search=${encodeURIComponent(modelName.value)}&page_size=50`,
    )
    pricingData.value = (data.rows || []).filter(
      (r) => r.raw_model_name === modelName.value || r.canonical_name === modelName.value,
    )
    if (pricingData.value.length === 0) {
      pricingData.value = data.rows || []
    }
  } catch {
    setTabError('pricing', '价格数据不可用（需要 platform_ops 权限）')
    pricingData.value = []
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
  try {
    return new Date(ts).toLocaleString('zh-CN', {
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    })
  } catch {
    return ts
  }
}

function formatDuration(minutes: number): string {
  if (minutes < 60) return `${Math.round(minutes)}分钟`
  const hours = Math.floor(minutes / 60)
  const mins = Math.round(minutes % 60)
  return mins > 0 ? `${hours}小时${mins}分钟` : `${hours}小时`
}

function formatNumber(n: number | null | undefined, decimals = 2): string {
  if (n == null) return '—'
  return Number(n).toFixed(decimals)
}

function formatPrice(p: number | string | null | undefined, currency: string | null | undefined): string {
  if (p == null) return '—'
  return `${Number(p).toFixed(4)} ${currency || ''}`.trim()
}

function formatTimestamp(ts: number): string {
  return new Date(ts).toLocaleString('zh-CN')
}

onMounted(() => {
  switchTab('nodes')
})
</script>

<template>
  <div class="detail-container">
    <div class="detail-header">
      <button @click="goBack" class="btn btn-sm btn-ghost">← 返回列表</button>
      <h1>{{ modelName || '未指定模型' }}</h1>
      <span v-if="nodeSummary" class="state-badges">
        <span class="state-badge state-badge-good">{{ nodeSummary.healthy }} 健康</span>
        <span class="state-badge state-badge-warn">{{ nodeSummary.suspicious }} 可疑</span>
        <span class="state-badge state-badge-bad">{{ nodeSummary.failing }} 失败</span>
        <span class="state-badge state-badge-info">{{ nodeSummary.probing }} 探测中</span>
      </span>
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

    <div v-if="flashMsg" class="flash">{{ flashMsg }}</div>

    <div class="tab-content card">
      <div v-if="loading" class="empty-state">加载中...</div>

      <div v-else-if="activeTab === 'nodes'">
        <div v-if="nodes.length === 0" class="empty-state">暂无节点数据</div>
        <div v-else class="node-list">
          <div v-for="node in nodes" :key="node.credential_id" class="node-card">
            <div class="node-header">
              <div>
                <div class="node-label">{{ node.credential_label }}</div>
                <div class="muted-text small">
                  Credential #{{ node.credential_id }} · {{ node.provider_name }}
                </div>
              </div>
              <div class="node-badges">
                <span :class="['badge', getPriorityBadge(node.probe_priority)]">
                  {{ node.probe_priority }}
                </span>
                <span class="badge" :style="{ color: getStateColor(node.state), background: 'rgba(255,255,255,0.06)' }">
                  {{ node.state }}
                </span>
              </div>
            </div>
            <div class="node-stats">
              <div class="node-stat"><span class="muted-text small">连续成功</span><div>{{ node.consecutive_successes }}</div></div>
              <div class="node-stat"><span class="muted-text small">连续失败</span><div class="rate-bad">{{ node.consecutive_failures }}</div></div>
              <div class="node-stat"><span class="muted-text small">成功率(7d)</span><div>{{ formatNumber(node.success_rate_7d, 1) }}%</div></div>
              <div class="node-stat"><span class="muted-text small">上次验证</span><div>{{ formatTime(node.last_verified_at) }}</div></div>
              <div class="node-stat"><span class="muted-text small">下次重试</span><div>{{ formatTime(node.next_retry_at) }}</div></div>
              <div class="node-stat"><span class="muted-text small">状态持续</span><div>{{ formatDuration(node.state_duration_minutes) }}</div></div>
              <div class="node-stat"><span class="muted-text small">实际成功(24h)</span><div class="rate-good">{{ node.real_success_24h }}</div></div>
              <div class="node-stat"><span class="muted-text small">实际失败(24h)</span><div class="rate-bad">{{ node.real_failure_24h }}</div></div>
            </div>
            <div v-if="node.last_unavailable_reason" class="node-error">
              <span class="muted-text small">最后错误</span>
              <div class="error-text">{{ node.last_unavailable_reason }}</div>
            </div>
          </div>
        </div>
      </div>

      <div v-else-if="activeTab === 'routing'">
        <div v-if="!routingResult" class="empty-state">暂无路由数据</div>
        <div v-else>
          <div class="info-grid">
            <div class="info-row"><span class="info-label">客户端模型</span><span>{{ routingResult.client_model }}</span></div>
            <div class="info-row"><span class="info-label">规范化名</span><span>{{ routingResult.canonical_name || '—' }}</span></div>
            <div class="info-row"><span class="info-label">解析路径</span><span class="mono">{{ routingResult.resolution_path }}</span></div>
            <div class="info-row"><span class="info-label">原始模型</span><span>{{ routingResult.raw_models?.join(', ') || '—' }}</span></div>
            <div class="info-row"><span class="info-label">可路由凭据</span><span>{{ routingResult.candidates?.filter(c => c.routable).length || 0 }} / {{ routingResult.candidates?.length || 0 }}</span></div>
          </div>
          <h3 style="margin-top:16px">候选凭据</h3>
          <div v-if="!routingResult.candidates || routingResult.candidates.length === 0" class="empty-state">无可用候选</div>
          <table v-else class="data-table">
            <thead>
              <tr>
                <th>层级</th>
                <th>Provider</th>
                <th>凭据</th>
                <th>成功率</th>
                <th>延迟P95</th>
                <th>输入价</th>
                <th>输出价</th>
                <th>状态</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="c in routingResult.candidates" :key="c.credential_id">
                <td>T{{ c.tier }}</td>
                <td>{{ c.provider_name }}</td>
                <td>
                  {{ c.credential_label || '#' + c.credential_id }}
                  <div v-if="c.runtime_block_reason" class="muted-text small">{{ c.runtime_block_reason }}</div>
                </td>
                <td>{{ formatNumber(c.success_rate * 100, 1) }}%</td>
                <td>{{ c.p95_latency_ms }}ms</td>
                <td>{{ formatPrice(c.unit_price_in_per_1m, c.currency) }}</td>
                <td>{{ formatPrice(c.unit_price_out_per_1m, c.currency) }}</td>
                <td>
                  <span :class="['badge', c.routable ? 'badge-green' : 'badge-red']">
                    {{ c.routable ? '可路由' : '不可用' }}
                  </span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <div v-else-if="activeTab === 'decisions'">
        <div v-if="decisions.length === 0" class="empty-state">近 7 天无决策记录</div>
        <table v-else class="data-table">
          <thead>
            <tr>
              <th>时间</th>
              <th>请求ID</th>
              <th>凭据</th>
              <th>层级</th>
              <th>结果</th>
              <th>延迟</th>
              <th>Tokens</th>
              <th>成本</th>
              <th>错误</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="d in decisions" :key="d.request_id">
              <td>{{ formatTime(d.ts) }}</td>
              <td class="mono">{{ d.request_id.slice(0, 12) }}…</td>
              <td>{{ d.chosen_credential_id ? '#' + d.chosen_credential_id : '—' }}</td>
              <td>{{ d.tier != null ? 'T' + d.tier : '—' }}</td>
              <td>
                <span :class="['badge', d.success ? 'badge-green' : 'badge-red']">
                  {{ d.success ? '成功' : '失败' }}
                </span>
              </td>
              <td>{{ d.latency_ms ?? '—' }}ms</td>
              <td>{{ ((d.prompt_tokens || 0) + (d.completion_tokens || 0)) || '—' }}</td>
              <td>{{ d.cost_usd != null ? '$' + formatNumber(Number(d.cost_usd), 4) : '—' }}</td>
              <td>{{ d.error_class || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else-if="activeTab === 'monitor'">
        <div class="window-controls">
          <span class="muted-text">时间窗口:</span>
          <button
            v-for="w in [15, 60, 360, 1440]"
            :key="w"
            :class="['window-btn', { active: monitorWindow === w }]"
            @click="monitorWindow = w; void loadMonitor()"
          >
            {{ w < 60 ? `${w}分钟` : `${w / 60}小时` }}
          </button>
        </div>
        <div v-if="monitorStats" class="stats-row">
          <div class="stat-mini"><span class="stat-mini-label">请求总数</span><span class="stat-mini-val">{{ monitorStats.total }}</span></div>
          <div class="stat-mini"><span class="stat-mini-label">成功</span><span class="stat-mini-val rate-good">{{ monitorStats.success }}</span></div>
          <div class="stat-mini"><span class="stat-mini-label">失败</span><span class="stat-mini-val rate-bad">{{ monitorStats.failed }}</span></div>
          <div class="stat-mini"><span class="stat-mini-label">失败率</span><span class="stat-mini-val rate-bad">{{ formatNumber(monitorStats.failure_rate * 100, 1) }}%</span></div>
        </div>
        <div v-if="monitorStats && Object.keys(monitorStats.error_kinds).length > 0" class="error-kinds">
          <span class="muted-text small">错误分类:</span>
          <span v-for="(count, kind) in monitorStats.error_kinds" :key="kind" class="badge badge-red">{{ kind }}: {{ count }}</span>
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
            <tr v-for="e in monitorEntries.slice(0, 200)" :key="e.rid">
              <td>{{ formatTimestamp(e.ts) }}</td>
              <td>
                <span :class="['badge', e.ok ? 'badge-green' : 'badge-red']">
                  {{ e.ok ? '成功' : '失败' }}
                </span>
              </td>
              <td>{{ e.lat }}ms</td>
              <td>{{ e.err || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else-if="activeTab === 'logs'">
        <div v-if="requestLogs.length === 0" class="empty-state">暂无请求记录</div>
        <table v-else class="data-table">
          <thead>
            <tr>
              <th>时间</th>
              <th>请求ID</th>
              <th>凭据</th>
              <th>结果</th>
              <th>延迟</th>
              <th>Prompt</th>
              <th>Completion</th>
              <th>成本</th>
              <th>错误</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="log in requestLogs" :key="log.request_id">
              <td>{{ formatTime(log.ts) }}</td>
              <td class="mono">{{ log.request_id.slice(0, 12) }}…</td>
              <td>
                <span v-if="log.credential_label">{{ log.credential_label }}</span>
                <span v-else-if="log.credential_id">#{{ log.credential_id }}</span>
                <span v-else>—</span>
                <div v-if="log.provider_name" class="muted-text small">{{ log.provider_name }}</div>
              </td>
              <td>
                <span :class="['badge', log.success ? 'badge-green' : 'badge-red']">
                  {{ log.success ? '成功' : '失败' }}
                </span>
              </td>
              <td>{{ log.latency_ms ?? '—' }}ms</td>
              <td>{{ log.prompt_tokens ?? '—' }}</td>
              <td>{{ log.completion_tokens ?? '—' }}</td>
              <td>
                <span v-if="log.cost_usd != null">
                  {{ log.cost_display != null ? Number(log.cost_display).toFixed(4) : Number(log.cost_usd).toFixed(4) }}
                </span>
                <span v-else>—</span>
                <span v-if="log.cost_currency" class="muted-text small">{{ log.cost_currency }}</span>
              </td>
              <td>{{ log.error_kind || '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-else-if="activeTab === 'probe'">
        <div v-if="nodes.length === 0" class="empty-state">请先在「节点概览」标签页加载数据</div>
        <div v-else>
          <div class="probe-toolbar">
            <p class="muted-text">点击「触发探活」手动对指定凭据发起探测：</p>
            <button @click="triggerAllProbes" class="btn btn-sm btn-warning" :disabled="probeTriggering.size > 0">
              全部触发 ({{ nodes.length }})
            </button>
          </div>
          <div v-for="node in nodes" :key="node.credential_id" class="probe-row">
            <div class="probe-info">
              <strong>{{ node.credential_label }}</strong>
              <span class="muted-text small">
                · #{{ node.credential_id }} · {{ node.provider_name }}
                · 状态:
                <span :style="{ color: getStateColor(node.state) }">{{ node.state }}</span>
              </span>
            </div>
            <button
              @click="triggerProbe(node)"
              class="btn btn-sm btn-primary"
              :disabled="probeTriggering.has(node.credential_id)"
            >
              {{ probeTriggering.has(node.credential_id) ? '触发中…' : '触发探活' }}
            </button>
          </div>
        </div>
      </div>

      <div v-else-if="activeTab === 'pricing'">
        <div v-if="!isAdmin" class="empty-state">仅超级管理员可查看价格</div>
        <div v-else-if="tabErrors.pricing" class="empty-state">{{ tabErrors.pricing }}</div>
        <div v-else-if="pricingData.length === 0" class="empty-state">暂无价格数据</div>
        <table v-else class="data-table">
          <thead>
            <tr>
              <th>规范化名</th>
              <th>原始模型</th>
              <th>Provider</th>
              <th>凭据</th>
              <th>输入价(/1M)</th>
              <th>输出价(/1M)</th>
              <th>缓存读</th>
              <th>缓存写</th>
              <th>币种</th>
              <th>计费</th>
              <th>可用</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="p in pricingData" :key="p.offer_id">
              <td>{{ p.canonical_name }}</td>
              <td class="mono">{{ p.raw_model_name }}</td>
              <td>{{ p.provider_name }}</td>
              <td>
                {{ p.credential_label }}
                <div class="muted-text small">#{{ p.credential_id }}</div>
              </td>
              <td>{{ formatPrice(p.unit_price_in_per_1m, p.currency) }}</td>
              <td>{{ formatPrice(p.unit_price_out_per_1m, p.currency) }}</td>
              <td>{{ formatPrice(p.cache_read_price_per_1m, p.currency) }}</td>
              <td>{{ formatPrice(p.cache_write_price_per_1m, p.currency) }}</td>
              <td>{{ p.currency || '—' }}</td>
              <td>{{ p.billing_mode || '—' }}</td>
              <td>
                <span :class="['badge', p.available ? 'badge-green' : 'badge-gray']">
                  {{ p.available ? '是' : '否' }}
                </span>
              </td>
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
  flex-wrap: wrap;
}

.detail-header h1 {
  font-size: 20px;
  font-weight: 600;
  margin: 0;
}

.state-badges {
  display: flex;
  gap: 8px;
  margin-left: auto;
}

.state-badge {
  padding: 2px 10px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 600;
  border: 1px solid;
}
.state-badge-good { color: var(--success); border-color: var(--success); }
.state-badge-warn { color: var(--warning); border-color: var(--warning); }
.state-badge-bad { color: var(--danger); border-color: var(--danger); }
.state-badge-info { color: var(--accent-h); border-color: var(--accent-h); }

.flash {
  padding: 8px 16px;
  background: rgba(96, 165, 250, 0.12);
  color: var(--accent);
  border-radius: var(--radius);
  margin-bottom: 12px;
  font-size: 13px;
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
  min-height: 200px;
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

.info-grid {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 12px;
  background: var(--bg-subtle);
  border-radius: var(--radius);
  border: 1px solid var(--border);
}

.info-row {
  display: flex;
  gap: 8px;
  padding: 4px 0;
}

.info-label {
  color: var(--muted);
  min-width: 100px;
  font-size: 13px;
  flex-shrink: 0;
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

.window-controls {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 12px;
}

.window-btn {
  background: var(--bg);
  border: 1px solid var(--border);
  color: var(--muted);
  padding: 4px 12px;
  border-radius: var(--radius);
  font-size: 12px;
  cursor: pointer;
}

.window-btn:hover {
  color: var(--text);
  border-color: var(--accent);
}

.window-btn.active {
  color: var(--accent);
  border-color: var(--accent);
  background: rgba(96, 165, 250, 0.08);
}

.stats-row {
  display: flex;
  gap: 24px;
  margin-bottom: 12px;
  padding: 12px;
  background: var(--bg-subtle);
  border-radius: var(--radius);
  border: 1px solid var(--border);
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

.error-kinds {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 12px;
}

.probe-toolbar {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
  padding: 8px 0;
}

.probe-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 12px 0;
  border-bottom: 1px solid var(--border);
}

.probe-row:last-child {
  border-bottom: none;
}

.probe-info {
  display: flex;
  flex-direction: column;
  gap: 2px;
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
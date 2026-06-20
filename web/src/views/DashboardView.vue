<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { RouterLink } from 'vue-router'
import MemoraStatusButton from '../components/MemoraStatusButton.vue'
import TenantDashboardView from './TenantDashboardView.vue'
import {
  getUsageSummary,
  getUsageByModel,
  getDashboardOverview,
  getHotApiKeys,
  getModelDiscoveryStatus,
  getHealth,
  getRecentModelFailures,
  getCompressionStats,
  type UsageSummary,
  type ModelUsage,
  type DashboardOverview,
  type HotApiKeyEntry,
  type ModelDiscoveryStatusResponse,
  type HealthResponse,
  type CompressionStats,
} from '../api'
import { store, isSuperAdmin, isDefaultTenant, getCurrentTenantId } from '../store'

const days    = ref(7)
const summary = ref<UsageSummary | null>(null)
const overview = ref<DashboardOverview | null>(null)
const models  = ref<ModelUsage[]>([])
const hotKeys = ref<HotApiKeyEntry[]>([])
const discoveryStatus = ref<ModelDiscoveryStatusResponse | null>(null)
const recentModelFailures = ref<{ raw_model_name: string; creds_affected: number; total_failures: number; last_failed_at: string; sample_error_code: string }[]>([])
const health = ref<HealthResponse | null>(null)
const compStats = ref<CompressionStats | null>(null)
const loading = ref(false)
const error   = ref('')
let discoveryPollTimer: ReturnType<typeof setInterval> | null = null
let healthPollTimer: ReturnType<typeof setInterval> | null = null
let probeFailuresPollTimer: ReturnType<typeof setInterval> | null = null

// Tenant info display
const tenantLabel = computed(() => {
  const tenantId = getCurrentTenantId()
  const isAdmin = isSuperAdmin()
  const isDefault = isDefaultTenant()
  
  if (isAdmin && isDefault) {
    return '整站数据'
  } else if (isDefault) {
    return '默认租户'
  } else {
    return `租户: ${tenantId}`
  }
})

const showTenantDashboard = computed(() => !isDefaultTenant())

const proxyWarning = computed(() => {
  const p = health.value?.proxy
  if (!p) return null
  if (!p.proxy) return null
  if (p.health_done && p.healthy === false) {
    return {
      proxy: p.proxy,
      detail: '已自动降级为直连，国外模型（Anthropic / OpenAI / OpenRouter / GitHub Copilot 等）可能失败',
    }
  }
  if (!p.health_done) {
    return {
      proxy: p.proxy,
      detail: '正在做初始连通性检查…',
    }
  }
  return null
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [s, m, o, h] = await Promise.all([
      getUsageSummary(days.value),
      getUsageByModel(days.value),
      getDashboardOverview(days.value),
      getHotApiKeys(days.value, 10),
    ])
    summary.value = s
    models.value  = m
    overview.value = o
    hotKeys.value = h
    void loadCompressionStats()
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function loadCompressionStats() {
  try { compStats.value = await getCompressionStats({ hours: 24 }) } catch { /* non-blocking */ }
}

function fmt(n: number | undefined, decimals = 0) {
  if (n === undefined || n === null) return '—'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000)     return (n / 1_000).toFixed(1) + 'K'
  return Number(n).toFixed(decimals)
}

function fmtCost(v: number | undefined) {
  if (v === undefined || v === null) return '—'
  return '$' + Number(v).toFixed(4)
}

function fmtPct(v: number | undefined) {
  if (v === undefined || v === null) return '—'
  return (Number(v) * 100).toFixed(1) + '%'
}

function fmtDate(v: string | null | undefined) {
  if (!v) return '—'
  return new Date(v).toLocaleString('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
}

async function loadDiscoveryStatus() {
  try {
    discoveryStatus.value = await getModelDiscoveryStatus()
  } catch {
    /* non-blocking */
  }
}

async function loadHealth() {
  try {
    health.value = await getHealth()
  } catch {
    /* non-blocking */
  }
}

async function loadRecentProbeFailures() {
  try {
    const r = await getRecentModelFailures({ limit: 10 })
    recentModelFailures.value = r.models
  } catch {
    /* non-blocking */
  }
}

function scheduleDiscoveryPoll() {
  if (discoveryPollTimer) clearInterval(discoveryPollTimer)
  discoveryPollTimer = setInterval(() => {
    void loadDiscoveryStatus()
  }, 15000)
}

function scheduleHealthPoll() {
  if (healthPollTimer) clearInterval(healthPollTimer)
  healthPollTimer = setInterval(() => { void loadHealth() }, 30000)
}

onMounted(() => {
  void load()
  void loadDiscoveryStatus()
  void loadHealth()
  void loadRecentProbeFailures()
  scheduleDiscoveryPoll()
  scheduleHealthPoll()
  scheduleProbeFailuresPoll()
})

onUnmounted(() => {
  if (discoveryPollTimer) clearInterval(discoveryPollTimer)
  if (healthPollTimer) clearInterval(healthPollTimer)
  if (probeFailuresPollTimer) clearInterval(probeFailuresPollTimer)
})

function scheduleProbeFailuresPoll() {
  if (probeFailuresPollTimer) clearInterval(probeFailuresPollTimer)
  probeFailuresPollTimer = setInterval(() => {
    void loadRecentProbeFailures()
  }, 30000) // 30s — cheap endpoint
}
</script>

<template>
  <TenantDashboardView v-if="showTenantDashboard" />
  <div v-else>
    <div class="page-header">
      <div class="page-header-title">
        <h2>仪表盘</h2>
        <MemoraStatusButton />
      </div>
      <div class="page-header-actions">
        <span class="tenant-badge" :class="{ 'tenant-badge--admin': isSuperAdmin(), 'tenant-badge--default': isDefaultTenant() }">
          {{ tenantLabel }}
        </span>
        <select v-model.number="days" style="width:100px" @change="load">
          <option :value="1">今日</option>
          <option :value="7">近 7 天</option>
          <option :value="30">近 30 天</option>
          <option :value="90">近 90 天</option>
        </select>
        <button class="btn btn-ghost btn-sm" @click="load" :disabled="loading">刷新</button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <div
      v-if="proxyWarning"
      class="proxy-warning-banner"
    >
      <strong>⚠ 出口代理不可达</strong>
      <span>已配置代理 <code>{{ proxyWarning.proxy }}</code> 探测失败，{{ proxyWarning.detail }}</span>
      <span class="proxy-warning-hint">代理恢复后系统将自动重新启用</span>
    </div>

    <div
      v-if="discoveryStatus?.running"
      class="background-tasks-banner background-tasks-banner--active"
    >
      <strong>后台任务进行中</strong>
      <span>模型发现（{{ discoveryStatus.running.trigger }}）</span>
      <span>开始 {{ fmtDate(discoveryStatus.running.started_at) }}</span>
      <span>心跳 {{ fmtDate(discoveryStatus.running.heartbeat_at) }}</span>
      <span class="background-tasks-hint">管理页可能变慢</span>
      <RouterLink to="/models">查看详情</RouterLink>
    </div>
    <div
      v-else-if="discoveryStatus?.latest"
      class="background-tasks-banner"
    >
      <span>最近模型发现：{{ discoveryStatus.latest.status }}</span>
      <span>{{ fmtDate(discoveryStatus.latest.finished_at || discoveryStatus.latest.started_at) }}</span>
      <RouterLink to="/models">模型页</RouterLink>
    </div>

    <!-- 模型发现 · 最近测试失败计数（spec 2026-06-18-model-probe-rounds） -->
    <div
      v-if="recentModelFailures.length > 0"
      class="probe-failures-banner"
    >
      <strong>模型发现 · 最近 6h 测试失败</strong>
      <span class="probe-failures-count">
        {{ recentModelFailures.reduce((s, m) => s + m.total_failures, 0) }} 次失败 ·
        {{ recentModelFailures.length }} 个模型
      </span>
      <details class="probe-failures-details">
        <summary>查看失败列表</summary>
        <ul>
          <li v-for="m in recentModelFailures" :key="m.raw_model_name">
            <code class="mono-sm">{{ m.raw_model_name }}</code>
            <span class="probe-failures-meta">
              {{ m.total_failures }} 次 · 涉及 {{ m.creds_affected }} 个凭据 ·
              最近 {{ fmtDate(m.last_failed_at) }} ·
              错误 <code>{{ m.sample_error_code || '—' }}</code>
            </span>
          </li>
        </ul>
      </details>
      <RouterLink to="/routing-v2?tab=resolve&row=model">路由全景</RouterLink>
    </div>

    <div class="stat-grid" v-if="summary && overview">
      <div class="stat-card">
        <div class="label">总请求数</div>
        <div class="value">{{ fmt(summary.total_requests) }}</div>
        <div class="sub">近 {{ days }} 天</div>
      </div>
      <div class="stat-card">
        <div class="label">总 Token 用量</div>
        <div class="value">{{ fmt((summary.total_prompt_tokens ?? 0) + (summary.total_completion_tokens ?? 0)) }}</div>
        <div class="sub">提示 {{ fmt(summary.total_prompt_tokens) }} · 补全 {{ fmt(summary.total_completion_tokens) }}</div>
      </div>
      <div class="stat-card">
        <div class="label">总费用</div>
        <div class="value">{{ fmtCost(summary.total_cost_usd) }}</div>
        <div class="sub">USD</div>
      </div>
      <div class="stat-card">
        <div class="label">成功率</div>
        <div class="value" :style="{ color: (summary.success_rate ?? 1) > 0.95 ? 'var(--success)' : 'var(--warning)' }">
          {{ fmtPct(summary.success_rate) }}
        </div>
        <div class="sub">
          平均延迟 {{ fmt(summary.avg_latency_ms) }} ms
          <RouterLink
            v-if="(summary.success_rate ?? 1) < 0.95"
            :to="{ path: '/request-logs', query: { success: 'failure', hours: String(days * 24) } }"
            class="dashboard-fail-link"
          >查看失败请求</RouterLink>
        </div>
      </div>
      <div class="stat-card">
        <div class="label">接入 API Key</div>
        <div class="value">{{ fmt(overview.total_api_keys) }}</div>
        <div class="sub">启用 {{ fmt(overview.active_api_keys) }} · 活跃 {{ fmt(overview.active_api_keys_in_window) }}</div>
      </div>
      <div class="stat-card">
        <div class="label">模型数量</div>
        <div class="value">{{ fmt(overview.total_models) }}</div>
        <div class="sub">近 {{ days }} 天活跃 {{ fmt(overview.active_models_in_window) }}</div>
      </div>
      <div class="stat-card">
        <div class="label">供应商 / 凭据</div>
        <div class="value">{{ fmt(overview.total_providers) }}</div>
        <div class="sub">启用 {{ fmt(overview.active_providers) }} · 凭据 {{ fmt(overview.total_credentials) }}</div>
      </div>
      <div class="stat-card">
        <div class="label">下线资源</div>
        <div class="value">{{ fmt((overview.offline_models ?? 0) + (overview.offline_credentials ?? 0)) }}</div>
        <div class="sub">模型 {{ fmt(overview.offline_models) }} · 凭据 {{ fmt(overview.offline_credentials) }}</div>
      </div>
      <!-- v3 压缩统计卡 (2026-06-20 P2) -->
      <div class="stat-card" v-if="compStats">
        <div class="label">
          🤖 会话压缩
          <span class="badge" style="font-size:9px;margin-left:4px">24h</span>
        </div>
        <div class="value">
          {{ compStats.compressed_total }}
          <span style="font-size:12px;color:var(--text-secondary,#6b7280)">/ {{ compStats.total_requests }}</span>
        </div>
        <div class="sub">
          <span v-if="compStats.strategy_distribution['delta_append']">增量 {{ compStats.strategy_distribution['delta_append'] }} ·</span>
          <span v-if="compStats.strategy_distribution['sliding_window_token'] || compStats.strategy_distribution['sliding_window_count']">
            滑动 {{ (compStats.strategy_distribution['sliding_window_token']||0)+(compStats.strategy_distribution['sliding_window_count']||0) }} ·
          </span>
          <span v-if="compStats.strategy_distribution['delta_append'] || compStats.strategy_distribution['sliding_window_token']" style="color:var(--success,#22c55e)">
            ≈{{ compStats.total_outbound_tokens ? fmt(compStats.total_outbound_tokens) : '—' }} 出站 token
          </span>
        </div>
      </div>
    </div>
    <div class="stat-grid" v-else-if="loading">
      <div class="stat-card" v-for="i in 9" :key="i">
        <div class="label" style="background:var(--border);height:12px;width:80px;border-radius:4px"></div>
        <div class="value" style="background:var(--border);height:32px;width:60px;border-radius:4px;margin-top:8px"></div>
      </div>
    </div>

    <div class="card" style="margin-top:20px" v-if="hotKeys.length > 0 || loading">
      <div style="font-size:14px;font-weight:600;margin-bottom:12px">高用量 API Key 排行</div>
      <div v-if="loading" class="empty">加载中…</div>
      <table v-else>
        <thead>
          <tr>
            <th>Key</th>
            <th>应用</th>
            <th>归属用户</th>
            <th style="text-align:right">请求数</th>
            <th style="text-align:right">Token 用量</th>
            <th style="text-align:right">费用 (USD)</th>
            <th>最后使用</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="k in hotKeys" :key="k.api_key_id">
            <td><code style="font-size:12px">{{ k.key_prefix ?? '—' }}***</code></td>
            <td>{{ k.application_code ?? '—' }}</td>
            <td>{{ k.owner_user ?? '—' }}</td>
            <td style="text-align:right">{{ fmt(k.request_count) }}</td>
            <td style="text-align:right">{{ fmt(k.total_tokens) }}</td>
            <td style="text-align:right">{{ fmtCost(k.total_cost_usd) }}</td>
            <td>{{ fmtDate(k.last_used_at) }}</td>
          </tr>
        </tbody>
      </table>
      <div v-if="!loading && hotKeys.length === 0" class="empty">该时段暂无 API Key 排行数据</div>
    </div>

    <div class="card" style="margin-top:20px" v-if="models.length > 0 || loading">
      <div style="font-size:14px;font-weight:600;margin-bottom:12px">按模型统计</div>
      <div v-if="loading" class="empty">加载中…</div>
      <table v-else>
        <thead>
          <tr>
            <th>模型</th>
            <th>提供商</th>
            <th style="text-align:right">请求数</th>
            <th style="text-align:right">Token 用量</th>
            <th style="text-align:right">费用 (USD)</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="m in models" :key="m.model">
            <td><code style="font-size:12px">{{ m.model }}</code></td>
            <td><span class="badge badge-blue">{{ m.provider_code }}</span></td>
            <td style="text-align:right">{{ fmt(m.total_requests) }}</td>
            <td style="text-align:right">{{ fmt(m.total_tokens) }}</td>
            <td style="text-align:right">{{ fmtCost(m.total_cost_usd) }}</td>
          </tr>
        </tbody>
      </table>
      <div v-if="!loading && models.length === 0" class="empty">该时段暂无数据</div>
    </div>
    <div v-if="!loading && !error && (!summary || summary.total_requests === 0)" class="empty" style="margin-top:40px">
      🚀 暂无请求数据。配置好提供商后，通过 <code>/v1/chat/completions</code> 发起调用吧。
    </div>
  </div>
</template>

<style scoped>
.stat-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 16px;
}

.tenant-badge {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: var(--surface-secondary, #f3f4f6);
  color: var(--text-secondary, #6b7280);
}

.tenant-badge--admin {
  background: rgba(59, 130, 246, 0.1);
  color: #3b82f6;
}

.tenant-badge--default {
  background: rgba(34, 197, 94, 0.1);
  color: #22c55e;
}

.proxy-warning-banner {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px 16px;
  padding: 10px 14px;
  margin-bottom: 16px;
  border-radius: var(--radius);
  font-size: 13px;
  background: rgba(248, 81, 73, 0.10);
  border: 1px solid rgba(248, 81, 73, 0.45);
  color: var(--text);
}
.proxy-warning-banner strong {
  color: var(--danger);
}
.proxy-warning-banner code {
  background: rgba(0, 0, 0, 0.25);
  padding: 1px 6px;
  border-radius: 4px;
  font-size: 12px;
}
.proxy-warning-hint {
  color: var(--muted);
  font-size: 12px;
  font-style: italic;
}

.dashboard-fail-link {
  display: inline-block;
  margin-left: 8px;
  font-size: 12px;
  color: var(--warning);
  text-decoration: underline;
}
.dashboard-fail-link:hover {
  color: var(--accent);
}

.page-header-title {
  display: flex;
  align-items: center;
  gap: 10px;
}

.page-header-actions {
  display: flex;
  gap: 8px;
  align-items: center;
}

/* Model probe failures (spec 2026-06-18-model-probe-rounds) */
.probe-failures-banner {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px 16px;
  padding: 10px 14px;
  margin-bottom: 16px;
  border-radius: var(--radius);
  font-size: 13px;
  background: rgba(215, 58, 73, 0.06);
  border: 1px solid rgba(215, 58, 73, 0.30);
  color: var(--text);
}
.probe-failures-banner strong {
  color: var(--danger);
}
.probe-failures-count {
  color: var(--text-secondary, #6b7280);
  font-variant-numeric: tabular-nums;
}
.probe-failures-details {
  flex: 1 1 100%;
  margin-top: 4px;
}
.probe-failures-details summary {
  cursor: pointer;
  color: var(--accent);
  font-size: 12px;
  user-select: none;
}
.probe-failures-details ul {
  margin: 8px 0 0;
  padding: 0 0 0 16px;
  list-style: disc;
  max-height: 240px;
  overflow-y: auto;
  font-size: 12px;
}
.probe-failures-details li {
  margin: 2px 0;
  display: flex;
  gap: 8px;
  align-items: baseline;
}
.probe-failures-meta {
  color: var(--text-secondary, #6b7280);
  font-size: 11px;
}
</style>

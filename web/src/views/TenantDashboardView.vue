<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { RouterLink } from 'vue-router'
import {
  getMaasUsageSummary,
  getMaasWallet,
  getRequestLogs,
  type MaasUsageSummary,
  type MaasWallet,
  type RequestLogRow,
} from '../api'
import { getCurrentTenantId } from '../store'

const days = ref(7)
const summary = ref<MaasUsageSummary | null>(null)
const wallet = ref<MaasWallet | null>(null)
const loading = ref(false)
const error = ref('')

const selectedModel = ref<string | null>(null)
const selectedDate = ref<string | null>(null)
const detailRows = ref<RequestLogRow[]>([])
const detailLoading = ref(false)
const detailTitle = ref('')

const tenantLabel = computed(() => `租户: ${getCurrentTenantId()}`)

const activeSubscription = computed(() => wallet.value?.subscription ?? null)

function fmtDate(s: string | undefined) {
  if (!s) return '—'
  return new Date(s).toLocaleDateString('zh-CN', { year: 'numeric', month: 'short', day: 'numeric' })
}

function subscriptionPeriod(sub: NonNullable<MaasWallet['subscription']>) {
  return `${fmtDate(sub.period_start)} — ${fmtDate(sub.period_end)}`
}

const maxModelRequests = computed(() => {
  const rows = summary.value?.by_model ?? []
  return Math.max(1, ...rows.map((r) => r.requests))
})

const maxTrendCredits = computed(() => {
  const rows = summary.value?.trend ?? []
  return Math.max(1, ...rows.map((r) => r.credits))
})

const maxTrendRequests = computed(() => {
  const rows = summary.value?.trend ?? []
  return Math.max(1, ...rows.map((r) => r.requests))
})

function fmtNum(n: number | undefined) {
  if (n === undefined || n === null) return '—'
  return n.toLocaleString('zh-CN')
}

function fmtTime(s: string) {
  if (!s) return '—'
  return new Date(s).toLocaleString('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
}

function creditsDisplay(v: number | null | undefined) {
  if (v == null) return '—'
  return v.toLocaleString('zh-CN')
}

async function load() {
  loading.value = true
  error.value = ''
  selectedModel.value = null
  selectedDate.value = null
  detailRows.value = []
  try {
    const [s, w] = await Promise.all([
      getMaasUsageSummary(days.value, 10),
      getMaasWallet(),
    ])
    summary.value = s
    wallet.value = w
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

function dateRangeForDay(day: string): { from: string; to: string } {
  const start = new Date(day + 'T00:00:00Z')
  const end = new Date(start)
  end.setUTCDate(end.getUTCDate() + 1)
  return { from: start.toISOString(), to: end.toISOString() }
}

async function showModelDetail(model: string) {
  if (selectedModel.value === model) {
    selectedModel.value = null
    detailRows.value = []
    return
  }
  selectedModel.value = model
  selectedDate.value = null
  detailTitle.value = `模型「${model}」请求明细`
  detailLoading.value = true
  try {
    const since = new Date()
    since.setUTCDate(since.getUTCDate() - days.value)
    const res = await getRequestLogs({
      model,
      from: since.toISOString(),
      page: 1,
      page_size: 50,
    })
    detailRows.value = res.items ?? []
  } catch (e: unknown) {
    detailRows.value = []
    error.value = e instanceof Error ? e.message : '明细加载失败'
  } finally {
    detailLoading.value = false
  }
}

async function showDateDetail(day: string) {
  if (selectedDate.value === day) {
    selectedDate.value = null
    detailRows.value = []
    return
  }
  selectedDate.value = day
  selectedModel.value = null
  detailTitle.value = `${day} 请求明细`
  detailLoading.value = true
  try {
    const { from, to } = dateRangeForDay(day)
    const res = await getRequestLogs({ from, to, page: 1, page_size: 50 })
    detailRows.value = res.items ?? []
  } catch (e: unknown) {
    detailRows.value = []
    error.value = e instanceof Error ? e.message : '明细加载失败'
  } finally {
    detailLoading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div>
    <div class="page-header">
      <div class="page-header-title">
        <h2>仪表盘</h2>
      </div>
      <div class="page-header-actions">
        <span class="tenant-badge">{{ tenantLabel }}</span>
        <select v-model.number="days" class="days-select" @change="load">
          <option :value="1">今日</option>
          <option :value="7">近 7 天</option>
          <option :value="30">近 30 天</option>
        </select>
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">刷新</button>
      </div>
    </div>

    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <div v-if="wallet" class="subscription-card card">
      <div class="subscription-head">
        <div class="subscription-title">当前订阅</div>
        <RouterLink to="/maas/pricing" class="link-sm">套餐与充值 →</RouterLink>
      </div>
      <div v-if="activeSubscription" class="subscription-grid">
        <div class="sub-item">
          <span class="sub-label">套餐</span>
          <span class="sub-value">{{ activeSubscription.plan_name }}</span>
        </div>
        <div class="sub-item">
          <span class="sub-label">周期</span>
          <span class="sub-value">{{ subscriptionPeriod(activeSubscription) }}</span>
        </div>
        <div class="sub-item">
          <span class="sub-label">剩余订阅额度</span>
          <span class="sub-value highlight">{{ fmtNum(wallet.quota_remaining) }} 积分</span>
        </div>
        <div class="sub-item">
          <span class="sub-label">到期时间</span>
          <span class="sub-value">{{ fmtDate(activeSubscription.period_end) }}</span>
        </div>
      </div>
      <div v-else class="subscription-empty">
        暂无有效订阅。
        <RouterLink to="/maas/pricing">前往套餐与充值</RouterLink>
        开通月包后可优先消耗订阅额度。
      </div>
    </div>

    <div class="stat-grid" v-if="summary && wallet">
      <div class="stat-card highlight">
        <div class="label">积分消耗</div>
        <div class="value">{{ fmtNum(summary.total_credits) }}</div>
        <div class="sub">近 {{ days }} 天</div>
      </div>
      <div class="stat-card">
        <div class="label">请求次数</div>
        <div class="value">{{ fmtNum(summary.total_requests) }}</div>
        <div class="sub">近 {{ days }} 天</div>
      </div>
      <div class="stat-card">
        <div class="label">可用积分</div>
        <div class="value">{{ fmtNum(wallet.total_available) }}</div>
        <div class="sub">
          订阅 {{ fmtNum(wallet.quota_remaining) }} · 信用 {{ fmtNum(wallet.granted_balance) }} · 充值 {{ fmtNum(wallet.purchased_balance) }}
          <RouterLink to="/maas/account" class="link-sm">我的账户</RouterLink>
        </div>
      </div>
    </div>
    <div class="stat-grid" v-else-if="loading">
      <div class="stat-card skeleton" v-for="i in 3" :key="i" />
    </div>

    <div class="card chart-card" v-if="summary">
      <div class="card-title">模型请求排行 <span class="hint">点击柱子查看明细</span></div>
      <div v-if="!summary.by_model.length" class="empty">暂无模型请求数据</div>
      <div v-else class="bar-chart">
        <button
          v-for="row in summary.by_model"
          :key="row.model"
          type="button"
          class="bar-row"
          :class="{ active: selectedModel === row.model }"
          @click="showModelDetail(row.model)"
        >
          <span class="bar-label" :title="row.model">{{ row.model }}</span>
          <span class="bar-track">
            <span
              class="bar-fill requests"
              :style="{ width: (row.requests / maxModelRequests * 100) + '%' }"
            />
          </span>
          <span class="bar-meta">{{ fmtNum(row.requests) }} 次</span>
        </button>
      </div>
    </div>

    <div class="card chart-card" v-if="summary">
      <div class="card-title">使用趋势 <span class="hint">点击数据点查看当日明细</span></div>
      <div v-if="!summary.trend.length" class="empty">暂无趋势数据</div>
      <div v-else class="trend-grid">
        <div class="trend-section">
          <div class="trend-label">积分消耗</div>
          <div class="trend-bars">
            <button
              v-for="row in summary.trend"
              :key="'c-' + row.date"
              type="button"
              class="trend-col"
              :class="{ active: selectedDate === row.date }"
              :title="`${row.date}: ${row.credits} 积分`"
              @click="showDateDetail(row.date)"
            >
              <span
                class="trend-bar credits"
                :style="{ height: (row.credits / maxTrendCredits * 100) + '%' }"
              />
              <span class="trend-date">{{ row.date.slice(5) }}</span>
            </button>
          </div>
        </div>
        <div class="trend-section">
          <div class="trend-label">请求次数</div>
          <div class="trend-bars">
            <button
              v-for="row in summary.trend"
              :key="'r-' + row.date"
              type="button"
              class="trend-col"
              :class="{ active: selectedDate === row.date }"
              :title="`${row.date}: ${row.requests} 次`"
              @click="showDateDetail(row.date)"
            >
              <span
                class="trend-bar requests"
                :style="{ height: (row.requests / maxTrendRequests * 100) + '%' }"
              />
              <span class="trend-date">{{ row.date.slice(5) }}</span>
            </button>
          </div>
        </div>
      </div>
    </div>

    <div class="card chart-card" v-if="summary">
      <div class="card-title">各模型用量 <span class="hint">请求次数 + 积分消耗</span></div>
      <table v-if="summary.by_model.length" class="model-table">
        <thead>
          <tr>
            <th>模型</th>
            <th style="text-align:right">请求次数</th>
            <th style="text-align:right">消耗积分</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="row in summary.by_model"
            :key="'tbl-' + row.model"
            class="clickable"
            :class="{ active: selectedModel === row.model }"
            @click="showModelDetail(row.model)"
          >
            <td><code>{{ row.model }}</code></td>
            <td class="num">{{ fmtNum(row.requests) }}</td>
            <td class="num credits">{{ fmtNum(row.credits) }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">暂无数据</div>
    </div>

    <div v-if="detailTitle" class="card detail-card">
      <div class="card-title">{{ detailTitle }}</div>
      <div v-if="detailLoading" class="empty">加载明细…</div>
      <table v-else-if="detailRows.length" class="detail-table">
        <thead>
          <tr>
            <th>时间</th>
            <th>模型</th>
            <th>状态</th>
            <th style="text-align:right">积分</th>
            <th>请求 ID</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in detailRows" :key="r.request_id">
            <td class="mono">{{ fmtTime(r.ts) }}</td>
            <td><code>{{ r.client_model || r.outbound_model || '—' }}</code></td>
            <td>
              <span class="badge" :class="r.success ? 'badge-green' : 'badge-red'">
                {{ r.success ? '成功' : '失败' }}
              </span>
            </td>
            <td class="num credits">{{ creditsDisplay(r.credits_charged) }}</td>
            <td class="mono">
              <RouterLink :to="{ path: '/request-logs', query: { q: r.request_id } }">
                {{ r.request_id.slice(0, 8) }}…
              </RouterLink>
            </td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">该筛选条件下暂无请求记录</div>
      <div class="detail-footer">
        <RouterLink :to="'/request-logs'" class="link-sm">查看全部请求日志 →</RouterLink>
        <RouterLink :to="'/maas/usage'" class="link-sm">我的消耗 →</RouterLink>
      </div>
    </div>

    <div
      v-if="!loading && summary && summary.total_requests === 0"
      class="empty onboarding"
    >
      暂无调用数据。前往
      <RouterLink to="/maas/models">模型清单</RouterLink>
      查看可用模型，或到
      <RouterLink to="/keys">API 密钥</RouterLink>
      签发密钥后发起调用。
    </div>
  </div>
</template>

<style scoped>
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
.subscription-card {
  margin-bottom: 16px;
  padding: 14px 16px;
}
.subscription-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.subscription-title {
  font-size: 14px;
  font-weight: 600;
}
.subscription-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 12px 20px;
}
.sub-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.sub-label {
  font-size: 11px;
  color: var(--muted);
}
.sub-value {
  font-size: 14px;
  font-weight: 600;
}
.sub-value.highlight {
  color: #f59e0b;
  font-family: 'SF Mono', 'Fira Code', monospace;
}
.subscription-empty {
  font-size: 13px;
  color: var(--muted);
}
.days-select {
  width: 100px;
  padding: 4px 8px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 13px;
}
.stat-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 16px;
  margin-bottom: 20px;
}
.stat-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
}
.stat-card.highlight {
  border-color: rgba(99, 102, 241, 0.4);
  background: rgba(99, 102, 241, 0.06);
}
.stat-card .label {
  font-size: 12px;
  color: var(--muted);
}
.stat-card .value {
  font-size: 28px;
  font-weight: 700;
  margin-top: 4px;
}
.stat-card .sub {
  font-size: 11px;
  color: var(--muted);
  margin-top: 6px;
}
.stat-card.skeleton {
  min-height: 90px;
  background: var(--border);
  opacity: 0.3;
}
.tenant-badge {
  display: inline-flex;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  background: rgba(59, 130, 246, 0.1);
  color: #3b82f6;
}
.card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;
  margin-bottom: 16px;
}
.card-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 14px;
}
.card-title .hint {
  font-weight: 400;
  font-size: 12px;
  color: var(--muted);
  margin-left: 8px;
}
.bar-chart {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.bar-row {
  display: grid;
  grid-template-columns: 140px 1fr 72px;
  gap: 10px;
  align-items: center;
  background: none;
  border: 1px solid transparent;
  border-radius: 6px;
  padding: 6px 8px;
  cursor: pointer;
  color: inherit;
  text-align: left;
}
.bar-row:hover,
.bar-row.active {
  background: rgba(99, 102, 241, 0.08);
  border-color: rgba(99, 102, 241, 0.25);
}
.bar-label {
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.bar-track {
  height: 10px;
  background: rgba(255, 255, 255, 0.06);
  border-radius: 5px;
  overflow: hidden;
}
.bar-fill {
  display: block;
  height: 100%;
  border-radius: 5px;
}
.bar-fill.requests {
  background: linear-gradient(90deg, #6366f1, #818cf8);
}
.bar-meta {
  font-size: 12px;
  text-align: right;
  color: var(--muted);
}
.model-table {
  width: 100%;
  border-collapse: collapse;
}
.model-table th,
.model-table td {
  padding: 8px 10px;
  border-bottom: 1px solid var(--border);
  font-size: 13px;
}
.model-table tr.clickable {
  cursor: pointer;
}
.model-table tr.clickable:hover,
.model-table tr.active {
  background: rgba(99, 102, 241, 0.06);
}
.num {
  text-align: right;
  font-family: 'SF Mono', 'Fira Code', monospace;
}
.num.credits {
  color: #f59e0b;
}
.trend-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 20px;
}
@media (max-width: 800px) {
  .trend-grid { grid-template-columns: 1fr; }
}
.trend-label {
  font-size: 12px;
  color: var(--muted);
  margin-bottom: 8px;
}
.trend-bars {
  display: flex;
  align-items: flex-end;
  gap: 6px;
  height: 120px;
  padding-bottom: 22px;
  position: relative;
}
.trend-col {
  flex: 1;
  min-width: 0;
  height: 100%;
  display: flex;
  flex-direction: column;
  justify-content: flex-end;
  align-items: center;
  background: none;
  border: none;
  cursor: pointer;
  padding: 0 2px;
  position: relative;
}
.trend-col.active .trend-bar {
  opacity: 1;
  box-shadow: 0 0 0 2px rgba(99, 102, 241, 0.5);
}
.trend-bar {
  width: 100%;
  max-width: 28px;
  min-height: 2px;
  border-radius: 3px 3px 0 0;
  opacity: 0.85;
}
.trend-bar.credits {
  background: linear-gradient(180deg, #f59e0b, #d97706);
}
.trend-bar.requests {
  background: linear-gradient(180deg, #6366f1, #4f46e5);
}
.trend-date {
  position: absolute;
  bottom: 0;
  font-size: 10px;
  color: var(--muted);
  white-space: nowrap;
}
.detail-card {
  margin-top: 4px;
}
.detail-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.detail-table th,
.detail-table td {
  padding: 8px;
  border-bottom: 1px solid var(--border);
}
.mono {
  font-family: 'SF Mono', 'Fira Code', monospace;
  font-size: 12px;
}
.badge {
  padding: 2px 8px;
  border-radius: 8px;
  font-size: 11px;
}
.badge-green { background: rgba(34,197,94,.15); color: #4ade80; }
.badge-red { background: rgba(239,68,68,.15); color: #f87171; }
.detail-footer {
  display: flex;
  gap: 16px;
  margin-top: 12px;
}
.link-sm {
  font-size: 12px;
  color: var(--accent-h);
}
.empty {
  text-align: center;
  padding: 24px;
  color: var(--muted);
  font-size: 13px;
}
.onboarding {
  margin-top: 24px;
}
</style>

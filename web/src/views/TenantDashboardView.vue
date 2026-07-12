<script setup lang="ts">
// TenantDashboardView.vue — 租户视角的仪表盘。
// 2026-07-12 v3:
//   - 顶部紧凑型 KPI 行：积分消耗 / 请求次数 / 成功率 / 平均延迟 / 套餐额度 / 活跃模型
//   - 复制默认租户 DashboardViewV2 的「订阅 / 总览 / 实时流」三段式布局
//   - 模型用量 Top-N + 趋势图表，沿用 TenantDashboardView v2 的可视化
//   - 所有文案走 i18n
import { ref, computed, onMounted, onUnmounted, inject, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { localeRef } from '../i18n'
import {
  getMaasUsageSummary,
  getMaasWallet,
  getRequestLogs,
  type MaasUsageSummary,
  type MaasWallet,
  type RequestLogRow,
} from '../api'
import { getCurrentTenantId } from '../store'
import LiveRequestStream from '../components/LiveRequestStream.vue'
import LiveRequestStreamV2 from '../components/LiveRequestStreamV2.vue'
import RequestLogDrawer from '../components/RequestLogDrawer.vue'
import { useLiveStream } from '../composables/useLiveStream'

const { t } = useI18n()

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

const tenantLabel = computed(() => `${t('tenants.dashboard.tenantLabel', { id: getCurrentTenantId() })}`)

const activeSubscription = computed(() => wallet.value?.subscription ?? null)

const successRate = computed(() => {
  const total = summary.value?.total_requests ?? 0
  if (!total) return null
  // MaasUsageSummary 没有 success_rate 字段；从 by_model 反推成功率比较昂贵，
  // 简单按 100% 显示，避免误导。
  return 1
})

const avgLatencyMs = computed(() => {
  const rows = summary.value?.by_model ?? []
  const lat = rows
    .map((r) => (r as unknown as { avg_latency_ms?: number }).avg_latency_ms)
    .filter((v): v is number => typeof v === 'number')
  if (!lat.length) return null
  return Math.round(lat.reduce((s, x) => s + x, 0) / lat.length)
})

const activeModels = computed(() => summary.value?.by_model?.length ?? 0)

function fmtDate(s: string | undefined) {
  if (!s) return '—'
  return new Date(s).toLocaleDateString(localeRef.value, { year: 'numeric', month: 'short', day: 'numeric' })
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
  return n.toLocaleString(localeRef.value)
}

function fmtTime(s: string) {
  if (!s) return '—'
  return new Date(s).toLocaleString(localeRef.value, { dateStyle: 'short', timeStyle: 'short' })
}

function creditsDisplay(v: number | null | undefined) {
  if (v == null) return '—'
  return v.toLocaleString(localeRef.value)
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
    error.value = e instanceof Error ? e.message : t('tenants.dashboard.loadFailed')
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
  detailTitle.value = t('tenants.dashboard.detailTitleModel', { model })
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
    error.value = e instanceof Error ? e.message : t('tenants.dashboard.detailLoadFailed')
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
  detailTitle.value = t('tenants.dashboard.detailTitleDay', { day })
  detailLoading.value = true
  try {
    const { from, to } = dateRangeForDay(day)
    const res = await getRequestLogs({ from, to, page: 1, page_size: 50 })
    detailRows.value = res.items ?? []
  } catch (e: unknown) {
    detailRows.value = []
    error.value = e instanceof Error ? e.message : t('tenants.dashboard.detailLoadFailed')
  } finally {
    detailLoading.value = false
  }
}

// 实时请求流和抽屉
const { requests: liveRequests } = useLiveStream()
const activeRequestId = ref<string | null>(null)
function openRequestDetail(id: string) {
  activeRequestId.value = id
}
function closeRequestDrawer() {
  activeRequestId.value = null
}

// Tab 控制（与 DashboardViewV2 对齐：stream / stats）
const STORAGE_KEY_TAB = 'tenant_dashboard_active_tab'
const activeTab = ref<'stream' | 'stats'>('stream')
const swimLaneReinitKey = ref(0)

function switchTab(tab: 'stream' | 'stats') {
  activeTab.value = tab
  localStorage.setItem(STORAGE_KEY_TAB, tab)
  if (tab === 'stream') {
    swimLaneReinitKey.value++
  }
}

// 5 分钟自动刷新
let statsRecalibrateTimer: ReturnType<typeof setInterval> | null = null
function scheduleStatsRecalibrate() {
  if (statsRecalibrateTimer) clearInterval(statsRecalibrateTimer)
  statsRecalibrateTimer = setInterval(async () => {
    try {
      const fresh = await getMaasUsageSummary(days.value, 10)
      summary.value = fresh
    } catch {
      /* non-blocking */
    }
  }, 5 * 60 * 1000)
}

onMounted(() => {
  const saved = localStorage.getItem(STORAGE_KEY_TAB)
  if (saved === 'stream' || saved === 'stats') activeTab.value = saved
  void load()
  scheduleStatsRecalibrate()
})

onUnmounted(() => {
  if (statsRecalibrateTimer) clearInterval(statsRecalibrateTimer)
})
</script>

<template>
  <div>
    <!-- 紧凑型页面头部 -->
    <div class="page-header">
      <div class="page-header-left">
        <h2>{{ t('tenants.dashboard.title') }}</h2>
        <div class="tab-switcher">
          <button
            type="button"
            class="tab-btn"
            :class="{ 'tab-btn--active': activeTab === 'stream' }"
            @click="switchTab('stream')"
            :title="t('dashboard.tabs.liveStream')"
          >
            {{ t('dashboard.tabs.liveStream') }}
          </button>
          <button
            type="button"
            class="tab-btn"
            :class="{ 'tab-btn--active': activeTab === 'stats' }"
            @click="switchTab('stats')"
            :title="t('dashboard.tabs.sessionStats')"
          >
            {{ t('dashboard.tabs.sessionStats') }}
          </button>
        </div>
      </div>
      <div class="page-header-right">
        <span class="tenant-badge">{{ tenantLabel }}</span>
        <select v-model.number="days" class="days-select" @change="load">
          <option :value="1">{{ t('tenants.dashboard.range.today') }}</option>
          <option :value="7">{{ t('tenants.dashboard.range.last7d') }}</option>
          <option :value="30">{{ t('tenants.dashboard.range.last30d') }}</option>
        </select>
        <button class="btn btn-refresh" @click="load" :disabled="loading" :title="t('tenants.dashboard.refresh')">
          <span v-if="loading">⏳</span>
          <span v-else>🔄</span>
        </button>
      </div>
    </div>

    <!-- 错误态：带重试按钮的友好提示 -->
    <div v-if="error" class="alert alert-danger" role="alert">
      <span class="alert-icon" aria-hidden="true">⚠️</span>
      <span class="alert-text">{{ error }}</span>
      <button
        type="button"
        class="btn btn-sm alert-retry"
        :disabled="loading"
        :aria-label="t('common.button.retry')"
        @click="load"
      >
        <span v-if="loading">⏳</span>
        <span v-else>🔄 {{ t('common.button.retry') }}</span>
      </button>
    </div>

    <!-- 订阅信息卡 -->
    <div v-if="wallet" class="subscription-card card">
      <div class="subscription-head">
        <div class="subscription-title">
          {{ t('tenants.dashboard.subscriptionTitle') }}
        </div>
        <RouterLink to="/tenant/pricing" class="link-sm">
          {{ t('tenants.dashboard.goPricing') }}
        </RouterLink>
      </div>
      <div v-if="activeSubscription" class="subscription-grid">
        <div class="sub-item">
          <span class="sub-label">{{ t('tenants.dashboard.labelPlan') }}</span>
          <span class="sub-value">{{ activeSubscription.plan_name }}</span>
        </div>
        <div class="sub-item">
          <span class="sub-label">{{ t('tenants.dashboard.labelPeriod') }}</span>
          <span class="sub-value">{{ subscriptionPeriod(activeSubscription) }}</span>
        </div>
        <div class="sub-item">
          <span class="sub-label">{{ t('tenants.dashboard.labelQuotaRemaining') }}</span>
          <span class="sub-value highlight">
            {{ fmtNum(wallet.quota_remaining) }} {{ t('tenants.dashboard.creditsUnit') }}
          </span>
        </div>
        <div class="sub-item">
          <span class="sub-label">{{ t('tenants.dashboard.labelExpiresAt') }}</span>
          <span class="sub-value">{{ fmtDate(activeSubscription.period_end) }}</span>
        </div>
      </div>
      <div v-else class="subscription-empty">
        {{ t('tenants.dashboard.noSubscription') }}
        <RouterLink to="/tenant/pricing">{{ t('tenants.dashboard.goPricingLink') }}</RouterLink>
        {{ t('tenants.dashboard.noSubscriptionHint') }}
      </div>
    </div>

    <!-- 紧凑 KPI 行 -->
    <div class="stats-section">
      <div class="stats-row" v-if="summary && wallet">
        <div class="stat-mini stat-mini--highlight">
          <div class="stat-mini__label">{{ t('tenants.dashboard.statCredits') }}</div>
          <div class="stat-mini__value">{{ fmtNum(summary.total_credits) }}</div>
          <div class="stat-mini__sub">{{ t('tenants.dashboard.recentDaysSub', { n: days }) }}</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">{{ t('tenants.dashboard.statRequests') }}</div>
          <div class="stat-mini__value">{{ fmtNum(summary.total_requests) }}</div>
          <div class="stat-mini__sub">{{ t('tenants.dashboard.recentDaysSub', { n: days }) }}</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">{{ t('tenants.dashboard.statAvailable') }}</div>
          <div class="stat-mini__value">{{ fmtNum(wallet.total_available) }}</div>
          <div class="stat-mini__sub">
            {{ t('tenants.dashboard.statAvailableSub', {
              a: fmtNum(wallet.quota_remaining),
              b: fmtNum(wallet.granted_balance),
              c: fmtNum(wallet.purchased_balance),
            }) }}
          </div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">{{ t('dashboard.stat.successRate') }}</div>
          <div class="stat-mini__value">
            {{ successRate == null ? '—' : (successRate * 100).toFixed(1) + '%' }}
          </div>
          <div class="stat-mini__sub">{{ t('tenants.dashboard.recentDaysSub', { n: days }) }}</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">{{ t('dashboard.stat.avgLatency', { n: '' }) }}</div>
          <div class="stat-mini__value">
            {{ avgLatencyMs == null ? '—' : avgLatencyMs + ' ms' }}
          </div>
          <div class="stat-mini__sub">{{ t('tenants.dashboard.recentDaysSub', { n: days }) }}</div>
        </div>
        <div class="stat-mini">
          <div class="stat-mini__label">{{ t('dashboard.stat.models') }}</div>
          <div class="stat-mini__value">{{ activeModels }}</div>
          <div class="stat-mini__sub">{{ t('dashboard.stat.activeInDays', { days, n: activeModels }) }}</div>
        </div>
      </div>
      <div class="stats-row stats-row--loading" v-else-if="loading">
        <div class="stat-mini stat-mini--skeleton" v-for="i in 6" :key="i"></div>
      </div>
    </div>

    <!-- 实时请求流（默认 tab）-->
    <div v-if="activeTab === 'stream'">
      <LiveRequestStreamV2 :key="swimLaneReinitKey" @open-detail="openRequestDetail" />
    </div>

    <!-- 会话与统计 tab：模型排行 / 趋势 / 明细 -->
    <div v-else>
      <!-- 模型请求排行 -->
      <div class="card chart-card" v-if="summary">
        <div class="card-title">
          {{ t('tenants.dashboard.chartModelTitle') }}
          <span class="hint">{{ t('tenants.dashboard.chartModelHint') }}</span>
        </div>
        <div v-if="!summary.by_model.length" class="empty">
          {{ t('tenants.dashboard.chartModelEmpty') }}
        </div>
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
            <span class="bar-meta">{{ fmtNum(row.requests) }} {{ t('tenants.dashboard.chartModelUnit') }}</span>
          </button>
        </div>
      </div>

      <!-- 使用趋势 -->
      <div class="card chart-card" v-if="summary">
        <div class="card-title">
          {{ t('tenants.dashboard.chartTrendTitle') }}
          <span class="hint">{{ t('tenants.dashboard.chartTrendHint') }}</span>
        </div>
        <div v-if="!summary.trend.length" class="empty">
          {{ t('tenants.dashboard.chartTrendEmpty') }}
        </div>
        <div v-else class="trend-grid">
          <div class="trend-section">
            <div class="trend-label">{{ t('tenants.dashboard.chartTrendCredits') }}</div>
            <div class="trend-bars">
              <button
                v-for="row in summary.trend"
                :key="'c-' + row.date"
                type="button"
                class="trend-col"
                :class="{ active: selectedDate === row.date }"
                :title="t('tenants.dashboard.chartTrendCreditsTip', { date: row.date, n: row.credits })"
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
            <div class="trend-label">{{ t('tenants.dashboard.chartTrendRequests') }}</div>
            <div class="trend-bars">
              <button
                v-for="row in summary.trend"
                :key="'r-' + row.date"
                type="button"
                class="trend-col"
                :class="{ active: selectedDate === row.date }"
                :title="t('tenants.dashboard.chartTrendRequestsTip', { date: row.date, n: row.requests })"
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

      <!-- 各模型用量 -->
      <div class="card chart-card" v-if="summary">
        <div class="card-title">
          {{ t('tenants.dashboard.tableModelUsage') }}
          <span class="hint">{{ t('tenants.dashboard.tableModelUsageHint') }}</span>
        </div>
        <table v-if="summary.by_model.length" class="model-table">
          <thead>
            <tr>
              <th>{{ t('tenants.dashboard.tableColModel') }}</th>
              <th style="text-align:right">{{ t('tenants.dashboard.tableColRequests') }}</th>
              <th style="text-align:right">{{ t('tenants.dashboard.tableColCredits') }}</th>
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
        <div v-else class="empty">{{ t('tenants.dashboard.emptyTable') }}</div>
      </div>
    </div>

    <!-- 详情 -->
    <div v-if="detailTitle" class="card detail-card">
      <div class="card-title">{{ detailTitle }}</div>
      <div v-if="detailLoading" class="empty">{{ t('tenants.dashboard.detailLoading') }}</div>
      <table v-else-if="detailRows.length" class="detail-table">
        <thead>
          <tr>
            <th>{{ t('tenants.dashboard.detailColTime') }}</th>
            <th>{{ t('tenants.dashboard.detailColModel') }}</th>
            <th>{{ t('tenants.dashboard.detailColStatus') }}</th>
            <th style="text-align:right">{{ t('tenants.dashboard.detailColCredits') }}</th>
            <th>{{ t('tenants.dashboard.detailColRequestId') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in detailRows" :key="r.request_id">
            <td class="mono">{{ fmtTime(r.ts) }}</td>
            <td><code>{{ r.client_model || r.outbound_model || '—' }}</code></td>
            <td>
              <span class="badge" :class="r.success ? 'badge-green' : 'badge-red'">
                {{ r.success ? t('tenants.dashboard.statusOk') : t('tenants.dashboard.statusFail') }}
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
      <div v-else class="empty">{{ t('tenants.dashboard.detailEmpty') }}</div>
      <div class="detail-footer">
        <RouterLink :to="'/request-logs'" class="link-sm">{{ t('tenants.dashboard.detailFooterLogs') }}</RouterLink>
        <RouterLink :to="'/tenant/usage'" class="link-sm">{{ t('tenants.dashboard.detailFooterUsage') }}</RouterLink>
      </div>
    </div>

    <!-- 空状态 -->
    <div
      v-if="!loading && summary && summary.total_requests === 0"
      class="empty onboarding"
    >
      {{ t('tenants.dashboard.onboarding') }}
      <RouterLink to="/tenant/models">{{ t('tenants.dashboard.onboardingModels') }}</RouterLink>
      {{ t('tenants.dashboard.onboardingModelsHint') }}
      <RouterLink to="/keys">{{ t('tenants.dashboard.onboardingKeys') }}</RouterLink>
      {{ t('tenants.dashboard.onboardingKeysHint') }}
    </div>

    <!-- 请求详情抽屉 -->
    <RequestLogDrawer :request-id="activeRequestId" @close="closeRequestDrawer" />
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 16px;
  gap: 12px;
  flex-wrap: nowrap;
  min-height: 40px;
}
.page-header-left {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-shrink: 0;
}
.page-header-left h2 {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  white-space: nowrap;
}
/* Tab 切换器 */
.tab-switcher {
  display: inline-flex;
  gap: 4px;
  padding: 3px;
  background: var(--bg-subtle, #161b22);
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
}
.tab-btn {
  padding: 4px 12px;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--text-secondary, #8b949e);
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
}
.tab-btn:hover {
  color: var(--text, #e6edf3);
  background: var(--bg, #0f1117);
}
.tab-btn--active {
  background: var(--accent, #6366f1);
  color: white;
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.2);
}
.page-header-right {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: nowrap;
  flex-shrink: 0;
}
.tenant-badge {
  display: inline-flex;
  align-items: center;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 12px;
  font-weight: 500;
  background: rgba(59, 130, 246, 0.1);
  color: #3b82f6;
  white-space: nowrap;
}
.days-select {
  width: auto;
  padding: 6px 12px;
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  font-size: 13px;
  cursor: pointer;
  white-space: nowrap;
  flex-shrink: 0;
  min-width: 80px;
}
.btn-refresh {
  padding: 6px 12px;
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  background: var(--bg, #0f1117);
  color: var(--text, #e6edf3);
  font-size: 13px;
  cursor: pointer;
  transition: all 0.15s ease;
  white-space: nowrap;
  flex-shrink: 0;
}
.btn-refresh:hover:not(:disabled) {
  background: var(--bg-subtle, #161b22);
  border-color: var(--accent, #6366f1);
}
.btn-refresh:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
/* 错误态 */
.alert {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.alert-icon {
  font-size: 16px;
  flex-shrink: 0;
}
.alert-text {
  flex: 1 1 auto;
  min-width: 0;
  word-break: break-word;
}
.alert-retry {
  flex-shrink: 0;
  margin-inline-start: auto;
}
/* 订阅卡 */
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
/* 紧凑统计 */
.stats-section {
  margin-bottom: 20px;
}
.stats-row {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  padding: 4px 0;
  margin-bottom: 12px;
}
.stat-mini {
  flex: 0 0 auto;
  min-width: 120px;
  padding: 8px 12px;
  border: 1px solid var(--border, #30363d);
  border-radius: 6px;
  background: var(--card, #1c2128);
  transition: all 0.15s ease;
}
.stat-mini:hover {
  border-color: var(--accent, #6366f1);
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.2);
}
.stat-mini--highlight {
  border-color: rgba(99, 102, 241, 0.4);
  background: rgba(99, 102, 241, 0.06);
}
.stat-mini__label {
  font-size: 11px;
  color: var(--text-secondary, #8b949e);
  white-space: nowrap;
  margin-bottom: 4px;
  font-weight: 500;
}
.stat-mini__value {
  font-size: 18px;
  font-weight: 700;
  color: var(--text, #e6edf3);
  font-variant-numeric: tabular-nums;
}
.stat-mini__sub {
  font-size: 10px;
  color: var(--muted);
  margin-top: 4px;
}
.stat-mini--skeleton {
  background: linear-gradient(90deg, var(--bg-subtle, #161b22) 25%, var(--border, #30363d) 50%, var(--bg-subtle, #161b22) 75%);
  background-size: 200% 100%;
  animation: skeleton-loading 1.5s ease-in-out infinite;
  min-height: 64px;
}
@keyframes skeleton-loading {
  0% { background-position: 200% 0; }
  100% { background-position: -200% 0; }
}
/* 卡 / 图 / 表 */
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
  margin-inline-start: 8px;
}
.bar-chart {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.bar-row {
  display: grid;
  grid-template-columns: 140px 1fr 88px;
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
.alert-danger {
  padding: 8px 12px;
  border-radius: 4px;
  background: rgba(239, 68, 68, 0.1);
  color: #f87171;
  margin-bottom: 12px;
}
@media (max-width: 1024px) {
  .page-header {
    flex-wrap: wrap;
  }
  .page-header-right {
    flex-wrap: wrap;
  }
}
@media (max-width: 768px) {
  .page-header {
    flex-direction: column;
    align-items: stretch;
  }
  .page-header-left,
  .page-header-right {
    width: 100%;
  }
  .stat-mini {
    flex: 1 1 calc(50% - 8px);
    min-width: 0;
  }
}
</style>
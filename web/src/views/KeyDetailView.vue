<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { getKeyDetail, updateKeyLimits, type ApiKey, type UpdateKeyLimitsRequest } from '../api'
import {
  getKeyUsage,
  getKeyUsageByModel,
  getKeyUsageTrend,
  type KeyUsageSummary,
  type ModelUsageForKey,
  type TrendEntry,
} from '../api'

// ── Route params ──────────────────────────────────────────────────────────
const route = useRoute()
const router = useRouter()
const keyId = computed(() => Number(route.params.id))

// ── State ──────────────────────────────────────────────────────────────────
const keyInfo = ref<ApiKey | null>(null)
const loading = ref(false)
const error = ref('')

const keyUsage = ref<KeyUsageSummary | null>(null)
const keyModels = ref<ModelUsageForKey[]>([])
const keyTrend = ref<TrendEntry[]>([])
const detailLoading = ref(false)
const detailError = ref('')

// ── Limit editing ──────────────────────────────────────────────────────────
const showLimitsEditor = ref(false)
const limitsForm = ref<UpdateKeyLimitsRequest>({
  rate_limit_rpm: null,
  rate_limit_concurrent: null,
  rate_limit_tpm: null,
})
const limitsSaving = ref(false)
const limitsErr = ref('')
const limitsSuccess = ref('')

type LimitMode = 'default' | 'custom'
const rpmMode = ref<LimitMode>('default')
const concurrentMode = ref<LimitMode>('default')
const tpmMode = ref<LimitMode>('default')

function initLimitsForm() {
  const k = keyInfo.value
  if (!k) return
  limitsForm.value = {
    rate_limit_rpm: k.rate_limit_rpm,
    rate_limit_concurrent: k.rate_limit_concurrent ?? null,
    rate_limit_tpm: k.rate_limit_tpm ?? null,
  }
  // 'unlimited' was removed: backend rejects 0; "default" (= null) is
  // the only way to opt out of a per-key limit.
  rpmMode.value = k.rate_limit_rpm == null ? 'default' : 'custom'
  concurrentMode.value = k.rate_limit_concurrent == null ? 'default' : 'custom'
  tpmMode.value = k.rate_limit_tpm == null ? 'default' : 'custom'
  limitsErr.value = ''
  limitsSuccess.value = ''
}

function openLimitsEditor() {
  initLimitsForm()
  showLimitsEditor.value = true
}

function modeToValue(mode: LimitMode, current: number | null | undefined): number | null {
  if (mode === 'default') return null
  return current ?? 0
}

async function saveLimits() {
  limitsErr.value = ''
  limitsSuccess.value = ''
  limitsSaving.value = true
  try {
    const data: UpdateKeyLimitsRequest = {
      rate_limit_rpm: modeToValue(rpmMode.value, limitsForm.value.rate_limit_rpm),
      rate_limit_concurrent: modeToValue(concurrentMode.value, limitsForm.value.rate_limit_concurrent),
      rate_limit_tpm: modeToValue(tpmMode.value, limitsForm.value.rate_limit_tpm),
    }
    await updateKeyLimits(keyId.value, data)
    limitsSuccess.value = '限制已保存'
    showLimitsEditor.value = false
    await loadKey()
  } catch (e: unknown) {
    limitsErr.value = e instanceof Error ? e.message : '保存失败'
  } finally {
    limitsSaving.value = false
  }
}

// Time range (summary cards)
type PeriodType = 'minute' | 'hour' | 'day' | 'week' | 'month'
const periodOptions: { label: string; days: number }[] = [
  { label: '最近 1 天', days: 1 },
  { label: '最近 3 天', days: 3 },
  { label: '最近 7 天', days: 7 },
  { label: '最近 30 天', days: 30 },
  { label: '最近 90 天', days: 90 },
]
const selectedPeriod = ref(periodOptions[2])

// Trend chart controls
type TrendMetric = 'requests' | 'cost'
const trendMetric = ref<TrendMetric>('requests')
const trendPeriod = ref<PeriodType>('hour')
const hoveredTrendIndex = ref<number | null>(null)

const CHART_W = 640
const CHART_H = 160
const CHART_PAD = { l: 44, r: 10, t: 8, b: 22 }
const trendPeriodOptions: { label: string; value: PeriodType }[] = [
  { label: '按分钟', value: 'minute' },
  { label: '按小时', value: 'hour' },
  { label: '按天', value: 'day' },
  { label: '按周', value: 'week' },
  { label: '按月', value: 'month' },
]

const TREND_WINDOW_LIMITS: Record<'minute' | 'hour', number> = {
  minute: 3,
  hour: 31,
}

// Custom date range (shared by summary + trend)
const useCustomRange = ref(false)
const customStart = ref('')
const customEnd = ref('')

function trendValue(t: TrendEntry): number {
  return trendMetric.value === 'cost' ? t.cost_usd : t.requests
}

// ── Computed ───────────────────────────────────────────────────────────────
const maxTrendValue = computed(() => {
  if (keyTrend.value.length === 0) return 0
  return Math.max(...keyTrend.value.map(t => trendValue(t)))
})

const totalTrendValue = computed(() => {
  if (keyTrend.value.length === 0) return 0
  return keyTrend.value.reduce((sum, t) => sum + trendValue(t), 0)
})

const trendHasActivity = computed(() =>
  keyTrend.value.some(t => t.requests > 0 || t.cost_usd > 0)
)

const trendSummaryLabel = computed(() => {
  const periodLabel = trendPeriodOptions.find(o => o.value === trendPeriod.value)?.label ?? '按小时'
  if (useCustomRange.value && customStart.value && customEnd.value) {
    return `${periodLabel} · ${customStart.value} ~ ${customEnd.value}`
  }
  return `${periodLabel} · ${selectedPeriod.value.label}`
})

const trendGranularityHint = computed(() => {
  if (trendPeriod.value === 'minute') {
    return '按分钟最多 3 天窗口，便于观察日内峰谷'
  }
  if (trendPeriod.value === 'hour') {
    return '按小时最多 31 天窗口，便于对比每日时段规律'
  }
  return ''
})

function customRangeDaySpan(): number | null {
  if (!useCustomRange.value || !customStart.value || !customEnd.value) return null
  const start = new Date(`${customStart.value}T00:00:00`)
  const end = new Date(`${customEnd.value}T00:00:00`)
  if (Number.isNaN(start.getTime()) || Number.isNaN(end.getTime()) || end < start) return null
  return Math.floor((end.getTime() - start.getTime()) / 86400000) + 1
}

function effectiveWindowDays(): number {
  const customDays = customRangeDaySpan()
  if (customDays != null) return customDays
  return selectedPeriod.value.days
}

function clampWindowForGranularity(period: PeriodType): boolean {
  if (period !== 'minute' && period !== 'hour') return false
  const limit = TREND_WINDOW_LIMITS[period]
  if (effectiveWindowDays() <= limit) return false
  if (useCustomRange.value) return false
  const match = periodOptions.find(o => o.days === limit)
    ?? periodOptions.filter(o => o.days <= limit).at(-1)
  if (match && selectedPeriod.value !== match) {
    selectedPeriod.value = match
    return true
  }
  return false
}

const trendTotalMatchesSummary = computed(() => {
  if (!keyUsage.value || keyTrend.value.length === 0) return true
  if (trendMetric.value === 'requests') {
    return totalTrendValue.value === keyUsage.value.total_requests
  }
  return Math.abs(totalTrendValue.value - keyUsage.value.total_cost_usd) < 1e-9
})

function buildYAxis(maxVal: number, divisions = 4): { max: number; ticks: number[] } {
  if (maxVal <= 0) {
    return { max: 1, ticks: [0] }
  }
  const padded = maxVal * 1.08
  const rawStep = padded / divisions
  const mag = Math.pow(10, Math.floor(Math.log10(rawStep)))
  const norm = rawStep / mag
  let niceStep: number
  if (norm <= 1) niceStep = mag
  else if (norm <= 2) niceStep = 2 * mag
  else if (norm <= 5) niceStep = 5 * mag
  else niceStep = 10 * mag
  const niceMax = Math.ceil(padded / niceStep) * niceStep
  const ticks: number[] = []
  for (let v = 0; v <= niceMax + niceStep * 0.001; v += niceStep) {
    ticks.push(Number(v.toFixed(10)))
    if (ticks.length > divisions + 1) break
  }
  return { max: niceMax, ticks }
}

const yAxis = computed(() => buildYAxis(maxTrendValue.value))

const chartPixelWidth = computed(() => {
  const n = keyTrend.value.length
  if (n <= 48) return CHART_W
  const pxPerPoint = trendPeriod.value === 'minute' ? 4 : trendPeriod.value === 'hour' ? 5 : 6
  return Math.max(CHART_W, n * pxPerPoint)
})

const chartViewBox = computed(() => `0 0 ${chartPixelWidth.value} ${CHART_H}`)

const plotSize = computed(() => ({
  w: chartPixelWidth.value - CHART_PAD.l - CHART_PAD.r,
  h: CHART_H - CHART_PAD.t - CHART_PAD.b,
}))

function formatYTick(v: number): string {
  if (trendMetric.value === 'cost') {
    if (v === 0) return '$0'
    if (v < 0.01) return '$' + v.toFixed(4)
    if (v < 1) return '$' + v.toFixed(3)
    return '$' + v.toFixed(2)
  }
  if (v >= 10000) return (v / 1000).toFixed(0) + 'k'
  if (v >= 1000) return (v / 1000).toFixed(1) + 'k'
  return String(Math.round(v))
}

const yGridLines = computed(() => {
  const { max, ticks } = yAxis.value
  const { h } = plotSize.value
  return ticks.map(v => ({
    y: CHART_PAD.t + h - (max > 0 ? (v / max) * h : 0),
    label: formatYTick(v),
  }))
})

function maxXLabelsForPeriod(total: number): number {
  if (trendPeriod.value === 'minute') {
    if (total > 500) return 5
    if (total > 200) return 6
    return 8
  }
  if (trendPeriod.value === 'hour') {
    if (total > 120) return 8
    return 10
  }
  if (total > 60) return 6
  return 8
}

function shouldShowXLabel(index: number, total: number): boolean {
  if (total <= 8) return true
  if (index === 0 || index === total - 1) return true
  const maxLabels = maxXLabelsForPeriod(total)
  const step = Math.max(1, Math.ceil(total / maxLabels))
  return index % step === 0
}

function compactTrendLabel(s: string, period: PeriodType, total: number): string {
  if (!s) return '—'
  if (period === 'minute' || period === 'hour') {
    const m = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})/.exec(s)
    if (m) {
      if (period === 'minute') {
        if (total > 200) return `${m[4]}:${m[5]}`
        if (total > 48) return `${parseInt(m[3], 10)}日${m[4]}:${m[5]}`
        return `${m[2]}/${m[3]} ${m[4]}:${m[5]}`
      }
      if (total > 72) return `${parseInt(m[3], 10)}日${m[4]}时`
      return `${m[2]}/${m[3]} ${m[4]}:00`
    }
    return s
  }
  return fmtTrendPeriod(s, period)
}

const chartPoints = computed(() => {
  const data = keyTrend.value
  const { max } = yAxis.value
  const { w, h } = plotSize.value
  const n = data.length
  return data.map((t, i) => {
    const value = trendValue(t)
    const x = CHART_PAD.l + (n <= 1 ? w / 2 : (i / (n - 1)) * w)
    const y = CHART_PAD.t + h - (max > 0 ? (value / max) * h : h)
    return {
      x,
      y,
      value,
      entry: t,
      label: compactTrendLabel(t.period, trendPeriod.value, n),
    }
  })
})

const linePathD = computed(() => {
  const pts = chartPoints.value
  if (pts.length === 0) return ''
  return pts
    .map((p, i) => `${i === 0 ? 'M' : 'L'} ${p.x.toFixed(1)} ${p.y.toFixed(1)}`)
    .join(' ')
})

const areaPathD = computed(() => {
  const pts = chartPoints.value
  if (pts.length === 0) return ''
  const baseY = CHART_PAD.t + plotSize.value.h
  const line = pts.map((p, i) => `${i === 0 ? 'M' : 'L'} ${p.x.toFixed(1)} ${p.y.toFixed(1)}`).join(' ')
  const last = pts[pts.length - 1]
  const first = pts[0]
  return `${line} L ${last.x.toFixed(1)} ${baseY.toFixed(1)} L ${first.x.toFixed(1)} ${baseY.toFixed(1)} Z`
})

const hoveredPoint = computed(() => {
  if (hoveredTrendIndex.value == null) return null
  return chartPoints.value[hoveredTrendIndex.value] ?? null
})

const trendLineColor = computed(() =>
  trendMetric.value === 'cost' ? 'var(--success)' : 'var(--accent)'
)

// ── Helpers ────────────────────────────────────────────────────────────────
function fmtDate(s: string | null | undefined) {
  if (!s) return '—'
  return new Date(s).toLocaleString('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
}

function fmtDateShort(s: string | null | undefined) {
  if (!s) return '—'
  return new Date(s).toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' })
}

// Format a trend period label based on the selected trend period.
// The backend emits "YYYY-MM-DD" for day, "IYYY-IW" for week, "YYYY-MM"
// for month.  new Date() does not parse "2026-25" (returns Invalid Date),
// so we need period-aware formatting.
function fmtTrendPeriod(s: string, period: PeriodType) {
  if (!s) return '—'
  if (period === 'minute' || period === 'hour') {
    const m = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2})/.exec(s)
    if (m) {
      if (period === 'hour') return `${m[2]}/${m[3]} ${m[4]}:00`
      return `${m[2]}/${m[3]} ${m[4]}:${m[5]}`
    }
    return s
  }
  if (period === 'week') {
    const m = /^(\d{4})-(\d{1,2})$/.exec(s)
    if (m) return `${m[1].slice(2)}-${m[2]}周`
    return s
  }
  if (period === 'month') {
    const m = /^(\d{4})-(\d{1,2})$/.exec(s)
    if (m) return `${m[1].slice(2)}年${parseInt(m[2], 10)}月`
    return s
  }
  return new Date(s).toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' })
}

function fmtNum(n: number | string | null | undefined, decimals = 0): string {
  if (n == null) return '0'
  return Number(n).toLocaleString('zh-CN', { minimumFractionDigits: decimals, maximumFractionDigits: decimals })
}

function fmtCost(n: number | string | null | undefined): string {
  if (n == null) return '$0.00'
  return '$' + Number(n).toFixed(6)
}

function fmtTrendValue(value: number): string {
  if (trendMetric.value === 'cost') return fmtCost(value)
  return fmtNum(value)
}

function fmtQueryWindow(): string {
  const u = keyUsage.value
  if (u?.window_start && u?.window_end) {
    const end = new Date(u.window_end)
    end.setUTCDate(end.getUTCDate() - 1)
    return `${fmtDateShort(u.window_start)} ~ ${fmtDateShort(end.toISOString())}`
  }
  if (useCustomRange.value && customStart.value && customEnd.value) {
    return `${customStart.value} ~ ${customEnd.value}`
  }
  return selectedPeriod.value.label
}

function trendTooltipStyle(p: { x: number; y: number }): Record<string, string> {
  const width = chartPixelWidth.value
  const leftPct = Math.min(92, Math.max(8, (p.x / width) * 100))
  const topPct = Math.max(6, (p.y / CHART_H) * 100 - 14)
  return {
    left: `${leftPct}%`,
    top: `${topPct}%`,
  }
}

async function onTrendPeriodChange(period: PeriodType) {
  trendPeriod.value = period
  clampWindowForGranularity(period)
  await changePeriod()
}

// ── Data loading ───────────────────────────────────────────────────────────
async function loadKey() {
  loading.value = true
  error.value = ''
  try {
    keyInfo.value = await getKeyDetail(keyId.value)
  } catch (e: unknown) {
    error.value = e instanceof Error ? e.message : '加载失败'
  } finally {
    loading.value = false
  }
}

async function loadDetail() {
  if (!keyId.value) return

  detailLoading.value = true
  detailError.value = ''
  keyUsage.value = null
  keyModels.value = []
  keyTrend.value = []

  try {
    const params: { days?: number; start?: string; end?: string } = {}
    if (useCustomRange.value && customStart.value && customEnd.value) {
      params.start = customStart.value
      params.end = customEnd.value
    } else {
      params.days = selectedPeriod.value.days
    }

    const [usage, models, trend] = await Promise.all([
      getKeyUsage(keyId.value, params),
      getKeyUsageByModel(keyId.value, { ...params, limit: 50 }),
      getKeyUsageTrend(keyId.value, trendPeriod.value, useCustomRange.value && customStart.value && customEnd.value
        ? { start: customStart.value, end: customEnd.value }
        : { days: selectedPeriod.value.days }),
    ])

    keyUsage.value = usage
    keyModels.value = models
    keyTrend.value = trend
  } catch (e: unknown) {
    detailError.value = e instanceof Error ? e.message : '加载详情失败'
  } finally {
    detailLoading.value = false
  }
}

async function changePeriod() {
  // When custom-range mode is enabled, only reload after both dates are
  // filled.  Otherwise the @change handler fires twice (once when each
  // date input becomes non-empty) and the first call falls back to the
  // default `days` range, causing a visible flicker.
  if (useCustomRange.value) {
    if (!customStart.value || !customEnd.value) return
  }
  await loadDetail()
}

onMounted(async () => {
  await loadKey()
  if (keyId.value) {
    await loadDetail()
  }
})

watch(keyId, async () => {
  await loadKey()
  await loadDetail()
})
</script>

<template>
  <div class="key-detail-page">
    <!-- Back button -->
    <div class="page-header">
      <button class="btn btn-ghost" @click="router.push('/keys')">← 返回密钥列表</button>
      <h2 v-if="keyInfo">密钥统计: {{ keyInfo.key_prefix }}***</h2>
      <h2 v-else>密钥统计</h2>
    </div>

    <div v-if="loading" class="empty">加载中…</div>
    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <template v-if="keyInfo && !loading">
      <!-- Key info card -->
      <div class="card key-info-card">
        <div class="key-info-header">
          <span class="key-info-title">密钥信息</span>
          <button class="btn btn-sm" @click="openLimitsEditor">⚙ 编辑限制</button>
        </div>
        <div class="key-info-row">
          <div class="key-info-item">
            <span class="key-info-label">应用</span>
            <span class="key-info-value">{{ keyInfo.application_code }}</span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">归属用户</span>
            <span class="key-info-value">{{ keyInfo.owner_user ?? '—' }}</span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">状态</span>
            <span class="badge" :class="keyInfo.enabled ? 'badge-green' : 'badge-red'">
              {{ keyInfo.enabled ? '有效' : '已吊销' }}
            </span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">预算</span>
            <span class="key-info-value">{{ keyInfo.budget_usd != null ? fmtCost(keyInfo.budget_usd) : '无限制' }}</span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">RPM</span>
            <span class="key-info-value">{{ keyInfo.rate_limit_rpm != null ? keyInfo.rate_limit_rpm + ' RPM' : '默认' }}</span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">并发</span>
            <span class="key-info-value">{{ keyInfo.rate_limit_concurrent != null ? keyInfo.rate_limit_concurrent : '默认' }}</span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">TPM</span>
            <span class="key-info-value">{{ keyInfo.rate_limit_tpm != null ? fmtNum(keyInfo.rate_limit_tpm) + ' TPM' : '不限制' }}</span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">最后使用</span>
            <span class="key-info-value">{{ fmtDate(keyInfo.last_used_at) }}</span>
          </div>
        </div>

        <!-- Limit editor modal -->
        <div v-if="showLimitsEditor" class="modal-overlay" @click.self="showLimitsEditor = false">
          <div class="modal" style="max-width:450px" @click.stop>
            <h3>编辑速率限制</h3>
            <div v-if="limitsErr" class="alert alert-danger">{{ limitsErr }}</div>
            <div v-if="limitsSuccess" class="alert alert-success">{{ limitsSuccess }}</div>

            <div class="form-group">
              <label>RPM（每分钟请求数）</label>
<div class="limit-options">
                <label><input type="radio" v-model="rpmMode" value="default"> 默认</label>
                <label><input type="radio" v-model="rpmMode" value="custom"> 自定义</label>
              </div>
              <input v-if="rpmMode === 'custom'" v-model.number="limitsForm.rate_limit_rpm" type="number" min="1" placeholder="输入 RPM">
            </div>

            <div class="form-group">
              <label>并发（同时请求数）</label>
              <div class="limit-options">
                <label><input type="radio" v-model="concurrentMode" value="default"> 默认</label>
                <label><input type="radio" v-model="concurrentMode" value="custom"> 自定义</label>
              </div>
              <input v-if="concurrentMode === 'custom'" v-model.number="limitsForm.rate_limit_concurrent" type="number" min="1" placeholder="输入并发数">
            </div>

            <div class="form-group">
              <label>TPM（每分钟 Token 数）</label>
              <div class="limit-options">
                <label><input type="radio" v-model="tpmMode" value="default"> 不限制</label>
                <label><input type="radio" v-model="tpmMode" value="custom"> 自定义</label>
              </div>
              <input v-if="tpmMode === 'custom'" v-model.number="limitsForm.rate_limit_tpm" type="number" min="1" placeholder="输入 TPM">
            </div>

            <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
              <button class="btn btn-ghost" @click="showLimitsEditor = false" :disabled="limitsSaving">取消</button>

              <button class="btn btn-primary" @click="saveLimits" :disabled="limitsSaving">
                {{ limitsSaving ? '保存中…' : '保存' }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- Usage stats + trend -->
      <div class="card">
        <!-- Loading state -->
        <div v-if="detailLoading" class="empty">加载中…</div>
        <div v-else-if="detailError" class="alert alert-danger">{{ detailError }}</div>

        <template v-else-if="keyUsage">
          <!-- Summary cards -->
          <div class="stats-grid">
            <div class="stat-card">
              <div class="stat-label">总请求数</div>
              <div class="stat-value">{{ fmtNum(keyUsage.total_requests) }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">Prompt Tokens</div>
              <div class="stat-value">{{ fmtNum(keyUsage.total_prompt_tokens) }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">Completion Tokens</div>
              <div class="stat-value">{{ fmtNum(keyUsage.total_completion_tokens) }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">总 Tokens</div>
              <div class="stat-value">{{ fmtNum(keyUsage.total_tokens) }}</div>
            </div>
            <div class="stat-card highlight">
              <div class="stat-label">总费用</div>
              <div class="stat-value cost">{{ fmtCost(keyUsage.total_cost_usd) }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">成功率</div>
              <div class="stat-value">{{ keyUsage.total_requests > 0 ? (keyUsage.success_rate * 100).toFixed(1) + '%' : '—' }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">平均延迟</div>
              <div class="stat-value">{{ keyUsage.avg_latency_ms.toFixed(0) }}ms</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">使用模型数</div>
              <div class="stat-value">{{ keyUsage.unique_models }}</div>
            </div>
          </div>

          <!-- Time range info -->
          <div class="time-range-info">
            <span>查询窗口：{{ fmtQueryWindow() }}</span>
            <span v-if="keyUsage.first_request_at || keyUsage.last_request_at" class="time-range-actual">
              · 实际使用 {{ fmtDate(keyUsage.first_request_at) }} ~ {{ fmtDate(keyUsage.last_request_at) }}
            </span>
          </div>

          <!-- Trend chart -->
          <div class="section trend-section">
            <div class="trend-header">
              <div class="section-title">使用趋势</div>
              <div class="trend-controls">
                <div class="trend-tabs" role="tablist" aria-label="趋势指标">
                  <button
                    type="button"
                    class="btn btn-sm"
                    :class="trendMetric === 'requests' ? 'btn-primary' : 'btn-ghost'"
                    role="tab"
                    :aria-selected="trendMetric === 'requests'"
                    @click="trendMetric = 'requests'"
                  >次数</button>
                  <button
                    type="button"
                    class="btn btn-sm"
                    :class="trendMetric === 'cost' ? 'btn-primary' : 'btn-ghost'"
                    role="tab"
                    :aria-selected="trendMetric === 'cost'"
                    @click="trendMetric = 'cost'"
                  >费用</button>
                </div>
                <span class="trend-divider" aria-hidden="true"></span>
                <div class="trend-window">
                  <button
                    v-for="opt in periodOptions"
                    :key="opt.label"
                    type="button"
                    class="btn btn-sm"
                    :class="selectedPeriod === opt && !useCustomRange ? 'btn-primary' : 'btn-ghost'"
                    @click="useCustomRange = false; selectedPeriod = opt; clampWindowForGranularity(trendPeriod); changePeriod()"
                  >{{ opt.label }}</button>
                  <button
                    type="button"
                    class="btn btn-sm"
                    :class="useCustomRange ? 'btn-primary' : 'btn-ghost'"
                    @click="useCustomRange = true; changePeriod()"
                  >自定义</button>
                  <template v-if="useCustomRange">
                    <input type="date" v-model="customStart" @change="changePeriod" class="date-input">
                    <span class="range-sep">至</span>
                    <input type="date" v-model="customEnd" @change="changePeriod" class="date-input">
                  </template>
                </div>
                <span class="trend-divider" aria-hidden="true"></span>
                <div class="trend-granularity">
                  <button
                    v-for="opt in trendPeriodOptions"
                    :key="opt.value"
                    type="button"
                    class="btn btn-sm"
                    :class="trendPeriod === opt.value ? 'btn-primary' : 'btn-ghost'"
                    @click="onTrendPeriodChange(opt.value)"
                  >{{ opt.label }}</button>
                </div>
              </div>
            </div>
            <div v-if="trendGranularityHint" class="trend-hint">{{ trendGranularityHint }}</div>
            <div class="trend-chart" v-if="keyTrend.length > 0">
              <div class="trend-chart-scroll">
                <div class="trend-chart-wrap" :style="{ width: chartPixelWidth + 'px', minWidth: '100%' }">
                <svg
                  class="trend-svg"
                  :viewBox="chartViewBox"
                  preserveAspectRatio="none"
                  role="img"
                  :aria-label="trendMetric === 'cost' ? '费用折线图' : '请求次数折线图'"
                >
                  <line
                    :x1="CHART_PAD.l"
                    :y1="CHART_PAD.t"
                    :x2="CHART_PAD.l"
                    :y2="CHART_PAD.t + plotSize.h"
                    class="trend-axis-line"
                  />
                  <line
                    v-for="(grid, gi) in yGridLines"
                    :key="'grid-' + gi"
                    :x1="CHART_PAD.l"
                    :y1="grid.y"
                    :x2="chartPixelWidth - CHART_PAD.r"
                    :y2="grid.y"
                    class="trend-grid-line"
                  />
                  <path :d="areaPathD" class="trend-area" :style="{ fill: trendLineColor }" />
                  <path
                    :d="linePathD"
                    class="trend-line"
                    fill="none"
                    :stroke="trendLineColor"
                  />
                  <g
                    v-for="(p, i) in chartPoints"
                    :key="'pt-' + p.entry.period"
                    class="trend-dot-group"
                    @mouseenter="hoveredTrendIndex = i"
                    @mouseleave="hoveredTrendIndex = null"
                  >
                    <circle
                      :cx="p.x"
                      :cy="p.y"
                      r="10"
                      class="trend-dot-hit"
                    />
                    <circle
                      :cx="p.x"
                      :cy="p.y"
                      r="3"
                      class="trend-dot"
                      :class="{ 'trend-dot--active': hoveredTrendIndex === i }"
                      :fill="trendLineColor"
                    />
                  </g>
                  <text
                    v-for="(grid, gi) in yGridLines"
                    :key="'ylabel-' + gi"
                    :x="CHART_PAD.l - 8"
                    :y="grid.y + 3.5"
                    class="trend-y-label"
                    text-anchor="end"
                  >{{ grid.label }}</text>
                  <text
                    v-for="(p, i) in chartPoints"
                    :key="'xlabel-' + p.entry.period"
                    :x="p.x"
                    :y="CHART_H - 6"
                    class="trend-x-label"
                    text-anchor="middle"
                    :opacity="shouldShowXLabel(i, chartPoints.length) ? 1 : 0"
                  >{{ p.label }}</text>
                </svg>
                <div v-if="hoveredPoint" class="trend-tooltip" :style="trendTooltipStyle(hoveredPoint)">
                  <div class="trend-tooltip-period">{{ fmtTrendPeriod(hoveredPoint.entry.period, trendPeriod) }}</div>
                  <div class="trend-tooltip-value">
                    {{ trendMetric === 'cost' ? fmtCost(hoveredPoint.entry.cost_usd) : fmtNum(hoveredPoint.entry.requests) + ' 次' }}
                  </div>
                </div>
                </div>
              </div>
              <div class="trend-summary">
                <span>{{ trendSummaryLabel }} · 共 {{ keyTrend.length }} 个周期 · 合计 {{ fmtTrendValue(totalTrendValue) }}</span>
                <span v-if="!trendHasActivity" class="trend-summary-muted">（该窗口内无使用记录）</span>
                <span v-if="!trendTotalMatchesSummary" class="trend-summary-warn" title="趋势合计与上方汇总卡片不一致，可能由时区或聚合粒度导致">⚠ 与汇总不完全一致</span>
              </div>
            </div>
            <div v-else-if="keyUsage.total_requests > 0" class="empty small">
              汇总有数据但趋势序列为空，请刷新页面；若仍异常请联系管理员
            </div>
            <div v-else class="empty small">{{ trendSummaryLabel }}内暂无使用记录</div>
          </div>

          <!-- Model breakdown -->
          <div class="section">
            <div class="section-title">模型使用详情</div>
            <table class="detail-table" v-if="keyModels.length > 0">
              <thead>
                <tr>
                  <th>模型</th>
                  <th>请求数</th>
                  <th>Prompt Tokens</th>
                  <th>Completion Tokens</th>
                  <th>总 Tokens</th>
                  <th>费用</th>
                  <th>成功率</th>
                  <th>首次使用</th>
                  <th>最近使用</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="m in keyModels" :key="m.model">
                  <td><code>{{ m.model }}</code></td>
                  <td>{{ fmtNum(m.request_count) }}</td>
                  <td>{{ fmtNum(m.prompt_tokens) }}</td>
                  <td>{{ fmtNum(m.completion_tokens) }}</td>
                  <td>{{ fmtNum(m.total_tokens) }}</td>
                  <td class="cost-cell">{{ fmtCost(m.cost_usd) }}</td>
                  <td>{{ (m.success_rate * 100).toFixed(1) }}%</td>
                  <td style="font-size:11px">{{ fmtDateShort(m.first_used_at) }}</td>
                  <td style="font-size:11px">{{ fmtDateShort(m.last_used_at) }}</td>
                </tr>
              </tbody>
            </table>
            <div v-else class="empty small">暂无模型使用数据</div>
          </div>
        </template>
      </div>
    </template>
  </div>
</template>

<style scoped>
.key-detail-page {
  max-width: 1400px;
}

.page-header {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 16px;
}

.page-header h2 {
  margin: 0;
  font-size: 18px;
}

.key-info-card {
  margin-bottom: 16px;
}

.key-info-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.key-info-title {
  font-size: 14px;
  font-weight: 600;
}

.key-info-row {
  display: flex;
  flex-wrap: wrap;
  gap: 24px;
}

.key-info-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.key-info-label {
  font-size: 11px;
  color: var(--muted);
  text-transform: uppercase;
}

.key-info-value {
  font-size: 14px;
  font-weight: 500;
}

.detail-toolbar {
  margin-bottom: 16px;
}

.period-selector {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
}

.custom-range-toggle {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 13px;
  color: var(--text-secondary);
  cursor: pointer;
  margin-left: 8px;
}

.date-input {
  padding: 4px 8px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  font-size: 12px;
  margin-left: 4px;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
  gap: 12px;
  margin-bottom: 20px;
}

.stat-card {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 16px 12px;
  text-align: center;
}

.stat-card.highlight {
  border-color: var(--success);
  background: color-mix(in srgb, var(--success) 10%, transparent);
}

.stat-label {
  font-size: 11px;
  color: var(--muted);
  margin-bottom: 6px;
  text-transform: uppercase;
}

.stat-value {
  font-size: 20px;
  font-weight: 600;
}

.stat-value.cost {
  color: var(--success);
}

.time-range-info {
  font-size: 12px;
  color: var(--muted);
  text-align: center;
  margin-bottom: 16px;
}

.time-range-actual {
  color: color-mix(in srgb, var(--muted) 80%, var(--text));
}

.section {
  margin-top: 20px;
}

.section-title {
  font-size: 14px;
  font-weight: 600;
  margin-bottom: 0;
}

.trend-section {
  margin-top: 16px;
}

.trend-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 10px;
  flex-wrap: nowrap;
}

.trend-controls {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: nowrap;
  min-width: 0;
  overflow-x: auto;
  scrollbar-width: thin;
}

.trend-divider {
  width: 1px;
  height: 18px;
  background: var(--border);
  flex-shrink: 0;
}

.trend-tabs {
  display: inline-flex;
  flex-wrap: nowrap;
  gap: 0;
  align-items: center;
  flex-shrink: 0;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
}

.trend-tabs .btn {
  border-radius: 0;
  border: none;
  box-shadow: none;
}

.trend-tabs .btn-ghost {
  border-right: 1px solid var(--border);
}

.trend-tabs .btn-ghost:last-child {
  border-right: none;
}

.trend-window,
.trend-granularity {
  display: flex;
  flex-wrap: nowrap;
  gap: 4px;
  align-items: center;
  flex-shrink: 0;
}

.range-sep {
  font-size: 11px;
  color: var(--muted);
  flex-shrink: 0;
}

.date-input {
  padding: 3px 6px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  font-size: 11px;
  width: auto;
  flex-shrink: 0;
}

.trend-chart {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 10px 12px 8px;
}

.trend-hint {
  font-size: 11px;
  color: var(--muted);
  margin: -4px 0 8px;
  text-align: right;
}

.trend-chart-scroll {
  overflow-x: auto;
  overflow-y: hidden;
  scrollbar-width: thin;
}

.trend-chart-wrap {
  position: relative;
  height: 160px;
}

.trend-svg {
  display: block;
  width: 100%;
  height: 100%;
}

.trend-axis-line {
  stroke: color-mix(in srgb, var(--muted) 55%, var(--border));
  stroke-width: 1;
}

.trend-grid-line {
  stroke: var(--border);
  stroke-width: 1;
  stroke-dasharray: 4 4;
  opacity: 0.55;
}

.trend-area {
  opacity: 0.14;
}

.trend-line {
  stroke-width: 2;
  stroke-linecap: round;
  stroke-linejoin: round;
}

.trend-dot-group {
  cursor: pointer;
}

.trend-dot-hit {
  fill: transparent;
  pointer-events: all;
}

.trend-dot {
  opacity: 0.55;
  transition: opacity 0.15s;
}

.trend-dot--active,
.trend-dot-group:hover .trend-dot {
  opacity: 1;
}

.trend-y-label {
  fill: var(--muted);
  font-size: 8px;
  font-family: inherit;
}

.trend-x-label {
  fill: var(--muted);
  font-size: 8px;
  font-family: inherit;
}

.trend-tooltip {
  position: absolute;
  transform: translate(-50%, -100%);
  pointer-events: none;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 4px 8px;
  font-size: 11px;
  line-height: 1.35;
  white-space: nowrap;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.25);
  z-index: 2;
}

.trend-tooltip-period {
  color: var(--muted);
}

.trend-tooltip-value {
  font-weight: 600;
}

.trend-summary {
  margin-top: 8px;
  font-size: 12px;
  color: var(--muted);
  text-align: center;
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
  justify-content: center;
}

.trend-summary-warn {
  color: var(--warning);
  font-size: 11px;
}

.trend-summary-muted {
  color: var(--muted);
  font-size: 11px;
}

.detail-table {
  width: 100%;
  font-size: 13px;
  border-collapse: collapse;
}

.detail-table th {
  text-align: left;
  padding: 10px 12px;
  border-bottom: 2px solid var(--border);
  color: var(--muted);
  font-weight: 500;
  font-size: 11px;
  text-transform: uppercase;
}

.detail-table td {
  padding: 10px 12px;
  border-bottom: 1px solid var(--border);
}

.detail-table tr:hover td {
  background: var(--bg-hover);
}

.cost-cell {
  color: var(--success);
  font-weight: 600;
}

.empty.small {
  padding: 24px;
  font-size: 13px;
}

.key-info-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.key-info-title {
  font-size: 15px;
  font-weight: 600;
}

.limit-options {
  display: flex;
  gap: 16px;
  flex-wrap: nowrap;
  margin-bottom: 8px;
}

.limit-options label {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  white-space: nowrap;
  cursor: pointer;
}

.limit-options input[type="radio"] {
  width: auto;
}
</style>
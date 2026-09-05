<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { localeRef } from '../i18n'
import { fmtDateTimeShort, fmtDateCompact } from '../i18n/useFormat'
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
const { t, tm } = useI18n()

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

type LimitMode = 'default' | 'unlimited' | 'custom'
const rpmMode = ref<LimitMode>('default')
const concurrentMode = ref<LimitMode>('default')
const tpmMode = ref<LimitMode>('default')

function limitModeFromValue(v: number | null | undefined, supportsUnlimited: boolean): LimitMode {
  if (v == null) return 'default'
  if (supportsUnlimited && v === 0) return 'unlimited'
  return 'custom'
}

function initLimitsForm() {
  const k = keyInfo.value
  if (!k) return
  limitsForm.value = {
    rate_limit_rpm: k.rate_limit_rpm,
    rate_limit_concurrent: k.rate_limit_concurrent ?? null,
    rate_limit_tpm: k.rate_limit_tpm ?? null,
  }
  rpmMode.value = limitModeFromValue(k.rate_limit_rpm, true)
  concurrentMode.value = limitModeFromValue(k.rate_limit_concurrent, true)
  tpmMode.value = limitModeFromValue(k.rate_limit_tpm, false)
  limitsErr.value = ''
  limitsSuccess.value = ''
}

function openLimitsEditor() {
  initLimitsForm()
  showLimitsEditor.value = true
}

function modeToValue(mode: LimitMode, current: number | null | undefined): number | null {
  if (mode === 'default') return null
  if (mode === 'unlimited') return 0
  return current ?? null
}

function validateLimitsForm(): string | null {
  if (rpmMode.value === 'custom') {
    const v = limitsForm.value.rate_limit_rpm
    if (v == null || v < 1) return t('keys.detail.validation.rpmMin')
  }
  if (concurrentMode.value === 'custom') {
    const v = limitsForm.value.rate_limit_concurrent
    if (v == null || v < 1) return t('keys.detail.validation.concurrentMin')
  }
  if (tpmMode.value === 'custom') {
    const v = limitsForm.value.rate_limit_tpm
    if (v == null || v < 1) return t('keys.detail.validation.tpmMin')
  }
  return null
}

async function saveLimits() {
  limitsErr.value = ''
  limitsSuccess.value = ''
  const validationErr = validateLimitsForm()
  if (validationErr) {
    limitsErr.value = validationErr
    return
  }
  limitsSaving.value = true
  try {
    const data: UpdateKeyLimitsRequest = {
      rate_limit_rpm: modeToValue(rpmMode.value, limitsForm.value.rate_limit_rpm),
      rate_limit_concurrent: modeToValue(concurrentMode.value, limitsForm.value.rate_limit_concurrent),
      rate_limit_tpm: modeToValue(tpmMode.value, limitsForm.value.rate_limit_tpm),
    }
    await updateKeyLimits(keyId.value, data)
    limitsSuccess.value = t('keys.detail.limitsSaved')
    showLimitsEditor.value = false
    await loadKey()
  } catch (e: unknown) {
    limitsErr.value = e instanceof Error ? e.message : t('keys.common.saveFailed')
  } finally {
    limitsSaving.value = false
  }
}

// Time range (summary cards)
type PeriodType = 'minute' | 'hour' | 'day' | 'week' | 'month'
const periodOptions = computed<{ label: string; days: number }[]>(() => [
  { label: t('keys.detail.period.d1'), days: 1 },
  { label: t('keys.detail.period.d3'), days: 3 },
  { label: t('keys.detail.period.d7'), days: 7 },
  { label: t('keys.detail.period.d30'), days: 30 },
  { label: t('keys.detail.period.d90'), days: 90 },
])
const selectedPeriodDays = ref(7)

function periodLabelForDays(days: number): string {
  return periodOptions.value.find(o => o.days === days)?.label ?? t('keys.detail.period.custom', { days })
}

// Trend chart controls
type TrendMetric = 'requests' | 'cost'
const trendMetric = ref<TrendMetric>('requests')
const trendPeriod = ref<PeriodType>('hour')
const hoveredTrendIndex = ref<number | null>(null)

const CHART_W = 640
const CHART_H = 160
const CHART_PAD = { l: 44, r: 10, t: 8, b: 22 }
const trendPeriodOptions = computed<{ label: string; value: PeriodType }[]>(() => [
  { label: t('keys.detail.granularity.minute'), value: 'minute' },
  { label: t('keys.detail.granularity.hour'), value: 'hour' },
  { label: t('keys.detail.granularity.day'), value: 'day' },
  { label: t('keys.detail.granularity.week'), value: 'week' },
  { label: t('keys.detail.granularity.month'), value: 'month' },
])

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
  const periodLabel = trendPeriodOptions.value.find(o => o.value === trendPeriod.value)?.label ?? t('keys.detail.granularity.hour')
  if (useCustomRange.value && customStart.value && customEnd.value) {
    return `${periodLabel} · ${customStart.value} ~ ${customEnd.value}`
  }
  return `${periodLabel} · ${periodLabelForDays(selectedPeriodDays.value)}`
})

const trendGranularityHint = computed(() => {
  if (trendPeriod.value === 'minute') {
    return t('keys.detail.granularity.minuteHint')
  }
  if (trendPeriod.value === 'hour') {
    return t('keys.detail.granularity.hourHint')
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
  return selectedPeriodDays.value
}

function clampWindowForGranularity(period: PeriodType): boolean {
  if (period !== 'minute' && period !== 'hour') return false
  const limit = TREND_WINDOW_LIMITS[period]
  if (effectiveWindowDays() <= limit) return false
  if (useCustomRange.value) return false
  const matchDays = period === 'minute'
    ? Math.min(selectedPeriodDays.value, TREND_WINDOW_LIMITS.minute)
    : Math.min(selectedPeriodDays.value, TREND_WINDOW_LIMITS.hour)
  if (matchDays !== selectedPeriodDays.value) {
    selectedPeriodDays.value = matchDays
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

const chartViewBox = computed(() => `0 0 ${CHART_W} ${CHART_H}`)

const plotSize = computed(() => ({
  w: CHART_W - CHART_PAD.l - CHART_PAD.r,
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
  return Number(n).toLocaleString(localeRef.value, { minimumFractionDigits: decimals, maximumFractionDigits: decimals })
}

function formatRpmLimit(v: number | null | undefined): string {
  if (v == null) return t('keys.common.defaultLabel')
  if (v === 0) return t('keys.common.unlimited')
  return `${v} RPM`
}

function formatConcurrentLimit(v: number | null | undefined): string {
  if (v == null) return t('keys.common.defaultLabel')
  if (v === 0) return t('keys.common.unlimited')
  return String(v)
}

function formatTpmLimit(v: number | null | undefined): string {
  if (v == null || v === 0) return t('keys.common.noLimit')
  return `${fmtNum(v)} TPM`
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
    return `${fmtDateCompact(u.window_start)} ~ ${fmtDateCompact(end.toISOString())}`
  }
  if (useCustomRange.value && customStart.value && customEnd.value) {
    return `${customStart.value} ~ ${customEnd.value}`
  }
  return periodLabelForDays(selectedPeriodDays.value)
}

function trendTooltipStyle(p: { x: number; y: number }): Record<string, string> {
  const leftPct = Math.min(92, Math.max(8, (p.x / CHART_W) * 100))
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
    error.value = e instanceof Error ? e.message : t('keys.common.loadFailed')
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
      params.days = selectedPeriodDays.value
    }

    const [usage, models, trend] = await Promise.all([
      getKeyUsage(keyId.value, params),
      getKeyUsageByModel(keyId.value, { ...params, limit: 50 }),
      getKeyUsageTrend(keyId.value, trendPeriod.value, useCustomRange.value && customStart.value && customEnd.value
        ? { start: customStart.value, end: customEnd.value }
        : { days: selectedPeriodDays.value }),
    ])

    keyUsage.value = usage
    keyModels.value = models
    keyTrend.value = trend
  } catch (e: unknown) {
    detailError.value = e instanceof Error ? e.message : t('keys.detail.error.loadDetailFailed')
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
      <button class="btn btn-ghost" @click="router.push('/keys')">{{ t('keys.detail.backToList') }}</button>
      <h2 v-if="keyInfo">{{ t('keys.detail.titleWithPrefix', { prefix: keyInfo.key_prefix }) }}</h2>
      <h2 v-else>{{ t('keys.detail.title') }}</h2>
    </div>

    <div v-if="loading" class="empty">{{ t('keys.common.loading') }}</div>
    <div v-if="error" class="alert alert-danger">{{ error }}</div>

    <template v-if="keyInfo && !loading">
      <!-- Key info card -->
      <div class="card key-info-card">
        <div class="key-info-header">
          <span class="key-info-title">{{ t('keys.detail.keyInfoTitle') }}</span>
          <button class="btn btn-sm" @click="openLimitsEditor">{{ t('keys.detail.editLimitsBtn') }}</button>
        </div>
        <div class="key-info-row">
          <div class="key-info-item">
            <span class="key-info-label">{{ t('keys.common.application') }}</span>
            <span class="key-info-value">{{ keyInfo.application_code }}</span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">{{ t('keys.common.owner') }}</span>
            <span class="key-info-value">{{ keyInfo.owner_user ?? '—' }}</span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">{{ t('keys.common.statusLabel') }}</span>
            <span class="badge" :class="keyInfo.enabled ? 'badge-green' : 'badge-red'">
              {{ keyInfo.enabled ? t('keys.detail.enabled') : t('keys.detail.revoked') }}
            </span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">{{ t('keys.common.budget') }}</span>
            <span class="key-info-value">{{ keyInfo.budget_usd != null ? fmtCost(keyInfo.budget_usd) : t('keys.common.unlimited') }}</span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">{{ t('keys.detail.limitEditor.rpm') }}</span>
            <span class="key-info-value">{{ formatRpmLimit(keyInfo.rate_limit_rpm) }}</span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">{{ t('keys.common.concurrent') }}</span>
            <span class="key-info-value">{{ formatConcurrentLimit(keyInfo.rate_limit_concurrent) }}</span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">{{ t('keys.detail.limitEditor.tpm') }}</span>
            <span class="key-info-value">{{ formatTpmLimit(keyInfo.rate_limit_tpm) }}</span>
          </div>
          <div class="key-info-item">
            <span class="key-info-label">{{ t('keys.common.lastUsed') }}</span>
            <span class="key-info-value">{{ fmtDateTimeShort(keyInfo.last_used_at) }}</span>
          </div>
        </div>

<!-- Limit editor modal -->
        <div v-if="showLimitsEditor" class="modal-overlay" @click.self="showLimitsEditor = false">
          <div class="modal" style="max-width:450px" @click.stop>
            <h3>{{ t('keys.detail.limitEditor.title') }}</h3>
            <div v-if="limitsErr" class="alert alert-danger">{{ limitsErr }}</div>
            <div v-if="limitsSuccess" class="alert alert-success">{{ limitsSuccess }}</div>

            <div class="form-group">
              <label>{{ t('keys.detail.limitEditor.rpm') }}</label>
              <div class="limit-options">
                <label><input type="radio" v-model="rpmMode" value="default"> {{ t('keys.common.defaultLabel') }}</label>
                <label><input type="radio" v-model="rpmMode" value="unlimited"> {{ t('keys.common.unlimited') }}</label>
                <label><input type="radio" v-model="rpmMode" value="custom"> {{ t('keys.common.custom') }}</label>
              </div>
              <input v-if="rpmMode === 'custom'" v-model.number="limitsForm.rate_limit_rpm" type="number" min="1" :placeholder="t('keys.detail.limitEditor.rpmPlaceholder')">
            </div>

            <div class="form-group">
              <label>{{ t('keys.detail.limitEditor.concurrent') }}</label>
              <div class="limit-options">
                <label><input type="radio" v-model="concurrentMode" value="default"> {{ t('keys.common.defaultLabel') }}</label>
                <label><input type="radio" v-model="concurrentMode" value="unlimited"> {{ t('keys.common.unlimited') }}</label>
                <label><input type="radio" v-model="concurrentMode" value="custom"> {{ t('keys.common.custom') }}</label>
              </div>
              <input v-if="concurrentMode === 'custom'" v-model.number="limitsForm.rate_limit_concurrent" type="number" min="1" :placeholder="t('keys.detail.limitEditor.concurrentPlaceholder')">
            </div>

            <div class="form-group">
              <label>{{ t('keys.detail.limitEditor.tpm') }}</label>
              <div class="limit-options">
                <label><input type="radio" v-model="tpmMode" value="default"> {{ t('keys.common.noLimit') }}</label>
                <label><input type="radio" v-model="tpmMode" value="custom"> {{ t('keys.common.custom') }}</label>
              </div>
              <input v-if="tpmMode === 'custom'" v-model.number="limitsForm.rate_limit_tpm" type="number" min="1" :placeholder="t('keys.detail.limitEditor.tpmPlaceholder')">
            </div>

            <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px">
              <button class="btn btn-ghost" @click="showLimitsEditor = false" :disabled="limitsSaving">{{ t('keys.detail.limitEditor.cancel') }}</button>

              <button class="btn btn-primary" @click="saveLimits" :disabled="limitsSaving">
                {{ limitsSaving ? t('keys.detail.limitEditor.saving') : t('keys.detail.limitEditor.save') }}
              </button>
            </div>
          </div>
        </div>
      </div>

      <!-- Usage stats + trend -->
      <div class="card">
        <!-- Loading state -->
        <div v-if="detailLoading" class="empty">{{ t('keys.common.loading') }}</div>
        <div v-else-if="detailError" class="alert alert-danger">{{ detailError }}</div>

        <template v-else-if="keyUsage">
          <!-- Summary cards -->
          <div class="stats-grid">
            <div class="stat-card">
              <div class="stat-label">{{ t('keys.detail.stats.totalRequests') }}</div>
              <div class="stat-value">{{ fmtNum(keyUsage.total_requests) }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">{{ t('keys.detail.stats.totalPromptTokens') }}</div>
              <div class="stat-value">{{ fmtNum(keyUsage.total_prompt_tokens) }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">{{ t('keys.detail.stats.totalCompletionTokens') }}</div>
              <div class="stat-value">{{ fmtNum(keyUsage.total_completion_tokens) }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">{{ t('keys.detail.stats.totalTokens') }}</div>
              <div class="stat-value">{{ fmtNum(keyUsage.total_tokens) }}</div>
            </div>
            <div class="stat-card highlight">
              <div class="stat-label">{{ t('keys.detail.stats.totalCost') }}</div>
              <div class="stat-value cost">{{ fmtCost(keyUsage.total_cost_usd) }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">{{ t('keys.detail.stats.successRate') }}</div>
              <div class="stat-value">{{ keyUsage.total_requests > 0 ? (keyUsage.success_rate * 100).toFixed(1) + '%' : '—' }}</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">{{ t('keys.detail.stats.avgLatency') }}</div>
              <div class="stat-value">{{ keyUsage.avg_latency_ms.toFixed(0) }}ms</div>
            </div>
            <div class="stat-card">
              <div class="stat-label">{{ t('keys.detail.stats.modelCount') }}</div>
              <div class="stat-value">{{ keyUsage.unique_models }}</div>
            </div>
          </div>

          <!-- Time range info -->
          <div class="time-range-info">
            <span>{{ t('keys.detail.queryWindow') }}{{ fmtQueryWindow() }}</span>
            <span v-if="keyUsage.first_request_at || keyUsage.last_request_at" class="time-range-actual">
              {{ t('keys.detail.actualRange', { start: fmtDateTimeShort(keyUsage.first_request_at), end: fmtDateTimeShort(keyUsage.last_request_at) }) }}
            </span>
          </div>

          <!-- Trend chart -->
          <div class="section trend-section">
            <div class="trend-header">
              <div class="section-title">{{ t('keys.detail.trend.title') }}</div>
              <div class="trend-controls">
                <div class="trend-tabs" role="tablist" :aria-label="t('keys.detail.trend.tabsAria')">
                  <button
                    type="button"
                    class="btn btn-sm"
                    :class="trendMetric === 'requests' ? 'btn-primary' : 'btn-ghost'"
                    role="tab"
                    :aria-selected="trendMetric === 'requests'"
                    @click="trendMetric = 'requests'"
                  >{{ t('keys.detail.trend.count') }}</button>
                  <button
                    type="button"
                    class="btn btn-sm"
                    :class="trendMetric === 'cost' ? 'btn-primary' : 'btn-ghost'"
                    role="tab"
                    :aria-selected="trendMetric === 'cost'"
                    @click="trendMetric = 'cost'"
                  >{{ t('keys.detail.trend.cost') }}</button>
                </div>
                <span class="trend-divider" aria-hidden="true"></span>
                <div class="trend-window">
                  <button
                    v-for="opt in periodOptions"
                    :key="opt.label"
                    type="button"
                    class="btn btn-sm"
                    :class="selectedPeriodDays === opt.days && !useCustomRange ? 'btn-primary' : 'btn-ghost'"
                    @click="useCustomRange = false; selectedPeriodDays = opt.days; clampWindowForGranularity(trendPeriod); changePeriod()"
                  >{{ opt.label }}</button>
                  <button
                    type="button"
                    class="btn btn-sm"
                    :class="useCustomRange ? 'btn-primary' : 'btn-ghost'"
                    @click="useCustomRange = true; changePeriod()"
                  >{{ t('keys.detail.trend.customRange') }}</button>
                  <template v-if="useCustomRange">
                    <input type="date" v-model="customStart" @change="changePeriod" class="date-input">
                    <span class="range-sep">{{ t('keys.detail.trend.rangeSeparator') }}</span>
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
              <div class="trend-chart-wrap">
                <svg
                  class="trend-svg"
                  :viewBox="chartViewBox"
                  preserveAspectRatio="none"
                  role="img"
                  :aria-label="trendMetric === 'cost' ? t('keys.detail.trend.costChartAria') : t('keys.detail.trend.requestChartAria')"
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
                    :x2="CHART_W - CHART_PAD.r"
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
                      :r="chartPoints.length > 120 ? 0 : 2"
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
              <div class="trend-summary">
                <span>{{ trendSummaryLabel }} · {{ t('keys.detail.trend.summary', { count: keyTrend.length, total: fmtTrendValue(totalTrendValue) }) }}</span>
                <span v-if="!trendHasActivity" class="trend-summary-muted">{{ t('keys.detail.trend.summaryEmpty') }}</span>
                <span v-if="!trendTotalMatchesSummary" class="trend-summary-warn" :title="t('keys.detail.trend.warnTitle')">{{ t('keys.detail.trend.warnText') }}</span>
              </div>
            </div>
            <div v-else-if="keyUsage.total_requests > 0" class="empty small">
              {{ t('keys.detail.trend.emptyError') }}
            </div>
            <div v-else class="empty small">{{ t('keys.detail.trend.emptyNoRecord', { prefix: trendSummaryLabel }) }}</div>
          </div>

          <!-- Model breakdown -->
          <div class="section">
            <div class="section-title">{{ t('keys.detail.modelTable.title') }}</div>
            <table class="detail-table" v-if="keyModels.length > 0">
              <thead>
                <tr>
                  <th>{{ t('keys.detail.modelTable.model') }}</th>
                  <th>{{ t('keys.detail.modelTable.requests') }}</th>
                  <th>{{ t('keys.detail.modelTable.promptTokens') }}</th>
                  <th>{{ t('keys.detail.modelTable.completionTokens') }}</th>
                  <th>{{ t('keys.detail.modelTable.totalTokens') }}</th>
                  <th>{{ t('keys.detail.modelTable.cost') }}</th>
                  <th>{{ t('keys.detail.modelTable.successRate') }}</th>
                  <th>{{ t('keys.detail.modelTable.firstUsed') }}</th>
                  <th>{{ t('keys.detail.modelTable.lastUsed') }}</th>
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
                  <td style="font-size:11px">{{ fmtDateCompact(m.first_used_at) }}</td>
                  <td style="font-size:11px">{{ fmtDateCompact(m.last_used_at) }}</td>
                </tr>
              </tbody>
            </table>
            <div v-else class="empty small">{{ t('keys.detail.modelTable.empty') }}</div>
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
  flex-wrap: wrap;
  min-width: 0;
  /* 2026-07-07: nowrap → wrap, 移除 overflow-x: auto
     趋势按钮组在窄屏时自动折行，不再横向滚动 */
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

.trend-chart-wrap {
  position: relative;
  width: 100%;
  height: 160px;
}

.trend-svg {
  display: block;
  width: 100%;
  height: 100%;
}

.trend-axis-line {
  stroke: color-mix(in srgb, var(--muted) 50%, var(--border));
  stroke-width: 0.75;
}

.trend-grid-line {
  stroke: var(--border);
  stroke-width: 0.75;
  stroke-dasharray: 3 3;
  opacity: 0.45;
}

.trend-area {
  opacity: 0.1;
}

.trend-line {
  stroke-width: 1.25;
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
  font-size: 7px;
  font-weight: 400;
  font-family: inherit;
}

.trend-x-label {
  fill: var(--muted);
  font-size: 7px;
  font-weight: 400;
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
  box-shadow: 0 4px 12px var(--overlay-light);
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
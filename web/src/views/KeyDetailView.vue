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

type LimitMode = 'default' | 'unlimited' | 'custom'
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
  rpmMode.value = k.rate_limit_rpm == null ? 'default' : k.rate_limit_rpm === 0 ? 'unlimited' : 'custom'
  concurrentMode.value = k.rate_limit_concurrent == null ? 'default' : k.rate_limit_concurrent === 0 ? 'unlimited' : 'custom'
  tpmMode.value = k.rate_limit_tpm == null ? 'default' : k.rate_limit_tpm === 0 ? 'unlimited' : 'custom'
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

// Time range
type PeriodType = 'day' | 'week' | 'month'
const periodOptions: { label: string; value: PeriodType; days: number }[] = [
  { label: '最近 7 天', value: 'day', days: 7 },
  { label: '最近 30 天', value: 'day', days: 30 },
  { label: '最近 90 天', value: 'month', days: 90 },
]
const selectedPeriod = ref(periodOptions[0])
const trendPeriod = ref<PeriodType>('day')

// Custom date range
const useCustomRange = ref(false)
const customStart = ref('')
const customEnd = ref('')

// ── Computed ───────────────────────────────────────────────────────────────
const maxCost = computed(() => {
  if (keyTrend.value.length === 0) return 0
  return Math.max(...keyTrend.value.map(t => t.cost_usd))
})

const totalCost = computed(() => {
  if (keyTrend.value.length === 0) return 0
  return keyTrend.value.reduce((sum, t) => sum + t.cost_usd, 0)
})

// ── Helpers ────────────────────────────────────────────────────────────────
function fmtDate(s: string | null | undefined) {
  if (!s) return '—'
  return new Date(s).toLocaleString('zh-CN', { dateStyle: 'short', timeStyle: 'short' })
}

function fmtDateShort(s: string | null | undefined) {
  if (!s) return '—'
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

function trendBarWidth(cost: number): string {
  if (maxCost.value === 0) return '0%'
  return Math.max(2, (cost / maxCost.value) * 100).toFixed(1) + '%'
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
      getKeyUsageTrend(keyId.value, trendPeriod.value, useCustomRange.value ? 365 : selectedPeriod.value.days),
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
                <label><input type="radio" v-model="rpmMode" value="unlimited"> 无限制</label>
                <label><input type="radio" v-model="rpmMode" value="custom"> 自定义</label>
              </div>
              <input v-if="rpmMode === 'custom'" v-model.number="limitsForm.rate_limit_rpm" type="number" min="1" placeholder="输入 RPM">
            </div>

            <div class="form-group">
              <label>并发（同时请求数）</label>
              <div class="limit-options">
                <label><input type="radio" v-model="concurrentMode" value="default"> 默认</label>
                <label><input type="radio" v-model="concurrentMode" value="unlimited"> 无限制</label>
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

      <!-- Time range selector -->
      <div class="card">
        <div class="detail-toolbar">
          <div class="period-selector">
            <button
              v-for="opt in periodOptions"
              :key="opt.label"
              class="btn btn-sm"
              :class="selectedPeriod === opt && !useCustomRange ? 'btn-primary' : 'btn-ghost'"
              @click="useCustomRange = false; selectedPeriod = opt; changePeriod()"
            >
              {{ opt.label }}
            </button>
            <label class="custom-range-toggle">
              <input type="checkbox" v-model="useCustomRange" @change="changePeriod">
              自定义
            </label>
            <template v-if="useCustomRange">
              <input type="date" v-model="customStart" @change="changePeriod" class="date-input">
              <span>至</span>
              <input type="date" v-model="customEnd" @change="changePeriod" class="date-input">
            </template>
          </div>
        </div>

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
              <div class="stat-value">{{ (keyUsage.success_rate * 100).toFixed(1) }}%</div>
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
          <div class="time-range-info" v-if="keyUsage.first_request_at || keyUsage.last_request_at">
            数据范围：{{ fmtDate(keyUsage.first_request_at) }} ~ {{ fmtDate(keyUsage.last_request_at) }}
          </div>

          <!-- Trend chart -->
          <div class="section">
            <div class="section-title">
              费用趋势
              <select v-model="trendPeriod" @change="changePeriod" class="period-select">
                <option value="day">按天</option>
                <option value="week">按周</option>
                <option value="month">按月</option>
              </select>
            </div>
            <div class="trend-chart" v-if="keyTrend.length > 0">
              <div class="trend-bars">
                <div
                  v-for="t in keyTrend"
                  :key="t.period"
                  class="trend-bar-container"
                  :title="`${t.period}: ${fmtCost(t.cost_usd)} (${fmtNum(t.requests)} 请求)`"
                >
                  <div class="trend-bar" :style="{ height: trendBarWidth(t.cost_usd) }"></div>
                  <div class="trend-label">{{ fmtDateShort(t.period) }}</div>
                </div>
              </div>
              <div class="trend-summary">
                共 {{ keyTrend.length }} 个周期，合计 {{ fmtCost(totalCost) }}
              </div>
            </div>
            <div v-else class="empty small">暂无趋势数据</div>
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

.section {
  margin-top: 24px;
}

.section-title {
  font-size: 15px;
  font-weight: 600;
  margin-bottom: 12px;
  display: flex;
  align-items: center;
  gap: 12px;
}

.period-select {
  padding: 4px 8px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  font-size: 12px;
}

.trend-chart {
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 16px;
}

.trend-bars {
  display: flex;
  align-items: flex-end;
  gap: 3px;
  height: 100px;
  overflow-x: auto;
  padding-bottom: 8px;
}

.trend-bar-container {
  flex: 1;
  min-width: 24px;
  max-width: 60px;
  display: flex;
  flex-direction: column;
  align-items: center;
  height: 100%;
  justify-content: flex-end;
}

.trend-bar {
  width: 100%;
  background: var(--primary);
  border-radius: 3px 3px 0 0;
  min-height: 3px;
  transition: height 0.3s ease;
}

.trend-bar-container:hover .trend-bar {
  background: var(--primary-hover);
}

.trend-label {
  font-size: 10px;
  color: var(--muted);
  margin-top: 6px;
  text-align: center;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 100%;
}

.trend-summary {
  margin-top: 12px;
  font-size: 13px;
  color: var(--muted);
  text-align: center;
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
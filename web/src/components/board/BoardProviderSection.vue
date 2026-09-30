<script setup lang="ts">
// BoardProviderSection.vue — 供应商成本采购区（2026-09-30 看板重构轮，对齐效果图）。
// 三层：
//  1) 成本采购卡片：窗口成本取 getUsageByProvider.total_cost_usd（饼图 cost 经常是 0）；
//     积分按 provider_code 或名称回查 pies.providers。没有用量行就不拿饼图充卡。
//  2) 评分：复用对账报表接口 getReportSummary(view=provider) 的 quality_score；
//  3) 余额：凭据 balance_usd 汇总（getProviderCredentials，挂载后拉一次缓存，无数据显 —/套餐）。
// 供应商用量统计列表 = getUsageByProvider（随看板时间范围联动）。
// 交互：卡片/行点击 → /providers/:id 详情页；更多 → ProviderUsageExplorer 抽屉（保留）。
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import ProviderUsageExplorer from '../ProviderUsageExplorer.vue'
import type { BoardPayload } from '../../api/board'
import { getUsageByProvider, downloadProviderUsageExport, type ProviderUsageRow } from '../../api/usage'
import { getReportSummary } from '../../api/reportrollup'
import { getProviderCredentials } from '../../api/providers'
import { resolveBoardRangeMs, type BoardTimeRange, type BoardTimeQuery } from '../../utils/boardTimeRange'
import { buildProviderCards, fmtUsd, matchProviderPie } from './providerCards'

const props = defineProps<{
  board: BoardPayload | null | undefined
  timeRange: BoardTimeRange
  timeQuery: BoardTimeQuery
  loading?: boolean
}>()

const { t } = useI18n()
const router = useRouter()

const explorerOpen = ref(false)
const exportError = ref('')
const usageRows = ref<ProviderUsageRow[]>([])
const usageLoading = ref(false)
const usageError = ref('')
const qualityByName = ref<Map<string, number>>(new Map())
/** provider_id → 余额合计（undefined = 无数据）。挂载后拉一次，随供应商集合变化补拉。 */
const balanceById = ref<Map<number, number | 'plan' | undefined>>(new Map())

interface ProviderCard {
  code: string
  name: string
  id?: number
  requests: number
  tokens: number
  credits: number
  costUsd: number
  quality?: number
  balance: number | 'plan' | undefined
}

const AVATAR_VARS = ['var(--accent)', 'var(--success)', 'var(--warning)', 'var(--danger)']

function avatarVar(idx: number) {
  return AVATAR_VARS[idx % AVATAR_VARS.length]
}

function pieFor(code: string, name: string) {
  return matchProviderPie(props.board?.pies?.providers ?? [], code, name)
}

const providerCards = computed<ProviderCard[]>(() => {
  return buildProviderCards(usageRows.value, props.board?.pies?.providers ?? []).map((card) => ({
    ...card,
    quality: qualityByName.value.get(card.name),
    balance: card.id != null ? balanceById.value.get(card.id) : undefined,
  }))
})

const costMax = computed(() => Math.max(...providerCards.value.map((c) => c.costUsd), 1))

const tableRows = computed(() => {
  return [...usageRows.value]
    .sort((a, b) => (b.total_cost_usd ?? 0) - (a.total_cost_usd ?? 0))
    .map((r) => ({
      ...r,
      credits: pieFor(r.provider_code, r.provider_name)?.credits,
      quality: qualityByName.value.get(r.provider_name),
    }))
})

async function loadUsage() {
  usageLoading.value = true
  usageError.value = ''
  try {
    usageRows.value = await getUsageByProvider(props.timeQuery, 50)
  } catch (e: unknown) {
    usageRows.value = []
    usageError.value = e instanceof Error && e.message ? e.message : t('dashboard.loadError')
  } finally {
    usageLoading.value = false
  }
}

async function loadQuality() {
  try {
    const { startMs, endMs } = resolveBoardRangeMs(props.timeRange)
    const start = new Date(startMs).toISOString().slice(0, 10)
    const end = new Date(endMs - 1).toISOString().slice(0, 10)
    const report = await getReportSummary({ start, end, view: 'provider' })
    const map = new Map<string, number>()
    for (const row of report.providers ?? []) {
      map.set(row.provider_name, row.quality_score)
    }
    qualityByName.value = map
  } catch {
    qualityByName.value = new Map()
  }
}

const balanceFetchedFor = new Set<number>()

async function loadBalances() {
  const targets = usageRows.value.filter((r) => r.provider_id != null && !balanceFetchedFor.has(r.provider_id)).slice(0, 12)
  await Promise.all(
    targets.map(async (r) => {
      balanceFetchedFor.add(r.provider_id)
      try {
        const creds = await getProviderCredentials(r.provider_id)
        let sum = 0
        let hasBalance = false
        let hasPlan = false
        for (const c of creds) {
          const b = typeof c.balance_usd === 'string' ? Number(c.balance_usd) : c.balance_usd
          if (b != null && !Number.isNaN(b)) {
            sum += b
            hasBalance = true
          }
          if (c.plan_type) hasPlan = true
        }
        balanceById.value.set(r.provider_id, hasBalance ? sum : hasPlan ? 'plan' : undefined)
      } catch {
        balanceFetchedFor.delete(r.provider_id)
        balanceById.value.set(r.provider_id, undefined)
      }
    }),
  )
}

function openProvider(id?: number) {
  if (id == null) return
  router.push(`/providers/${id}`)
}

async function exportExcel() {
  exportError.value = ''
  try {
    await downloadProviderUsageExport(props.timeQuery)
  } catch (e: unknown) {
    exportError.value = e instanceof Error && e.message ? e.message : t('dashboard.loadError')
  }
}

function fmtBalance(b: number | 'plan' | undefined) {
  if (b === undefined) return '—'
  if (b === 'plan') return t('dashboard.board.providerPlan')
  return '$' + b.toFixed(2)
}

function fmtCompact(n: number | undefined) {
  if (n == null) return '—'
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K'
  return Number(n).toLocaleString()
}

function fmtPct(v: number | undefined) {
  if (v == null) return '—'
  return (Number(v) * 100).toFixed(1) + '%'
}

function scoreClass(q: number | undefined) {
  if (q == null) return ''
  if (q >= 80) return 'pv-score--good'
  if (q >= 60) return 'pv-score--mid'
  return 'pv-score--low'
}

onMounted(() => {
  void loadUsage()
  void loadQuality()
})

watch(
  () => props.timeQuery,
  () => {
    void loadUsage()
    void loadQuality()
  },
  { deep: true },
)

watch(usageRows, () => void loadBalances())
</script>

<template>
  <section class="pv-sec">
    <div class="pv-sec__bar">
      <h4 class="pv-sec__title">
        {{ t('dashboard.board.providerCostTitle') }}
        <span class="pv-count">{{ t('dashboard.board.providerCount', { n: providerCards.length }) }}</span>
      </h4>
      <button type="button" class="pv-more" @click="explorerOpen = true">
        {{ t('dashboard.providerUsage.more') }} →
      </button>
    </div>

    <p v-if="usageError" class="pv-export-error" role="alert">{{ usageError }}</p>
    <div v-if="usageLoading && !providerCards.length" class="pv-grid">
      <div v-for="i in 5" :key="i" class="pv-card pv-card--skeleton" />
    </div>
    <div v-else-if="providerCards.length" class="pv-grid">
      <div
        v-for="(card, i) in providerCards"
        :key="card.code"
        class="pv-card"
        :class="{ 'pv-card--clickable': card.id != null }"
        @click="openProvider(card.id)"
      >
        <div class="pv-card__name">
          <span class="pv-avatar" :style="{ background: avatarVar(i) }">{{ card.name.slice(0, 1).toUpperCase() }}</span>
          <span class="pv-card__name-text" :title="card.name">{{ card.name }}</span>
          <span v-if="card.id != null" class="pv-card__open" aria-hidden="true">{{ t('dashboard.board.providerDetail') }} ↗</span>
        </div>
        <div class="pv-card__cost">
          {{ fmtUsd(card.costUsd) }}
          <small>{{ t('dashboard.board.providerWindowCost') }}</small>
          <span v-if="card.quality != null" class="pv-score" :class="scoreClass(card.quality)">
            {{ t('dashboard.board.providerScore', { n: card.quality }) }}
          </span>
          <span v-else-if="tableRows.length === 0 && usageLoading" class="pv-score pv-score--loading">…</span>
        </div>
        <div class="pv-card__nums">
          <span>{{ t('dashboard.board.providerCredits') }} <b>{{ fmtCompact(card.credits) }}</b></span>
          <span>{{ t('dashboard.board.providerBalance') }} <b>{{ fmtBalance(card.balance) }}</b></span>
        </div>
        <div class="pv-bar" aria-hidden="true">
          <i :style="{ width: Math.max(3, (card.costUsd / costMax) * 100).toFixed(1) + '%', background: avatarVar(i) }"></i>
        </div>
      </div>
    </div>
    <div v-else class="pv-empty">{{ t('dashboard.board.empty') }}</div>

    <div class="pv-table-card">
      <div class="pv-sec__bar">
        <h4 class="pv-sec__title">
          {{ t('dashboard.board.providerStatsTitle') }}
          <span class="pv-count">{{ t('dashboard.board.providerStatsHint') }}</span>
        </h4>
        <button type="button" class="pv-more" @click="exportExcel">
          ⬇ {{ t('dashboard.board.providerExport') }}
        </button>
      </div>
      <p v-if="exportError" class="pv-export-error" role="alert">{{ exportError }}</p>
      <div class="pv-table-wrap">
        <table class="pv-list">
          <thead>
            <tr>
              <th>{{ t('dashboard.providerUsage.colName') }}</th>
              <th>{{ t('dashboard.providerUsage.colCode') }}</th>
              <th class="num">{{ t('dashboard.providerUsage.colRequests') }}</th>
              <th class="num">{{ t('dashboard.providerUsage.colTokens') }}</th>
              <th class="num">{{ t('dashboard.providerUsage.colCost') }}</th>
              <th class="num">{{ t('dashboard.board.providerCredits') }}</th>
              <th class="num">{{ t('dashboard.providerUsage.colSuccess') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-if="!tableRows.length">
              <td colspan="7" class="pv-table__empty">{{ usageLoading ? t('dashboard.loading') : t('dashboard.board.empty') }}</td>
            </tr>
            <tr v-for="row in tableRows" :key="row.provider_id" class="pv-row" @click="openProvider(row.provider_id)">
              <td><b>{{ row.provider_name }}</b></td>
              <td><code class="pv-code">{{ row.provider_code }}</code></td>
              <td class="num">{{ fmtCompact(row.request_count) }}</td>
              <td class="num">{{ fmtCompact((row.prompt_tokens ?? 0) + (row.completion_tokens ?? 0)) }}</td>
              <td class="num">{{ fmtUsd(row.total_cost_usd) }}</td>
              <td class="num">{{ fmtCompact(row.credits) }}</td>
              <td class="num">
                <span :style="{ color: row.success_rate != null && row.success_rate >= 0.95 ? 'var(--success)' : 'var(--warning)' }" class="pv-sr">
                  {{ fmtPct(row.success_rate) }}
                </span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <ProviderUsageExplorer
      :open="explorerOpen"
      :time-range="timeRange"
      :time-query="timeQuery"
      @close="explorerOpen = false"
    />
  </section>
</template>

<style scoped>
.pv-sec {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.pv-sec__bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  flex-wrap: wrap;
}
.pv-sec__title {
  font-size: 13.5px;
  font-weight: 700;
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0;
}
.pv-count {
  font-size: 10.5px;
  color: var(--text-muted);
  background: color-mix(in srgb, var(--text-muted) 12%, transparent);
  border-radius: 999px;
  padding: 1px 8px;
  font-weight: 600;
}
.pv-more {
  border: 1px solid var(--accent);
  border-radius: 8px;
  padding: 5px 12px;
  font-size: 12.5px;
  font-weight: 600;
  color: var(--accent);
  background: color-mix(in srgb, var(--accent) 10%, transparent);
  cursor: pointer;
  transition: background 0.15s;
}
.pv-more:hover {
  background: color-mix(in srgb, var(--accent) 18%, transparent);
}
.pv-grid {
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: 10px;
}
.pv-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 11px 13px;
  min-width: 0;
  transition: border-color 0.15s, box-shadow 0.15s;
}
.pv-card--clickable {
  cursor: pointer;
}
.pv-card--clickable:hover {
  border-color: color-mix(in srgb, var(--accent) 55%, var(--border));
  box-shadow: 0 0 0 1px color-mix(in srgb, var(--accent) 22%, transparent);
}
.pv-card--skeleton {
  min-height: 96px;
  background: linear-gradient(90deg, var(--border) 25%, transparent 37%, var(--border) 63%);
  background-size: 400% 100%;
  animation: pv-shimmer 1.2s ease infinite;
}
@keyframes pv-shimmer {
  0% { background-position: 100% 0; }
  100% { background-position: 0 0; }
}
.pv-card__name {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 12.5px;
  font-weight: 700;
  min-width: 0;
}
.pv-card__name-text {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.pv-avatar {
  width: 20px;
  height: 20px;
  border-radius: 6px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  font-size: 10px;
  font-weight: 800;
  color: var(--on-primary);
  flex-shrink: 0;
}
.pv-card__open {
  margin-left: auto;
  font-size: 11px;
  color: var(--text-muted);
  font-weight: 600;
  flex-shrink: 0;
}
.pv-card__cost {
  font-size: 16px;
  font-weight: 700;
  margin-top: 6px;
  font-variant-numeric: tabular-nums;
  display: flex;
  align-items: baseline;
  gap: 6px;
  flex-wrap: wrap;
}
.pv-card__cost small {
  font-size: 10.5px;
  color: var(--text-muted);
  font-weight: 500;
}
.pv-score {
  font-size: 10.5px;
  font-weight: 700;
  padding: 1.5px 7px;
  border-radius: 999px;
  margin-left: auto;
  align-self: center;
}
.pv-score--good {
  color: var(--success);
  background: color-mix(in srgb, var(--success) 13%, transparent);
}
.pv-score--mid {
  color: var(--warning);
  background: color-mix(in srgb, var(--warning) 14%, transparent);
}
.pv-score--low {
  color: var(--danger);
  background: color-mix(in srgb, var(--danger) 13%, transparent);
}
.pv-score--loading {
  color: var(--text-muted);
  background: transparent;
}
.pv-card__nums {
  display: flex;
  gap: 12px;
  margin-top: 5px;
  font-size: 11px;
  color: var(--text-muted);
  flex-wrap: wrap;
}
.pv-card__nums b {
  color: var(--text);
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.pv-bar {
  height: 4px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--text-muted) 18%, transparent);
  margin-top: 9px;
  overflow: hidden;
}
.pv-bar i {
  display: block;
  height: 100%;
  border-radius: 999px;
}
.pv-empty {
  font-size: 12.5px;
  color: var(--text-muted);
  padding: 18px 0;
  text-align: center;
  border: 1px dashed var(--border);
  border-radius: 12px;
}
.pv-table-card {
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 13px 15px;
}
.pv-table-wrap {
  overflow-x: auto;
}
.pv-list {
  width: 100%;
  border-collapse: collapse;
  font-size: 12.5px;
  min-width: 640px;
}
.pv-list th {
  text-align: left;
  font-size: 11px;
  color: var(--text-muted);
  font-weight: 700;
  padding: 7px 10px;
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
.pv-list th.num,
.pv-list td.num {
  text-align: right;
  font-variant-numeric: tabular-nums;
}
.pv-list td {
  padding: 8px 10px;
  border-bottom: 1px solid color-mix(in srgb, var(--border) 55%, transparent);
}
.pv-list tbody tr:last-child td {
  border-bottom: 0;
}
.pv-row {
  cursor: pointer;
}
.pv-row:hover td {
  background: color-mix(in srgb, var(--accent) 5%, transparent);
}
.pv-code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 11px;
  color: var(--text-muted);
}
.pv-sr {
  font-weight: 700;
}
.pv-table__empty {
  text-align: center;
  color: var(--text-muted);
}
.pv-export-error {
  margin: 0;
  font-size: 12px;
  color: var(--danger);
}
@media (max-width: 1440px) {
  .pv-grid {
    grid-template-columns: repeat(3, 1fr);
  }
}
@media (max-width: 768px) {
  .pv-grid {
    grid-template-columns: repeat(2, 1fr);
  }
}
@media (max-width: 480px) {
  .pv-grid {
    grid-template-columns: 1fr;
  }
}
</style>

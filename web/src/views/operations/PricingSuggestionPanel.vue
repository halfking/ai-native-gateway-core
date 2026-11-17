<script setup lang="ts">
// PricingSuggestionPanel.vue —— 定价建议面（2026-11-17 定价对比完善轮）。
//
// 需求原文（owner 2026-11-17）：
//   「在进行销售定价时，可以使用复制某个 MaaS 的价格或平均价格，或最低价格等
//     策略，快速地更新本地的销售价格……也可以将本地模型的价格与采集的
//     maas 中的模型价格进行对比，根据策略给出建议方案，可以一键执行。
//     简化价格的操作。并且要有价格变更的历史，用于审计分析。」
//
// 三段式（与需求逐条对应）：
//   ① 策略选择（复制某平台 / 平均 / 最低 / 地板×加成）→ 预览建议；
//   ② 建议表：当前零售价 vs 建议价 + 幅度 + 依据（可执行 / 不可执行 + 原因）；
//   ③ 一键执行（confirm 闸）→ 写入本地零售价 + 留期次/批次痕迹 + 变更历史。
//
// ★ 红线（与采集侧同源、语义不同）：
//   自动同步**不回流**计费面；这里是**显式**运营动作 ⇒ 执行按钮必须二次确认。
// ★ NULL≠0：没有可比行情的建议一律显示「—」并标出原因，不落 0。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  postAdminMarketPricingSuggestions,
  postAdminMarketPricingApply,
  getAdminMarketPricingBatches,
  type PricingSuggestionResp,
  type PricingSuggestionRow,
  type PricingStrategy,
  type PricingSuggestionBatchRow,
} from '../../api/market'
import ResponsiveDataView from '../../components/ui/ResponsiveDataView.vue'
import type { CardField } from '../../components/ui/CardList.vue'
import { useActionMessage } from '../../composables/useActionMessage'
import { confirmDialog } from '../../composables/useConfirmDialog'

const { t } = useI18n()
const { notifySuccess, notifyError } = useActionMessage()

const strategy = ref<PricingStrategy>('min')
const platformId = ref('')
const markupPct = ref<number>(20)
const minDeltaPct = ref<number>(0)
const dimension = ref('standard')

const loading = ref(false)
const applying = ref(false)
const result = ref<PricingSuggestionResp | null>(null)
const errorMsg = ref('')
const batches = ref<PricingSuggestionBatchRow[]>([])
const batchesFailed = ref(false)

const STRATEGIES: PricingStrategy[] = ['copy_platform', 'average', 'min', 'floor_markup']

async function preview() {
  loading.value = true
  errorMsg.value = ''
  try {
    const body = {
      strategy: strategy.value,
      platform_id: strategy.value === 'copy_platform' ? platformId.value : '',
      dimension: dimension.value,
      markup_pct: strategy.value === 'floor_markup' ? markupPct.value : undefined,
      min_delta_pct: minDeltaPct.value || undefined,
    }
    result.value = await postAdminMarketPricingSuggestions(body)
  } catch {
    errorMsg.value = t('priceCompare.suggestLoadFailed')
    result.value = null
  } finally {
    loading.value = false
  }
}

async function loadBatches() {
  batchesFailed.value = false
  try {
    const res = await getAdminMarketPricingBatches(20)
    batches.value = res.items || []
  } catch {
    batchesFailed.value = true
    batches.value = []
  }
}

async function applyAll() {
  // ★ 2026-11-18 12h 审计轮根修（P1）：批量改价必须先经**二次确认**。
  //   原实现直接把 `confirm: true` 硬编码在请求体里 ⇒ 服务端的「二次确认闸」
  //   （admin/pricing_suggestion.go：`if !req.Confirm { 400 }`）被前端自己
  //   无条件满足 ⇒ 那道闸形同虚设 ⇒ 误点一次「一键执行（347 个模型）」就直接
  //   改掉 347 个模型的零售价，**无提示、无撤销**。
  //   ⇒ 现在走全站统一的 confirmDialog（仓内既有约定，见 DataLifecycleView.vue），
  //     确认之后才发请求。
  if (!(await confirmDialog(t('priceCompare.suggestApplyConfirm', { n: applicableRows.value.length })))) {
    return
  }
  applying.value = true
  errorMsg.value = ''
  try {
    const body = {
      strategy: strategy.value,
      platform_id: strategy.value === 'copy_platform' ? platformId.value : '',
      dimension: dimension.value,
      markup_pct: strategy.value === 'floor_markup' ? markupPct.value : undefined,
      min_delta_pct: minDeltaPct.value || undefined,
      confirm: true,
    }
    const res = await postAdminMarketPricingApply(body)
    result.value = res
    notifySuccess(t('priceCompare.suggestApplyOk', { n: res.summary.applied, batch: res.batch_id }))
    await loadBatches()
  } catch {
    errorMsg.value = t('priceCompare.suggestApplyFailed')
  } finally {
    applying.value = false
  }
}

onMounted(() => {
  preview()
  loadBatches()
})

const rows = computed<PricingSuggestionRow[]>(() => result.value?.items || [])
const applicableRows = computed(() => rows.value.filter(r => r.applicable))

function fmtNum(v: number | null | undefined, digits = 2): string {
  if (v === null || v === undefined) return '—'
  return Number(v).toFixed(digits)
}

function fmtDelta(v: number | null | undefined): string {
  if (v === null || v === undefined) return '—'
  const n = Number(v)
  return `${n > 0 ? '+' : ''}${n.toFixed(1)}%`
}

function deltaClass(v: number | null | undefined): string {
  if (v === null || v === undefined) return 'badge-gray'
  const n = Number(v)
  if (n > 0.5) return 'badge-red'
  if (n < -0.5) return 'badge-green'
  return 'badge-gray'
}

function strategyLabel(s: string): string {
  const map: Record<string, string> = {
    copy_platform: 'priceCompare.strategyCopy',
    average: 'priceCompare.strategyAverage',
    min: 'priceCompare.strategyMin',
    floor_markup: 'priceCompare.strategyFloorMarkup',
  }
  return t(map[s] || s)
}

const suggestionFields = computed<CardField[]>(() => [
  { key: 'canonical_name', label: t('priceCompare.colModel') },
  { key: 'suggested_credits_in_per_1m', label: t('priceCompare.suggestCredits') },
  { key: 'suggested_cny_in_per_1m', label: t('priceCompare.suggestCNY') },
  { key: 'delta_pct_out', label: t('priceCompare.suggestDelta') },
  { key: 'basis', label: t('priceCompare.suggestBasis') },
])
</script>

<template>
  <div class="suggest-panel">
    <!-- ① 策略条 -->
    <div class="card filter-card">
      <label class="form-row">
        <span>{{ t('priceCompare.suggestStrategy') }}</span>
        <select v-model="strategy">
          <option v-for="s in STRATEGIES" :key="s" :value="s">{{ strategyLabel(s) }}</option>
        </select>
      </label>
      <label v-if="strategy === 'copy_platform'" class="form-row">
        <span>{{ t('priceCompare.suggestPlatform') }}</span>
        <input v-model="platformId" type="text" :placeholder="t('priceCompare.suggestPlatformPlaceholder')" />
      </label>
      <label v-if="strategy === 'floor_markup'" class="form-row">
        <span>{{ t('priceCompare.suggestMarkup') }}</span>
        <input v-model.number="markupPct" type="number" min="-99" step="1" />
      </label>
      <label class="form-row">
        <span>{{ t('priceCompare.suggestThreshold') }}</span>
        <input v-model.number="minDeltaPct" type="number" min="0" step="1" />
      </label>
      <button class="btn btn-primary btn-sm" :disabled="loading" @click="preview">
        {{ t('priceCompare.suggestPreview') }}
      </button>
      <button
        class="btn btn-sm"
        :class="applicableRows.length ? 'btn-primary' : 'btn-ghost'"
        :disabled="applying || !applicableRows.length"
        @click="applyAll"
      >
        {{ t('priceCompare.suggestApplyAll', { n: applicableRows.length }) }}
      </button>
    </div>

    <p v-if="errorMsg" class="action-message error">{{ errorMsg }}</p>

    <!-- ② 建议表 -->
    <div v-if="loading" class="card hint-line">…</div>
    <div v-else-if="!result" class="card app-empty-state">
      <p>{{ t('priceCompare.suggestEmptyWhy') }}</p>
      <p class="empty-next">{{ t('priceCompare.suggestEmptyNext') }}</p>
    </div>
    <template v-else>
      <div class="card summary-card">
        <div class="summary-row">
          <span class="badge badge-gray">{{ t('priceCompare.suggestModels', { n: result.summary.models }) }}</span>
          <span class="badge badge-green">{{ t('priceCompare.suggestWithMarket', { n: result.summary.with_market }) }}</span>
          <span class="badge badge-gray">{{ t('priceCompare.suggestWithoutMarket', { n: result.summary.without_market }) }}</span>
          <span class="badge badge-blue">{{ t('priceCompare.suggestChanged', { n: result.summary.changed }) }}</span>
        </div>
        <p class="hint-line">{{ t('priceCompare.suggestSummaryHint') }}</p>
        <p v-for="(w, i) in result.warnings" :key="i" class="hint-line warn-line">{{ w }}</p>
      </div>

      <div v-if="!rows.length" class="card app-empty-state">
        <p>{{ t('priceCompare.suggestEmptyWhy') }}</p>
        <p class="empty-next">{{ t('priceCompare.suggestEmptyNext') }}</p>
      </div>
      <div v-else class="card table-wrap">
        <div class="table-head-row">
          <h3 class="section-title">{{ t('priceCompare.suggestTableTitle') }}</h3>
          <span class="badge badge-gray">{{ rows.length }}</span>
        </div>
        <ResponsiveDataView
          :rows="rows"
          title-key="canonical_name"
          :title-format="row => String(row.display_name || row.canonical_name)"
          :fields="suggestionFields"
          :loading="false"
          :empty="false"
        >
          <template #table>
            <table class="data-table">
              <thead>
                <tr>
                  <th>{{ t('priceCompare.colModel') }}</th>
                  <th>{{ t('priceCompare.suggestCurrent') }}</th>
                  <th>{{ t('priceCompare.suggestProposed') }}</th>
                  <th>{{ t('priceCompare.suggestDelta') }}</th>
                  <th>{{ t('priceCompare.suggestBasis') }}</th>
                  <th>{{ t('priceCompare.suggestState') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in rows" :key="r.canonical_name">
                  <td>
                    <div>{{ r.display_name || r.canonical_name }}</div>
                    <code class="mono-sm">{{ r.canonical_name }}</code>
                  </td>
                  <td class="num">
                    {{ fmtNum(r.current_cny_in_per_1m) }} / {{ fmtNum(r.current_cny_out_per_1m) }} ¥
                    <div class="mono-sm sub">{{ fmtNum(r.current_credits_in_per_1m, 0) }} / {{ fmtNum(r.current_credits_out_per_1m, 0) }} cr</div>
                  </td>
                  <td class="num">
                    {{ fmtNum(r.suggested_cny_in_per_1m) }} / {{ fmtNum(r.suggested_cny_out_per_1m) }} ¥
                    <div class="mono-sm sub">{{ fmtNum(r.suggested_credits_in_per_1m, 0) }} / {{ fmtNum(r.suggested_credits_out_per_1m, 0) }} cr</div>
                  </td>
                  <td class="num">
                    <span class="badge" :class="deltaClass(r.delta_pct_out)">{{ fmtDelta(r.delta_pct_out) }}</span>
                    <div class="mono-sm sub">{{ fmtDelta(r.delta_pct_in) }}</div>
                  </td>
                  <td class="num basis-cell">
                    <div>{{ r.basis || '—' }}</div>
                    <div class="mono-sm sub">{{ t('priceCompare.suggestPlatforms', { n: r.basis_platforms }) }} · {{ r.run_date || '—' }}</div>
                  </td>
                  <td>
                    <span v-if="r.applicable" class="badge badge-blue">{{ t('priceCompare.suggestStateApply') }}</span>
                    <span v-else class="badge badge-gray" :title="r.skip_reason">{{ t('priceCompare.suggestStateSkip') }}</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </template>
          <template #cards="{ row }">
            <div class="suggest-card">
              <p><b>{{ row.display_name || row.canonical_name }}</b></p>
              <p class="hint-line">{{ t('priceCompare.suggestCurrent') }}: {{ fmtNum(row.current_cny_in_per_1m) }} / {{ fmtNum(row.current_cny_out_per_1m) }} ¥</p>
              <p class="hint-line">{{ t('priceCompare.suggestProposed') }}: {{ fmtNum(row.suggested_cny_in_per_1m) }} / {{ fmtNum(row.suggested_cny_out_per_1m) }} ¥</p>
              <p class="hint-line">{{ t('priceCompare.suggestDelta') }}: {{ fmtDelta(row.delta_pct_out) }}</p>
              <p class="hint-line">{{ t('priceCompare.suggestBasis') }}: {{ row.basis || '—' }}</p>
            </div>
          </template>
        </ResponsiveDataView>
        <p class="hint-line">{{ t('priceCompare.suggestHint') }}</p>
      </div>
    </template>

    <!-- ③ 变更历史（批次留痕） -->
    <div class="card table-wrap">
      <div class="table-head-row">
        <h3 class="section-title">{{ t('priceCompare.suggestHistoryTitle') }}</h3>
        <span class="badge badge-gray">{{ batches.length }}</span>
      </div>
      <div v-if="batchesFailed" class="app-empty-state">
        <p>{{ t('priceCompare.suggestHistoryFailed') }}</p>
      </div>
      <div v-else-if="!batches.length" class="app-empty-state">
        <p>{{ t('priceCompare.suggestHistoryEmpty') }}</p>
      </div>
      <table v-else class="data-table">
        <thead>
          <tr>
            <th>{{ t('priceCompare.suggestHistoryBatch') }}</th>
            <th>{{ t('priceCompare.suggestHistoryTime') }}</th>
            <th>{{ t('priceCompare.suggestStrategy') }}</th>
            <th>{{ t('priceCompare.suggestHistoryOperator') }}</th>
            <th>{{ t('priceCompare.suggestHistoryResult') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="b in batches" :key="b.id">
            <td>#{{ b.id }}</td>
            <td class="mono-sm">{{ b.created_at }}</td>
            <td>{{ strategyLabel(b.strategy) }}</td>
            <td>{{ b.operator || '—' }}</td>
            <td class="num">{{ b.applied }} / {{ b.requested }}</td>
          </tr>
        </tbody>
      </table>
      <p class="hint-line">{{ t('priceCompare.suggestHistoryHint') }}</p>
    </div>
  </div>
</template>

<style scoped>
.filter-card { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.form-row { display: flex; align-items: center; gap: 6px; }
.form-row > span { font-size: 12px; color: var(--text-secondary, #666); white-space: nowrap; }
.form-row input[type="text"] { width: 140px; }
.form-row input[type="number"] { width: 80px; }
.form-row select { min-width: 150px; }
.summary-card { display: flex; flex-direction: column; gap: 6px; }
.summary-row { display: flex; flex-wrap: wrap; gap: 8px; }
.sub { color: var(--text-secondary, #888); font-size: 11px; }
.basis-cell { font-size: 11px; }
.warn-line { color: #b26a00; }
.action-message { font-size: 13px; margin-top: 8px; }
.action-message.error { color: #c0392b; }
.hint-line, .empty-next { font-size: 12px; color: var(--text-secondary, #666); }
.suggest-card p { margin: 4px 0; }
</style>

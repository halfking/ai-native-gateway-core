<script setup lang="ts">
// UsageView — 用量：Tab 停靠（07 §5 简化：点按切换，不做横滑）+ 汇总卡 +
// 按模型宽表（专注模式）。Tab 独立数据、共享一次拉取。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useHyperPage } from '@/hyper'
import {
  fetchCostTrend,
  fetchUsageByModel,
  fetchUsageSummary,
  readCostTrend,
  unwrapModelUsage,
  type CostTrendDimension,
  type CostTrendReading,
  type ModelUsage,
  type UsageSummary,
} from '@/api/usage'
import { t } from '@/i18n'
import AppStateView from '@/components/common/AppStateView.vue'
import StatCard from '@/components/common/StatCard.vue'
import FocusLayer from '@/components/common/FocusLayer.vue'
import AppIcon from '@/components/common/AppIcon.vue'
import { fmtInt, fmtNum, fmtUsd } from '@/utils/format'

useHyperPage({ title: () => t('usage.title') })

const tab = ref<'summary' | 'byModel' | 'costTrend'>('summary')
const summary = ref<UsageSummary | null>(null)
const modelItems = ref<ModelUsage[]>([])
const degraded = ref(false)
const loading = ref(true)
const error = ref<string | null>(null)
const focusActive = ref(false)

let aborter: AbortController | null = null

async function load(): Promise<void> {
  aborter?.abort()
  aborter = new AbortController()
  const signal = aborter.signal
  error.value = null
  try {
    const [s, m] = await Promise.all([fetchUsageSummary(7, { signal }), fetchUsageByModel(7, { signal })])
    summary.value = s
    const unwrapped = unwrapModelUsage(m)
    modelItems.value = unwrapped.items
    degraded.value = unwrapped.degraded
  } catch (err) {
    if ((err as { name?: string })?.name === 'AbortError') return
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (!signal.aborted) loading.value = false
  }
}

onMounted(() => {
  void load()
})
onBeforeUnmount(() => {
  aborter?.abort()
})

const sortedModels = computed(() => [...modelItems.value].sort((a, b) => b.total_requests - a.total_requests))

// ── 成本趋势（cost-trend）─────────────────────────────────────────────
// ★ 降级与「真的零花费」必须分开：后端可选视图未迁移时返回 200 +
//   空 entries + total_cost 0（usage_enhanced.go:211-229）。若不判 degraded，
//   一次系统降级会被显示成「这周没花钱」。
const costGroupBy = ref<CostTrendDimension>('model')
const costTrend = ref<CostTrendReading | null>(null)
const costLoading = ref(false)
const costError = ref<string | null>(null)

const COST_DIMS: CostTrendDimension[] = ['model', 'provider', 'key', 'application', 'tenant']

async function loadCostTrend(): Promise<void> {
  costLoading.value = true
  costError.value = null
  try {
    costTrend.value = readCostTrend(await fetchCostTrend(costGroupBy.value, 7))
  } catch (err) {
    if ((err as { name?: string })?.name === 'AbortError') return
    costError.value = err instanceof Error ? err.message : String(err)
    costTrend.value = null
  } finally {
    costLoading.value = false
  }
}

async function onCostDimChange(d: CostTrendDimension): Promise<void> {
  if (costGroupBy.value === d) return
  costGroupBy.value = d
  await loadCostTrend()
}
</script>

<template>
  <div class="page usage">
    <!-- Tab 停靠：.page 即滚动宿主，sticky top:0 = 顶栏下沿（07 §4 effectiveTop） -->
    <div class="usage__tabs" role="tablist">
      <button
        type="button"
        role="tab"
        class="usage__tab"
        :class="{ 'usage__tab--active': tab === 'summary' }"
        :aria-selected="tab === 'summary'"
        @click="tab = 'summary'"
      >
        {{ t('usage.summary') }}
      </button>
      <button
        type="button"
        role="tab"
        class="usage__tab"
        :class="{ 'usage__tab--active': tab === 'byModel' }"
        :aria-selected="tab === 'byModel'"
        @click="tab = 'byModel'"
      >
        {{ t('usage.byModel') }}
      </button>
      <button
        type="button"
        role="tab"
        class="usage__tab"
        :class="{ 'usage__tab--active': tab === 'costTrend' }"
        :aria-selected="tab === 'costTrend'"
        @click="tab = 'costTrend'; void loadCostTrend()"
      >
        {{ t('usage.costTrend') }}
      </button>
    </div>

    <AppStateView :loading="loading" :error="error" @retry="load">
      <template v-if="tab === 'summary' && summary">
        <!-- ★ 2026-10-06：原先降级只在「请求数」一张卡的 hint 上提示，其余 5 张卡
             照常显示 0（tokens / cost / credits / 成功率 / 延迟）。
             ⇒ 一次后端视图缺失会被读成「这段时间真的一分钱没花、零调用」。
             现在降级是**整块**声明：降级时不再展示那些无依据的 0。 -->
        <p v-if="summary.degraded" class="usage__degraded" role="status">
          <span class="badge badge--warning">{{ t('home.degraded') }}</span>
          {{ t('usage.summaryDegraded') }}
        </p>
        <div v-else class="usage__grid">
          <StatCard :label="t('home.totalRequests')" :value="fmtInt(summary.total_requests)" :hint="summary.degraded ? t('home.summaryMissing') : undefined" />
          <StatCard :label="t('home.totalTokens')" :value="fmtInt(summary.total_prompt_tokens + summary.total_completion_tokens)" />
          <StatCard :label="t('home.totalCost')" :value="fmtUsd(summary.total_cost_usd)" />
          <StatCard :label="t('home.creditsCharged')" :value="fmtInt(summary.total_credits_charged)" />
          <StatCard :label="t('home.successRate')" :value="summary.success_rate != null ? fmtNum(summary.success_rate, 2) + '%' : '—'" />
          <StatCard :label="t('home.avgLatency')" :value="summary.avg_latency_ms != null ? fmtInt(Math.round(summary.avg_latency_ms)) + 'ms' : '—'" />
        </div>
        <p class="usage__period">{{ t('usage.period') }}</p>
      </template>

      <template v-else-if="tab === 'byModel'">
        <div class="usage__focus-head">
          <span v-if="degraded" class="badge badge--warning">{{ t('home.degraded') }}</span>
          <button type="button" class="btn btn--sm" @click="focusActive = true">
            <AppIcon name="expand" :size="16" />
            {{ t('common.focusView') }}
          </button>
        </div>

        <FocusLayer v-model:active="focusActive" :title="t('usage.byModel')">
          <div class="table-scroll">
            <table class="table">
              <thead>
                <tr>
                  <th>{{ t('usage.model') }}</th>
                  <th class="num">{{ t('usage.requests') }}</th>
                  <th class="num">{{ t('usage.tokens') }}</th>
                  <th class="num">{{ t('usage.cost') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="m in sortedModels" :key="m.model">
                  <td>{{ m.model }}</td>
                  <td class="num">{{ fmtInt(m.total_requests) }}</td>
                  <td class="num">{{ fmtInt(m.total_tokens) }}</td>
                  <td class="num">{{ fmtUsd(m.total_cost_usd) }}</td>
                </tr>
                <tr v-if="sortedModels.length === 0">
                  <td colspan="4">{{ t('common.empty') }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </FocusLayer>
      </template>

      <!-- 成本趋势：降级 / 真空 / 有数据 三态严格分开 -->
      <template v-else-if="tab === 'costTrend'">
        <div class="usage__dims">
          <button
            v-for="d in COST_DIMS"
            :key="d"
            type="button"
            class="usage__dim"
            :class="{ 'usage__dim--on': costGroupBy === d }"
            @click="onCostDimChange(d)"
          >
            {{ t(`usage.dim.${d}`) }}
          </button>
        </div>

        <p v-if="costLoading" class="usage__hint-line">{{ t('common.loading') }}</p>
        <p v-else-if="costError" class="usage__hint-line usage__hint-line--err">{{ costError }}</p>

        <template v-else-if="costTrend">
          <!-- 三态各自的文案，绝不混用 -->
          <p v-if="costTrend.kind === 'degraded'" class="usage__degraded" role="status">
            <span class="badge badge--warning">{{ t('home.degraded') }}</span>
            {{ t('usage.costTrendDegraded') }}
            <span v-if="costTrend.reason" class="usage__degraded-reason">{{ costTrend.reason }}</span>
          </p>
          <p v-else-if="costTrend.kind === 'empty'" class="usage__hint-line">
            {{ t('usage.costTrendEmpty') }}
          </p>
          <template v-else>
            <p class="usage__cost-total">
              {{ t('usage.totalCostLabel') }} {{ fmtUsd(costTrend.totalCost) }}
            </p>
            <div class="table-scroll">
              <table class="table">
                <thead>
                  <tr>
                    <th>{{ t('usage.dimensionLabel') }}</th>
                    <th class="num">{{ t('usage.requests') }}</th>
                    <th class="num">{{ t('usage.cost') }}</th>
                    <th class="num">{{ t('usage.percent') }}</th>
                    <th class="num">{{ t('usage.errorRate') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="e in costTrend.entries" :key="e.dimension_value">
                    <td>{{ e.dimension_value }}</td>
                    <td class="num">{{ fmtInt(e.request_count) }}</td>
                    <td class="num">{{ fmtUsd(e.total_cost_usd) }}</td>
                    <td class="num">{{ fmtNum(e.percentage, 1) }}%</td>
                    <td class="num">{{ fmtNum(e.error_rate * 100, 1) }}%</td>
                  </tr>
                </tbody>
              </table>
            </div>
          </template>
        </template>
      </template>
    </AppStateView>
  </div>
</template>

<style scoped>
.usage__degraded {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: var(--app-space-2);
  padding: var(--app-space-3);
  margin-bottom: var(--app-space-3);
  border-radius: var(--app-radius);
  background: color-mix(in srgb, var(--app-warning) 10%, transparent);
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
}

.usage__degraded-reason {
  display: block;
  width: 100%;
  font-size: 0.6875rem;
  color: var(--app-text-muted);
}

.usage__hint-line {
  margin: var(--app-space-2) 0;
  font-size: 0.8125rem;
  color: var(--app-text-secondary);
}

.usage__hint-line--err {
  color: var(--app-danger);
}

.usage__dims {
  display: flex;
  gap: var(--app-space-2);
  overflow-x: auto;
  padding-bottom: var(--app-space-2);
}

.usage__dim {
  flex: 0 0 auto;
  min-height: 48px;
  padding: 0 var(--app-space-3);
  /* R1（17 §4-R1 / 06 §7）：新增触控控件一律 ≥48 CSS px。
     44px 是存量 .btn 的下限、不是新标准；这些 chip 是 2026-10-06 新增的，
     走的是自定义选择器而非 .btn，故此前落在 32-40px —— 违反 R1。 */
  border: 1px solid var(--app-border);
  border-radius: 999px;
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font: inherit;
  font-size: 0.8125rem;
  cursor: pointer;
}

.usage__dim--on {
  background: var(--app-primary);
  border-color: var(--app-primary);
  color: var(--app-on-primary);
}

.usage__cost-total {
  margin: var(--app-space-2) 0;
  font-size: 0.9375rem;
  font-weight: 600;
}

.usage__tabs {
  position: sticky;
  top: 0;
  z-index: 2;
  display: flex;
  gap: var(--app-space-1);
  background: var(--app-bg);
  padding: var(--app-space-2) 0;
  margin-bottom: var(--app-space-2);
}

.usage__tab {
  flex: 1;
  /* R1-legacy：存量 44px（首个落地轮既有，R1 明确 44 是存量下限而非新标准）。
     保留原值以免改变存量控件的手感；新增控件一律走 48px（见 §11.27）。 */
  min-height: 44px;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius);
  background: var(--app-surface);
  color: var(--app-text-secondary);
  font-size: 0.875rem;
  font-weight: 500;
  cursor: pointer;
}

.usage__tab--active {
  border-color: var(--kx-primary);
  color: var(--kx-primary);
  background: var(--app-primary-soft);
}

.usage__grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--app-space-2);
}

@media (min-width: 600px) {
  .usage__grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

.usage__period {
  margin-top: var(--app-space-4);
  font-size: 0.75rem;
  color: var(--app-text-muted);
  text-align: center;
}

.usage__focus-head {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: var(--app-space-2);
  margin-bottom: var(--app-space-2);
}
</style>

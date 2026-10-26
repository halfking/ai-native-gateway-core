<script setup lang="ts">
// UsageEnhancedView — 批 158b（2026-10-17）初版；2026-10-21 契约对齐重写。
//
// 组合 5 个 usage 端点的综合视图（总览 + 成本趋势 + 同期对比 + 缓存经济 +
// Top API keys）。数据全部由 api/usageEnhanced 组合，字段名与后端响应逐字一致。
//
// ⚠️ 初版读的是一套**从未存在过**的字段（summary.total_cost / cost_trend.data /
// period_compare.delta_cost_pct / cache_economics.cache_hit_rate /
// hot_api_keys[].key_id），于是视图在真实响应上直接抛 TypeError ⇒ 整页白屏。
// 现在读的每个字段都能在 api/usage.ts 的响应类型里找到出处。
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getUsageEnhancedData, type UsageEnhancedData } from '../../api/usageEnhanced'
import {
  emptyDegradation,
  mergeDegradation,
  type DegradationState,
} from '../../composables/useDegradationMarker'
import { formatNumberLocale } from '../../utils/format'

const { t } = useI18n()

const loading = ref(false)
const error = ref('')
const data = ref<UsageEnhancedData | null>(null)

// 降级标记（2026-10-21）
//
// 后端在「schema 落后于代码」时返回 200 + 全 0。合成视图有 5 个数据源，
// 任何一源降级都必须让用户知道**是哪几个指标**不可信 —— 否则「本月没花钱」
// 和「本月算不出来」在页面上是同一张脸（同 UsageCost.vue 的处理）。
// 三态语义写在 useDegradationMarker.ts，这里不要重写一份内联逻辑。
const degradation = ref<DegradationState>(emptyDegradation())

async function load() {
  loading.value = true
  error.value = ''
  // 先清空再重收：本轮若全部降级为 false，旧的那条降级提示必须消失。
  degradation.value = emptyDegradation()
  try {
    const d = await getUsageEnhancedData({ days: 7, topN: 10 })
    data.value = d
    degradation.value = mergeDegradation(degradation.value, t('dataLifecycle.usageEnhanced.sources.summary'), d.summary)
    degradation.value = mergeDegradation(degradation.value, t('dataLifecycle.usageEnhanced.sources.costTrend'), d.cost_trend)
    degradation.value = mergeDegradation(degradation.value, t('dataLifecycle.usageEnhanced.sources.periodCompare'), d.period_compare)
    degradation.value = mergeDegradation(degradation.value, t('dataLifecycle.usageEnhanced.sources.cacheEconomics'), d.cache_economics)
    if (d.hot_keys_degraded) {
      degradation.value = mergeDegradation(degradation.value, t('dataLifecycle.usageEnhanced.sources.hotKeys'), {
        degraded: true,
        degraded_reason: d.hot_keys_degraded_reason,
      })
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : t('dataLifecycle.usageEnhanced.errors.load')
  } finally {
    loading.value = false
  }
}

function formatNumber(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return formatNumberLocale(n)
}

function formatCurrency(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return '$' + n.toFixed(2)
}

/**
 * 已经是百分数的值（如 period_compare.change_pct = 23.4 表示 23.4%）。
 * ⚠️ 后端 change_pct 的定义是 `(changeAbs / previousCost) * 100` ⇒ 已是百分数；
 * 而 cache_hit_ratio 是 0~1 的比例 ⇒ 两者不能共用一个「乘 100」的格式化器。
 * 初版把两者都用「乘 100」处理，缓存命中率会被显示成 3535.9%。
 */
function formatPercent(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return n.toFixed(1) + '%'
}

/** 0~1 比例 → 百分比。 */
function formatRatioAsPercent(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return (n * 100).toFixed(1) + '%'
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="usage-enhanced-view">
    <header class="enhanced-header">
      <h1 class="enhanced-title">{{ t('dataLifecycle.pages.usageEnhanced') }}</h1>
      <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
        {{ loading ? t('dataLifecycle.usageEnhanced.loading') : t('dataLifecycle.usageEnhanced.refresh') }}
      </button>
    </header>

    <p v-if="error" class="enhanced-error">{{ error }}</p>

    <div v-if="loading && !data" class="enhanced-loading">{{ t('dataLifecycle.usageEnhanced.loading') }}</div>

    <template v-else-if="data">
      <!--
        降级提示与上面的 error 是两件不同的事：error 是「请求失败」，
        degraded 是「请求成功但服务端算不出来」。刻意用 warning 语义：
        它不是故障，是「这个数别当结论看」。
      -->
      <div v-if="degradation.active" class="alert alert-warning enhanced-degraded" role="status">
        <strong>{{ t('dataLifecycle.usageEnhanced.degraded.title') }}</strong>
        {{ t('dataLifecycle.usageEnhanced.degraded.hint') }}
        <ul class="degraded-reasons">
          <li v-for="r in degradation.reasons" :key="r">{{ r }}</li>
        </ul>
      </div>

      <!-- 4 卡片：总览 -->
      <section class="enhanced-cards">
        <div class="enhanced-card">
          <h2 class="enhanced-card-title">{{ t('dataLifecycle.usageEnhanced.cards.totalCost') }}</h2>
          <p class="enhanced-card-value">{{ formatCurrency(data.summary.total_cost_usd) }}</p>
        </div>
        <div class="enhanced-card">
          <h2 class="enhanced-card-title">{{ t('dataLifecycle.usageEnhanced.cards.totalRequests') }}</h2>
          <p class="enhanced-card-value">{{ formatNumber(data.summary.total_requests) }}</p>
        </div>
        <div class="enhanced-card">
          <h2 class="enhanced-card-title">{{ t('dataLifecycle.usageEnhanced.cards.periodDelta') }}</h2>
          <p
            class="enhanced-card-value"
            :class="{
              'is-up': data.period_compare.change_abs > 0,
              'is-down': data.period_compare.change_abs < 0,
            }"
          >
            {{ formatPercent(data.period_compare.change_pct) }}
          </p>
          <p class="enhanced-card-hint">
            {{ formatCurrency(data.period_compare.change_abs) }}
          </p>
        </div>
        <div class="enhanced-card">
          <h2 class="enhanced-card-title">{{ t('dataLifecycle.usageEnhanced.cards.cacheHitRate') }}</h2>
          <p class="enhanced-card-value">{{ formatRatioAsPercent(data.cache_economics.cache_hit_ratio) }}</p>
        </div>
      </section>

      <!-- 1 大图：成本趋势（占位，后续可接 Chart.js） -->
      <section class="enhanced-section">
        <h2 class="enhanced-section-title">
          {{ t('dataLifecycle.usageEnhanced.trend.title', { days: data.days }) }}
        </h2>
        <ul v-if="data.cost_trend.entries.length" class="enhanced-list">
          <li v-for="p in data.cost_trend.entries" :key="p.dimension_value" class="enhanced-trend-row">
            <span class="trend-dimension">{{ p.dimension_value }}</span>
            <span class="trend-cost">{{ formatCurrency(p.total_cost_usd) }}</span>
            <span class="trend-requests">{{ formatNumber(p.request_count) }} req</span>
          </li>
        </ul>
        <p v-else class="enhanced-empty">{{ t('dataLifecycle.usageEnhanced.empty') }}</p>
      </section>

      <!-- 1 表格：Top API keys -->
      <section class="enhanced-section">
        <h2 class="enhanced-section-title">{{ t('dataLifecycle.usageEnhanced.hotKeys.title') }}</h2>
        <table v-if="data.hot_api_keys.length" class="enhanced-table">
          <thead>
            <tr>
              <th>{{ t('dataLifecycle.usageEnhanced.hotKeys.colId') }}</th>
              <th>{{ t('dataLifecycle.usageEnhanced.hotKeys.colPrefix') }}</th>
              <th>{{ t('dataLifecycle.usageEnhanced.hotKeys.colApplication') }}</th>
              <th>{{ t('dataLifecycle.usageEnhanced.hotKeys.colRequests') }}</th>
              <th>{{ t('dataLifecycle.usageEnhanced.hotKeys.colTokens') }}</th>
              <th>{{ t('dataLifecycle.usageEnhanced.hotKeys.colCost') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="k in data.hot_api_keys" :key="k.api_key_id">
              <td><code>{{ k.api_key_id }}</code></td>
              <td>{{ k.key_prefix ?? '—' }}</td>
              <td>{{ k.application_code ?? '—' }}</td>
              <td>{{ formatNumber(k.request_count) }}</td>
              <td>{{ formatNumber(k.total_tokens) }}</td>
              <td>{{ formatCurrency(k.total_cost_usd) }}</td>
            </tr>
          </tbody>
        </table>
        <p v-else class="enhanced-empty">{{ t('dataLifecycle.usageEnhanced.empty') }}</p>
      </section>

      <footer class="enhanced-footer">
        {{ t('dataLifecycle.usageEnhanced.footer', { time: data.refreshedAt }) }}
      </footer>
    </template>
  </div>
</template>

<style scoped>
.usage-enhanced-view {
  padding: var(--space-4);
}
.enhanced-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--space-3);
}
.enhanced-title {
  font-size: var(--text-xl);
  font-weight: var(--font-semibold);
}
.enhanced-error {
  color: var(--danger);
  padding: var(--space-2);
  background: var(--danger-bg);
  border-radius: var(--radius);
  margin-bottom: var(--space-3);
}
.enhanced-degraded {
  margin-bottom: var(--space-3);
}
.degraded-reasons {
  margin: var(--space-1) 0 0;
  padding-left: var(--space-4);
}
.enhanced-loading {
  color: var(--text-muted);
  padding: var(--space-3);
}
.enhanced-cards {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: var(--space-3);
  margin-bottom: var(--space-4);
}
.enhanced-card {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: var(--space-3);
}
.enhanced-card-title {
  font-size: var(--text-sm);
  color: var(--text-muted);
  margin-bottom: var(--space-1);
}
.enhanced-card-value {
  font-size: var(--text-2xl);
  font-weight: var(--font-semibold);
}
.enhanced-card-hint {
  font-size: var(--text-sm);
  color: var(--text-muted);
}
.enhanced-card-value.is-up { color: var(--danger); }
.enhanced-card-value.is-down { color: var(--success); }
.enhanced-section {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: var(--space-3);
  margin-bottom: var(--space-3);
}
.enhanced-section-title {
  font-size: var(--text-md);
  font-weight: var(--font-semibold);
  margin-bottom: var(--space-2);
}
.enhanced-list {
  list-style: none;
  margin: 0;
  padding: 0;
}
.enhanced-trend-row {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr;
  padding: var(--space-1) 0;
  border-bottom: 1px solid var(--border-muted);
}
.enhanced-trend-row:last-child { border-bottom: none; }
.enhanced-table {
  width: 100%;
  border-collapse: collapse;
}
.enhanced-table th,
.enhanced-table td {
  text-align: left;
  padding: var(--space-1) var(--space-2);
  border-bottom: 1px solid var(--border-muted);
}
.enhanced-table th {
  font-size: var(--text-sm);
  color: var(--text-muted);
  font-weight: var(--font-semibold);
}
.enhanced-empty {
  color: var(--text-muted);
  font-style: italic;
  margin: var(--space-2) 0;
}
.enhanced-footer {
  margin-top: var(--space-3);
  color: var(--text-muted);
  font-size: var(--text-sm);
}
@media (max-width: 768px) {
  .enhanced-cards { grid-template-columns: 1fr 1fr; }
}
</style>

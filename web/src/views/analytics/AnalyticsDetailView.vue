<script setup lang="ts">
// AnalyticsDetailView — 分析详情主容器 (2026-11-20)
// 参数化复用: 通过 route.params 区分维度和对象
import { onMounted, ref, computed, watchEffect } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { surfaceError } from '../../i18n/errorSurface'
import { fetchAnalyticsDetail, type AnalyticsDetailPayload } from '../../api/board'
import { ANALYTICS_REGISTRY, type AnalyticsDimension } from '../../config/analyticsRegistry'
import KxDateRangePicker from '../../components/ui/KxDateRangePicker.vue'
import { useSpanDaysRange } from '../../composables/useSpanDaysRange'
import type { KxDateRange } from '../../components/ui/kx-date-types'
import AnalyticsSummaryCards from '../../components/analytics/AnalyticsSummaryCards.vue'
import AnalyticsTrendChart from '../../components/analytics/AnalyticsTrendChart.vue'
import AnalyticsCrossTable from '../../components/analytics/AnalyticsCrossTable.vue'

const route = useRoute()
const router = useRouter()
const loading = ref(false)
const error = ref('')
const data = ref<AnalyticsDetailPayload | null>(null)

// 从路由获取维度和标识符
const dimension = computed(() => route.params.dimension as AnalyticsDimension)
const identifier = computed(() => route.params.id as string)

// 2026-11-21 审计轮 D-7：非法 dimension（手滑/外链 /admin/analytics/foo/bar）
// 原样直达模板 `ANALYTICS_REGISTRY[dimension].titleKey` ⇒ 渲染期 TypeError 整页崩。
// 守卫：registry 不存在时回到列表页（优雅降级），而非让模板抛错。
const registry = computed(() => ANALYTICS_REGISTRY[dimension.value])
watchEffect(() => {
  if (!registry.value) {
    void router.replace({ path: '/admin/board-operational' })
  }
})

// 时间范围
const timeRange = ref(Number(route.query.days) || 7)
const { presets, rangeValue, applyRange } = useSpanDaysRange(timeRange, [
  { days: 1, labelKey: 'common.dateRange.preset.today' },
  { days: 7, labelKey: 'common.dateRange.preset.last7d' },
  { days: 30, labelKey: 'common.dateRange.preset.last30d' },
  { days: 90, labelKey: 'dashboard.range.last90d' },
])

// 页面标题
const pageTitle = computed(() => {
  const registry = ANALYTICS_REGISTRY[dimension.value]
  const dimTitle = registry?.titleKey || 'analytics.detail.title'
  const displayName = data.value?.display_name || identifier.value
  return `${displayName}`
})

const breadcrumbs = computed(() => {
  const registry = ANALYTICS_REGISTRY[dimension.value]
  return [
    { label: 'dashboard.operational.title', path: '/admin/board-operational' },
    { label: registry?.listTitleKey || 'analytics.list.title', path: registry?.listPath },
    { label: pageTitle.value, path: '' }
  ]
})

function onRangeApply(range: KxDateRange) {
  applyRange(range)
  router.push({
    query: { ...route.query, days: timeRange.value }
  })
  void load()
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    data.value = await fetchAnalyticsDetail(
      dimension.value,
      identifier.value,
      { days: timeRange.value }
    )
  } catch (e) {
    error.value = surfaceError(e)
  } finally {
    loading.value = false
  }
}

function goBack() {
  const registry = ANALYTICS_REGISTRY[dimension.value]
  router.push({
    path: registry.listPath,
    query: { days: timeRange.value }
  })
}

function onCrossItemClick(crossDimension: string, itemKey: string) {
  const registry = ANALYTICS_REGISTRY[crossDimension as AnalyticsDimension]
  if (!registry) return
  
  const path = registry.detailPath(itemKey)
  router.push({
    path,
    query: { days: timeRange.value }
  })
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="analytics-detail-view">
    <header class="ad-header">
      <div class="ad-header-left">
        <button class="btn btn-ghost btn-sm" @click="goBack">
          ← {{ $t('common.button.back') }}
        </button>
        <div>
          <div class="ad-breadcrumbs">
            <span
              v-for="(crumb, index) in breadcrumbs"
              :key="index"
              class="ad-breadcrumb"
            >
              <router-link
                v-if="crumb.path"
                :to="crumb.path"
                class="ad-breadcrumb-link"
              >
                {{ $t(crumb.label) }}
              </router-link>
              <span v-else>{{ crumb.label }}</span>
              <span v-if="index < breadcrumbs.length - 1" class="ad-breadcrumb-sep">/</span>
            </span>
          </div>
          <h1 class="ad-title">
            <!-- 2026-11-21 审计轮 D-7：registry 可选链守卫（非法 dimension 由 watchEffect 重定向，渲染期不再抛 TypeError） -->
            <span v-if="registry" class="ad-dimension-badge">{{ $t(registry.titleKey) }}</span>
            {{ pageTitle }}
          </h1>
          <p class="ad-subtitle">{{ $t('analytics.detail.subtitle', { days: timeRange }) }}</p>
        </div>
      </div>
      <div class="ad-header-actions">
        <KxDateRangePicker
          :model-value="rangeValue"
          :presets="presets"
          :max-span-days="365"
          :disabled="loading"
          @apply="onRangeApply"
        />
        <button class="btn btn-ghost btn-sm" :disabled="loading" @click="load">
          {{ loading ? $t('common.feedback.loading') : $t('common.button.refresh') }}
        </button>
      </div>
    </header>

    <p v-if="error" class="ad-error">{{ error }}</p>

    <div v-if="loading && !data" class="ad-loading">
      {{ $t('common.feedback.loading') }}
    </div>

    <template v-else-if="data">
      <!-- 降级警告 -->
      <div v-if="data.degraded" class="ad-warning">
        <strong>{{ $t('analytics.detail.degraded') }}</strong>
        <span v-if="data.degraded_reason">: {{ data.degraded_reason }}</span>
      </div>

      <!-- 核心指标卡片 -->
      <section class="ad-section">
        <h2 class="ad-section-title">{{ $t('analytics.detail.summary') }}</h2>
        <AnalyticsSummaryCards :summary="data.summary" />
      </section>

      <!-- 趋势图表 -->
      <section v-if="data.trends && data.trends.length > 0" class="ad-section">
        <h2 class="ad-section-title">{{ $t('analytics.detail.trends') }}</h2>
        <div v-if="data.degraded_trends" class="ad-section-warning">
          {{ $t('analytics.detail.degradedTrends') }}
        </div>
        <AnalyticsTrendChart :trends="data.trends" />
      </section>

      <!-- 交叉分析 -->
      <section v-if="data.cross_analysis" class="ad-section">
        <h2 class="ad-section-title">{{ $t('analytics.detail.crossAnalysis') }}</h2>
        <p class="ad-section-hint">{{ $t('analytics.detail.crossAnalysisHint') }}</p>
        <AnalyticsCrossTable
          :cross-analysis="data.cross_analysis"
          :current-dimension="dimension"
          :degraded-dimensions="data.degraded_cross"
          @item-click="onCrossItemClick"
        />
      </section>
    </template>
  </div>
</template>

<style scoped>
.analytics-detail-view {
  padding: var(--space-4);
}

.ad-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: var(--space-4);
  gap: var(--space-3);
}

.ad-header-left {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
  flex: 1;
}

.ad-breadcrumbs {
  font-size: var(--text-xs);
  color: var(--text-muted);
  margin-bottom: var(--space-2);
  display: flex;
  align-items: center;
  gap: var(--space-1);
  flex-wrap: wrap;
}

.ad-breadcrumb-link {
  color: var(--text-muted);
  text-decoration: none;
  transition: color 0.15s;
}

.ad-breadcrumb-link:hover {
  color: var(--primary);
}

.ad-breadcrumb-sep {
  margin: 0 var(--space-1);
  color: var(--text-muted);
}

.ad-title {
  font-size: var(--text-2xl);
  font-weight: var(--font-semibold);
  margin-bottom: var(--space-1);
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}

.ad-dimension-badge {
  display: inline-block;
  padding: var(--space-1) var(--space-2);
  background: color-mix(in srgb, var(--accent) 14%, var(--bg));
  color: var(--primary);
  border-radius: var(--radius);
  font-size: var(--text-xs);
  font-weight: var(--font-medium);
  text-transform: uppercase;
}

.ad-subtitle {
  font-size: var(--text-sm);
  color: var(--text-muted);
}

.ad-header-actions {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.ad-error {
  color: var(--danger);
  padding: var(--space-3);
  background: var(--danger-bg);
  border-radius: var(--radius);
  margin-bottom: var(--space-3);
  border: 1px solid var(--danger);
}

.ad-warning {
  color: var(--warning);
  padding: var(--space-3);
  background: var(--warning-bg);
  border-radius: var(--radius);
  margin-bottom: var(--space-3);
  border: 1px solid var(--warning);
}

.ad-loading {
  color: var(--text-muted);
  padding: var(--space-4);
  text-align: center;
  background: var(--bg-surface);
  border-radius: var(--radius);
  border: 1px solid var(--border);
}

.ad-section {
  margin-bottom: var(--space-4);
}

.ad-section-title {
  font-size: var(--text-lg);
  font-weight: var(--font-semibold);
  margin-bottom: var(--space-3);
  padding-bottom: var(--space-2);
  border-bottom: 2px solid var(--border);
}

.ad-section-hint {
  font-size: var(--text-sm);
  color: var(--text-muted);
  margin-bottom: var(--space-3);
  padding: var(--space-2);
  background: var(--bg-surface);
  border-left: 3px solid var(--primary);
  border-radius: var(--radius);
}

.ad-section-warning {
  color: var(--warning);
  font-size: var(--text-sm);
  padding: var(--space-2);
  background: var(--warning-bg);
  border-radius: var(--radius);
  margin-bottom: var(--space-3);
}

@media (max-width: 768px) {
  .ad-header {
    flex-direction: column;
    align-items: stretch;
  }

  .ad-header-actions {
    justify-content: flex-end;
  }

  .ad-title {
    font-size: var(--text-xl);
  }
}
</style>

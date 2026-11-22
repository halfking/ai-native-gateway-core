<script setup lang="ts">
// BoardOperationalListView — 运营看板完整列表页 (2026-11-20)
// 参数化复用: 通过 route.params.dimension 区分维度 (models/providers/clients/tenants)
import { onMounted, ref, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { surfaceError } from '../i18n/errorSurface'
import { fetchDashboardBoard, type BoardPayload, type BoardPieItem } from '../api/board'
import { ANALYTICS_REGISTRY, type AnalyticsDimension } from '../config/analyticsRegistry'
import KxDateRangePicker from '../components/ui/KxDateRangePicker.vue'
import { useRangeDays } from '../composables/useSpanDaysRange'
import type { KxDateRange } from '../components/ui/kx-date-types'

const route = useRoute()
const router = useRouter()
const loading = ref(false)
const error = ref('')
const boardData = ref<BoardPayload | null>(null)

// 从路由参数获取维度
const dimension = computed(() => {
  const dim = route.params.dimension as string
  // 映射: models -> model, providers -> provider, etc.
  return dim.replace(/s$/, '') as AnalyticsDimension
})

// 时间范围
//
// ★ 2026-11-23 批判式审计轮：改用 useRangeDays + 支持 URL 里的 start/end。
//   此前用 useSpanDaysRange（自定义区间被压成「跨度天数」）⇒ 用户选的历史
//   区间拿到的却是「最近 N 天」。board 后端本就支持真实区间。
const timeRange = ref(Number(route.query.days) || 7)
const urlStart = typeof route.query.start === 'string' ? route.query.start : ''
const urlEnd = typeof route.query.end === 'string' ? route.query.end : ''
const { presets, rangeValue, customRange, applyRange, setCustomRange } = useRangeDays(timeRange, [
  { days: 1, labelKey: 'common.dateRange.preset.today' },
  { days: 7, labelKey: 'common.dateRange.preset.last7d' },
  { days: 30, labelKey: 'common.dateRange.preset.last30d' },
  { days: 90, labelKey: 'dashboard.range.last90d' },
])
// URL 带入的自定义区间（board 下钻链接）⇒ 直接作为当前区间。
if (urlStart && urlEnd) {
  setCustomRange({ start: urlStart, end: urlEnd })
}

// 排序配置
type SortKey = 'requests' | 'tokens' | 'cost_usd' | 'credits'
const sortKey = ref<SortKey>('requests')
const sortDesc = ref(true)

// 获取当前维度的数据
const items = computed(() => {
  if (!boardData.value?.pies) return []
  
  const pies = boardData.value.pies
  let data: BoardPieItem[] = []
  
  switch (dimension.value) {
    case 'model':
      data = pies.models || []
      break
    case 'provider':
      data = pies.providers || []
      break
    case 'client':
      data = pies.clients || []
      break
    case 'tenant':
      data = pies.tenants || []
      break
  }
  
  return data
})

// 排序后的数据
const sortedItems = computed(() => {
  const list = [...items.value]
  
  list.sort((a, b) => {
    const aVal = a[sortKey.value] || 0
    const bVal = b[sortKey.value] || 0
    return sortDesc.value ? bVal - aVal : aVal - bVal
  })
  
  return list
})

// 页面标题
const pageTitle = computed(() => {
  const registry = ANALYTICS_REGISTRY[dimension.value]
  return registry?.listTitleKey || 'analytics.list.title'
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
    boardData.value = await fetchDashboardBoard(customRange.value
      ? { start: customRange.value.start, end: customRange.value.end }
      : { days: timeRange.value })
  } catch (e) {
    error.value = surfaceError(e)
  } finally {
    loading.value = false
  }
}

function changeSortKey(key: SortKey) {
  if (sortKey.value === key) {
    sortDesc.value = !sortDesc.value
  } else {
    sortKey.value = key
    sortDesc.value = true
  }
}

function navigateToDetail(item: BoardPieItem) {
  const registry = ANALYTICS_REGISTRY[dimension.value]
  const path = registry.detailPath(item.key)
  
  router.push({
    path,
    query: { days: timeRange.value }
  })
}

function goBack() {
  router.push('/admin/board-operational')
}

function formatNumber(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return n.toLocaleString('en-US')
}

function formatCost(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return '$' + n.toFixed(2)
}

function formatPct(rate: number, total: number): string {
  if (total === 0) return '0.0%'
  return ((rate / total) * 100).toFixed(1) + '%'
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="list-view">
    <header class="list-header">
      <div class="list-header-left">
        <button class="btn btn-ghost btn-sm" @click="goBack">
          ← {{ $t('common.button.back') }}
        </button>
        <div>
          <h1 class="list-title">{{ $t(pageTitle) }}</h1>
          <p class="list-subtitle">{{ $t('analytics.list.subtitle', { days: timeRange }) }}</p>
        </div>
      </div>
      <div class="list-header-actions">
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

    <p v-if="error" class="list-error">{{ error }}</p>

    <div v-if="loading && !boardData" class="list-loading">
      {{ $t('common.feedback.loading') }}
    </div>

    <div v-else-if="sortedItems.length === 0" class="list-empty">
      <p>{{ $t('analytics.list.empty') }}</p>
    </div>

    <div v-else class="list-table-container">
      <table class="list-table">
        <thead>
          <tr>
            <th class="col-index">#</th>
            <th class="col-name">{{ $t('analytics.list.column.name') }}</th>
            <th class="col-sortable" @click="changeSortKey('requests')">
              <span>{{ $t('analytics.list.column.requests') }}</span>
              <span v-if="sortKey === 'requests'" class="sort-indicator">
                {{ sortDesc ? '▼' : '▲' }}
              </span>
            </th>
            <th class="col-sortable" @click="changeSortKey('tokens')">
              <span>{{ $t('analytics.list.column.tokens') }}</span>
              <span v-if="sortKey === 'tokens'" class="sort-indicator">
                {{ sortDesc ? '▼' : '▲' }}
              </span>
            </th>
            <th class="col-sortable" @click="changeSortKey('cost_usd')">
              <span>{{ $t('analytics.list.column.cost') }}</span>
              <span v-if="sortKey === 'cost_usd'" class="sort-indicator">
                {{ sortDesc ? '▼' : '▲' }}
              </span>
            </th>
            <th class="col-sortable" @click="changeSortKey('credits')">
              <span>{{ $t('analytics.list.column.credits') }}</span>
              <span v-if="sortKey === 'credits'" class="sort-indicator">
                {{ sortDesc ? '▼' : '▲' }}
              </span>
            </th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="(item, index) in sortedItems"
            :key="item.key"
            class="list-row"
            @click="navigateToDetail(item)"
          >
            <td class="col-index">{{ index + 1 }}</td>
            <td class="col-name">
              <span class="item-name">{{ item.key }}</span>
            </td>
            <td class="col-number">{{ formatNumber(item.requests) }}</td>
            <td class="col-number">{{ formatNumber(item.tokens) }}</td>
            <td class="col-number">{{ formatCost(item.cost_usd) }}</td>
            <td class="col-number">{{ formatNumber(item.credits) }}</td>
          </tr>
        </tbody>
      </table>
      
      <div class="list-footer">
        <p>{{ $t('analytics.list.total', { count: sortedItems.length }) }}</p>
        <!-- 2026-11-21 审计轮 D-6 披露：后端 queryDimPie SQL LIMIT 25（dashboard_board_queries.go:567），
             列表页复用 board.pies 载荷 ⇒ 实为 Top 25 截断面而非完整清单；>25 个维度的部署上第 26 名起静默不可见。 -->
        <p class="list-truncated-note">{{ $t('analytics.list.truncated') }}</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.list-view {
  padding: var(--space-4);
}

.list-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  margin-bottom: var(--space-4);
  gap: var(--space-3);
}

.list-header-left {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
}

.list-title {
  font-size: var(--text-xl);
  font-weight: var(--font-semibold);
  margin-bottom: var(--space-1);
}

.list-subtitle {
  font-size: var(--text-sm);
  color: var(--text-muted);
}

.list-header-actions {
  display: flex;
  gap: var(--space-2);
  align-items: center;
}

.list-error {
  color: var(--danger);
  padding: var(--space-2);
  background: var(--danger-bg);
  border-radius: var(--radius);
  margin-bottom: var(--space-3);
}

.list-loading,
.list-empty {
  color: var(--text-muted);
  padding: var(--space-4);
  text-align: center;
  background: var(--bg-surface);
  border-radius: var(--radius);
  border: 1px solid var(--border);
}

.list-table-container {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
}

.list-table {
  width: 100%;
  border-collapse: collapse;
}

.list-table thead {
  background: var(--bg);
  border-bottom: 2px solid var(--border);
}

.list-table th {
  padding: var(--space-3);
  text-align: left;
  font-size: var(--text-sm);
  font-weight: var(--font-semibold);
  color: var(--text);
}

.col-sortable {
  cursor: pointer;
  user-select: none;
  transition: background-color 0.15s;
}

.col-sortable:hover {
  background: var(--bg-surface);
}

.col-sortable span {
  display: inline-block;
}

.sort-indicator {
  margin-left: var(--space-1);
  font-size: var(--text-xs);
  color: var(--primary);
}

.col-index {
  width: 60px;
  text-align: center !important;
}

.col-name {
  min-width: 200px;
}

.col-number {
  text-align: right !important;
  font-variant-numeric: tabular-nums;
}

.list-row {
  cursor: pointer;
  transition: background-color 0.15s;
  border-bottom: 1px solid var(--border-muted);
}

.list-row:hover {
  background: var(--bg-hover);
}

.list-row:last-child {
  border-bottom: none;
}

.list-row td {
  padding: var(--space-3);
  font-size: var(--text-sm);
}

.item-name {
  font-weight: var(--font-medium);
  color: var(--text);
}

.list-footer {
  padding: var(--space-3);
  background: var(--bg);
  border-top: 1px solid var(--border);
  font-size: var(--text-sm);
  color: var(--text-muted);
  text-align: right;
}

/* 2026-11-21 审计轮 D-6：Top 25 截断面披露行 */
.list-truncated-note {
  margin: var(--space-2) 0 0;
  font-size: var(--text-xs, 12px);
  color: var(--text-muted);
  text-align: left;
}

@media (max-width: 768px) {
  .list-header {
    flex-direction: column;
    align-items: stretch;
  }

  .list-header-actions {
    justify-content: flex-end;
  }

  .list-table {
    font-size: var(--text-xs);
  }

  .col-name {
    min-width: 120px;
  }
}
</style>

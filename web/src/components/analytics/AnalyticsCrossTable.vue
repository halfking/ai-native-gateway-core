<script setup lang="ts">
// AnalyticsCrossTable — 交叉分析表格 (2026-11-20)
import { computed } from 'vue'
import type { AnalyticsCrossAnalysis } from '../../api/board'
import type { AnalyticsDimension } from '../../config/analyticsRegistry'

interface Props {
  crossAnalysis: AnalyticsCrossAnalysis
  currentDimension: AnalyticsDimension
  degradedDimensions?: string[]
}

const props = defineProps<Props>()

const emit = defineEmits<{
  itemClick: [dimension: string, itemKey: string]
}>()

interface CrossSection {
  dimension: AnalyticsDimension
  titleKey: string
  items: any[]
  degraded: boolean
}

const sections = computed<CrossSection[]>(() => {
  const result: CrossSection[] = []
  const degraded = props.degradedDimensions || []

  // 只显示与当前维度不同的维度
  if (props.currentDimension !== 'model' && props.crossAnalysis.by_model) {
    result.push({
      dimension: 'model',
      titleKey: 'analytics.cross.byModel',
      items: props.crossAnalysis.by_model,
      degraded: degraded.includes('model')
    })
  }

  if (props.currentDimension !== 'provider' && props.crossAnalysis.by_provider) {
    result.push({
      dimension: 'provider',
      titleKey: 'analytics.cross.byProvider',
      items: props.crossAnalysis.by_provider,
      degraded: degraded.includes('provider')
    })
  }

  if (props.currentDimension !== 'client' && props.crossAnalysis.by_client) {
    result.push({
      dimension: 'client',
      titleKey: 'analytics.cross.byClient',
      items: props.crossAnalysis.by_client,
      degraded: degraded.includes('client')
    })
  }

  if (props.currentDimension !== 'tenant' && props.crossAnalysis.by_tenant) {
    result.push({
      dimension: 'tenant',
      titleKey: 'analytics.cross.byTenant',
      items: props.crossAnalysis.by_tenant,
      degraded: degraded.includes('tenant')
    })
  }

  // 错误类型和状态不受currentDimension限制
  if (props.crossAnalysis.by_error) {
    result.push({
      dimension: 'error' as any,
      titleKey: 'analytics.cross.byError',
      items: props.crossAnalysis.by_error,
      degraded: degraded.includes('error')
    })
  }

  if (props.crossAnalysis.by_status) {
    result.push({
      dimension: 'status' as any,
      titleKey: 'analytics.cross.byStatus',
      items: props.crossAnalysis.by_status,
      degraded: degraded.includes('status')
    })
  }

  return result
})

function handleItemClick(dimension: string, itemKey: string) {
  // 错误和状态维度不支持导航
  if (dimension === 'error' || dimension === 'status') {
    return
  }
  emit('itemClick', dimension, itemKey)
}

function formatNumber(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return n.toLocaleString('en-US')
}

function formatCost(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  return '$' + n.toFixed(2)
}

function isClickable(dimension: string): boolean {
  return dimension !== 'error' && dimension !== 'status'
}
</script>

<template>
  <div class="cross-table">
    <div v-if="sections.length === 0" class="cross-empty">
      {{ $t('analytics.cross.empty') }}
    </div>

    <div
      v-for="section in sections"
      :key="section.dimension"
      class="cross-section"
    >
      <div class="cross-section-header">
        <h3 class="cross-section-title">{{ $t(section.titleKey) }}</h3>
        <span v-if="section.degraded" class="cross-degraded-badge">
          {{ $t('analytics.cross.degraded') }}
        </span>
      </div>

      <div v-if="section.items.length === 0" class="cross-section-empty">
        {{ $t('analytics.cross.noData') }}
      </div>

      <div v-else class="cross-table-container">
        <table class="cross-table-inner">
          <thead>
            <tr>
              <th class="col-index">#</th>
              <th class="col-name">{{ $t('analytics.cross.column.name') }}</th>
              <th class="col-number">{{ $t('analytics.cross.column.requests') }}</th>
              <th class="col-number">{{ $t('analytics.cross.column.tokens') }}</th>
              <th class="col-number">{{ $t('analytics.cross.column.cost') }}</th>
              <th class="col-number">{{ $t('analytics.cross.column.credits') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="(item, index) in section.items.slice(0, 10)"
              :key="item.key"
              class="cross-row"
              :class="{ 'is-clickable': isClickable(section.dimension) }"
              @click="handleItemClick(section.dimension, item.key)"
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
        
        <div v-if="section.items.length > 10" class="cross-section-footer">
          {{ $t('analytics.cross.showingTop', { count: 10, total: section.items.length }) }}
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.cross-table {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}

.cross-empty {
  color: var(--text-muted);
  padding: var(--space-4);
  text-align: center;
  background: var(--bg-surface);
  border-radius: var(--radius);
  border: 1px solid var(--border);
  font-style: italic;
}

.cross-section {
  background: var(--bg-surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
}

.cross-section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: var(--space-3);
  background: var(--bg);
  border-bottom: 1px solid var(--border);
}

.cross-section-title {
  font-size: var(--text-md);
  font-weight: var(--font-semibold);
  color: var(--text);
  margin: 0;
}

.cross-degraded-badge {
  display: inline-block;
  padding: var(--space-1) var(--space-2);
  background: var(--warning-bg);
  color: var(--warning);
  border-radius: var(--radius);
  font-size: var(--text-xs);
  font-weight: var(--font-medium);
}

.cross-section-empty {
  color: var(--text-muted);
  padding: var(--space-3);
  text-align: center;
  font-style: italic;
}

.cross-table-container {
  overflow-x: auto;
}

.cross-table-inner {
  width: 100%;
  border-collapse: collapse;
}

.cross-table-inner thead {
  background: var(--bg);
  border-bottom: 1px solid var(--border);
}

.cross-table-inner th {
  padding: var(--space-2) var(--space-3);
  text-align: left;
  font-size: var(--text-xs);
  font-weight: var(--font-semibold);
  color: var(--text-muted);
  text-transform: uppercase;
}

.col-index {
  width: 50px;
  text-align: center !important;
}

.col-name {
  min-width: 180px;
}

.col-number {
  text-align: right !important;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.cross-row {
  border-bottom: 1px solid var(--border-muted);
  transition: background-color 0.15s;
}

.cross-row.is-clickable {
  cursor: pointer;
}

.cross-row.is-clickable:hover {
  background: var(--bg-hover);
}

.cross-row:last-child {
  border-bottom: none;
}

.cross-row td {
  padding: var(--space-2) var(--space-3);
  font-size: var(--text-sm);
}

.item-name {
  font-weight: var(--font-medium);
  color: var(--text);
}

.cross-row.is-clickable .item-name {
  color: var(--primary);
}

.cross-row.is-clickable:hover .item-name {
  text-decoration: underline;
}

.cross-section-footer {
  padding: var(--space-2) var(--space-3);
  background: var(--bg);
  border-top: 1px solid var(--border);
  font-size: var(--text-xs);
  color: var(--text-muted);
  text-align: center;
}

@media (max-width: 768px) {
  .cross-table-inner {
    font-size: var(--text-xs);
  }

  .col-name {
    min-width: 120px;
  }
}
</style>

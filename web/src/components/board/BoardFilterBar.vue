<script setup lang="ts">
// BoardFilterBar.vue — 看板全局筛选条（2026-09-30 重构轮，对齐效果图）。
// 时间范围（统一 KxDateRangePicker）+ 粒度（首期只读「自动」）+ 刷新 + 数据源徽标（特有）。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import KxDateRangePicker from '../ui/KxDateRangePicker.vue'
import { makeDateRangePresets } from '../ui/kxDatePresets'
import type { KxDateRange } from '../ui/kx-date-types'
import { boardTrendBucketMinutes, type BoardTimeRange } from '../../utils/boardTimeRange'

const props = defineProps<{
  timeRange: BoardTimeRange
  /** 范围值（date 精度 KxDateRange）；由 BoardPanel 从 BoardTimeRange 映射 */
  rangeValue: KxDateRange | null
  loading?: boolean
  source?: string
}>()

const emit = defineEmits<{
  'apply-range': [value: KxDateRange]
  refresh: []
  more: []
}>()

const { t } = useI18n()

const presets = computed(() => makeDateRangePresets('date'))

const granularityLabel = computed(() => {
  const mins = boardTrendBucketMinutes(props.timeRange)
  if (mins >= 60) return t('dashboard.board.granHour')
  return t('dashboard.board.granMinutes', { n: mins })
})

const sourceKey = computed(() => {
  switch (props.source) {
    case 'redis_baseline_delta': return t('dashboard.board.sourceRedis')
    case 'live_sse_delta': return t('dashboard.board.sourceSse')
    case 'postgresql_baseline': return t('dashboard.board.sourcePg')
    default: return props.source ?? '—'
  }
})
</script>

<template>
  <div class="bfb">
    <div class="bfb__item">
      <span class="bfb__label">{{ t('dashboard.board.filterRange') }}</span>
      <KxDateRangePicker
        :model-value="rangeValue"
        :presets="presets"
        :max-span-days="92"
        :disabled="loading"
        @apply="emit('apply-range', $event)"
      />
      <!-- 2026-10-02：时间范围右侧「更多」入口 → 全页用量趋势分析（含供应商/
           租户/apikey/模型/指标过滤）。 -->
      <button
        type="button"
        class="bfb__more"
        :title="t('dashboard.board.trendMoreTitle')"
        @click="emit('more')"
      >
        {{ t('dashboard.board.trendMore') }} ›
      </button>
    </div>
    <div class="bfb__item">
      <span class="bfb__label">{{ t('dashboard.board.filterGran') }}</span>
      <span class="bfb__gran" :title="t('dashboard.board.granAutoTitle')">
        <b class="bfb__gran-auto">{{ t('dashboard.board.granAuto') }}</b>
        <span class="bfb__gran-value">{{ granularityLabel }}</span>
      </span>
    </div>
    <button type="button" class="bfb__refresh" :disabled="loading" @click="emit('refresh')">
      ⟳ {{ t('dashboard.refresh') }}
    </button>
    <span class="bfb__src" :title="t('dashboard.board.sourceTitle')">
      {{ t('dashboard.board.sourceLabel') }}：<b>{{ sourceKey }}</b>
      <span class="bfb__src-dot" aria-hidden="true"></span>
    </span>
  </div>
</template>

<style scoped>
.bfb {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
  padding: 9px 14px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
}
.bfb__item {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--text-muted);
}
.bfb__label {
  font-weight: 600;
  white-space: nowrap;
}
.bfb__gran {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  border: 1px solid var(--border);
  border-radius: 9px;
  padding: 4px 10px;
}
.bfb__gran-auto {
  background: color-mix(in srgb, var(--accent) 16%, var(--card));
  color: var(--accent);
  font-size: 11.5px;
  font-weight: 700;
  padding: 2px 9px;
  border-radius: 7px;
}
.bfb__gran-value {
  font-size: 11.5px;
  color: var(--text-muted);
}
.bfb__refresh {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 600;
  border-radius: 9px;
  padding: 6px 12px;
  cursor: pointer;
  transition: color 0.15s, border-color 0.15s;
}
.bfb__refresh:hover:not(:disabled) {
  color: var(--text);
  border-color: color-mix(in srgb, var(--accent) 45%, var(--border));
}
.bfb__refresh:disabled {
  opacity: 0.55;
  cursor: wait;
}
.bfb__more {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 600;
  border-radius: 9px;
  padding: 6px 12px;
  cursor: pointer;
  transition: color 0.15s, border-color 0.15s;
}
.bfb__more:hover {
  color: var(--accent);
  border-color: color-mix(in srgb, var(--accent) 45%, var(--border));
}
.bfb__src {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 10.5px;
  color: var(--text-muted);
  border: 1px dashed var(--border);
  border-radius: 8px;
  padding: 3px 9px;
}
.bfb__src b {
  color: var(--text);
}
.bfb__src-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--success);
  box-shadow: 0 0 0 3px color-mix(in srgb, var(--success) 22%, transparent);
}
@media (max-width: 768px) {
  .bfb {
    align-items: stretch;
    flex-direction: column;
    gap: 10px;
  }
  .bfb__item {
    justify-content: space-between;
  }
  .bfb__src {
    margin-left: 0;
  }
}
</style>

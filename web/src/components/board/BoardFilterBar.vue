<script setup lang="ts">
// BoardFilterBar.vue — 看板全局筛选条（2026-09-30 重构轮，对齐效果图）。
// 时间范围（统一 StatsDateRangePicker）+ 粒度（首期只读「自动」）+ 刷新 + 数据源徽标（特有）。
import { computed, useSlots } from 'vue'
import { useI18n } from 'vue-i18n'
import StatsDateRangePicker from '../analytics/StatsDateRangePicker.vue'
import type { KxDateRange } from '../ui/kx-date-types'
import { boardTrendBucketMinutes, type BoardTimeRange } from '../../utils/boardTimeRange'

const props = defineProps<{
  timeRange: BoardTimeRange
  /** 范围值（date 精度 KxDateRange）；由 BoardPanel 从 BoardTimeRange 映射 */
  rangeValue: KxDateRange | null
  loading?: boolean
  source?: string
  /**
   * 2026-11-20 口径对齐轮：英雄区汇总是否含探测流量（两处筛选条共用）。
   * true → 供应商对账口径（usage_facts 全量）；false（缺省）→ 内部对账口径。
   */
  includeProbes?: boolean
  /** 紧凑形态（2026-11-19 轮）：顶部那份与运维状态行（ops-bar 4px 12px 胶囊）对齐高度用 */
  compact?: boolean
  /** 隐藏「数据源」徽标（2026-11-20 合并行轮）：顶部那条把行尾位置让给运维 chips */
  hideSource?: boolean
}>()

const emit = defineEmits<{
  'apply-range': [value: KxDateRange]
  'update:includeProbes': [value: boolean]
  refresh: []
  more: []
}>()

const { t } = useI18n()
const slots = useSlots()

// ★ 2026-11-24 统一轮：时间范围控件改用**全站统一封装**
//   components/analytics/StatsDateRangePicker.vue。
//   ★ 等价替换，行为不变：原写法 = KxDateRangePicker + makeDateRangePresets('date')
//     + max-span-days=92；新封装内部正是这三项（逐项相同）。
//   ★ 收益：看板 / 「会话与统计」/ 「会话分析·用户」三处**结构上共用同一个组件文件**
//     ⇒ 以后改档位或改跨度只改一处，不会再出现「三处各写一套、同名不同义」。
const granularityLabel = computed(() => {
  const mins = boardTrendBucketMinutes(props.timeRange)
  if (mins >= 60) return t('dashboard.board.granHour')
  return t('dashboard.board.granMinutes', { n: mins })
})

const sourceKey = computed(() => {
  switch (props.source) {
    case 'redis_baseline_delta': return t('dashboard.board.sourceRedis')
    case 'live_sse_delta': return t('dashboard.board.sourceSse')
    case 'request_metrics':
    case 'postgresql_baseline': return t('dashboard.board.sourcePg')
    default: {
      // 2026-11-20：直查路径的新真源（usage_facts[_custom_range|_degraded_no_redis]）
      // 与旧直查标签（request_stats_minute*）归并到 PG 基线一组——
      // 徽标讲的是「这份数据从哪类存储来」，不是表名清单。
      const s = props.source ?? ''
      if (s.startsWith('usage_facts') || s.startsWith('request_stats_minute')) {
        return t('dashboard.board.sourcePg')
      }
      return s || '—'
    }
  }
})
</script>

<template>
  <div class="bfb" :class="{ 'bfb--compact': props.compact }">
    <div class="bfb__item">
      <span class="bfb__label">{{ t('dashboard.board.filterRange') }}</span>
      <StatsDateRangePicker
        :model-value="rangeValue"
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
    <!-- 2026-11-20 口径对齐轮：英雄区汇总的探测口径切换。
         不含探测 = 内部对账口径；含探测 = 供应商对账口径（对外对账因含探测流量会多一些）。
         切的是英雄区汇总的取数口径；分布/趋势图仍为业务口径。
         ⚠ 两个实例（顶部紧凑条 + 下方筛选条）都渲染本控件——1440 视口实测
         顶部行仍单行（含 ops chips）；两条是同一份状态的两个入口。
         ★ 不用 `v-if="includeProbes !== undefined"` 做「调用方可选」：
           Vue 对声明了 type:Boolean 的 prop，**未传也置 false 而不是
           undefined**（布尔转型）⇒ 那个门恒真（2026-11-20 线上实测踩坑）。 -->
    <div class="bfb__item">
      <span class="bfb__label">{{ t('dashboard.board.probeScopeLabel') }}</span>
      <span class="bfb__probe" :title="t('dashboard.board.probeScopeTitle')" role="group">
        <button
          type="button"
          class="bfb__probe-btn"
          :class="{ 'bfb__probe-btn--on': !includeProbes }"
          data-testid="board-probe-exclude"
          :disabled="loading"
          @click="emit('update:includeProbes', false)"
        >{{ t('dashboard.board.probeExclude') }}</button>
        <button
          type="button"
          class="bfb__probe-btn"
          :class="{ 'bfb__probe-btn--on': includeProbes }"
          data-testid="board-probe-include"
          :disabled="loading"
          @click="emit('update:includeProbes', true)"
        >{{ t('dashboard.board.probeInclude') }}</button>
      </span>
    </div>
    <button type="button" class="bfb__refresh" :disabled="loading" @click="emit('refresh')">
      ⟳ {{ t('dashboard.refresh') }}
    </button>
    <!-- 2026-11-23 统计标准轮：页面内嵌的「统计标准」帮助入口（插槽）。
         看板与「会话与统计」两页的请求数/费用口径不同（请求级 vs 会话级）、
         窗口不同、探测剔除面不同 ⇒ 数字本来就不该相等。这枚按钮打开
         《看板与会话统计的统计标准》正文，供用户自查两边差异。
         只有调用方显式填插槽才渲染（同一个筛选条被实例化两次，
         由调用方决定在哪一个实例里放入口）。 -->
    <slot name="help" />
    <span v-if="!hideSource" class="bfb__src" :title="t('dashboard.board.sourceTitle')">
      {{ t('dashboard.board.sourceLabel') }}：<b>{{ sourceKey }}</b>
      <span class="bfb__src-dot" aria-hidden="true"></span>
    </span>
    <!-- 2026-11-20 合并行轮：顶部条隐藏数据源徽标后，行尾位置让给运维 chips
         （后台任务/系统自检），与筛选控件合成一行，压掉独立 ops-bar 那一行。 -->
    <span v-else-if="slots.ops" class="bfb__ops">
      <slot name="ops" />
    </span>
  </div>
</template>

<style scoped>
.bfb {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  padding: 8px 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: 12px;
}
/* 顶部实例：与 ops-bar 状态胶囊（4px 12px，28px 高）同高——条自身与内部控件
   （触发器/粒度/刷新/更多/数据源）一起收紧，否则外层收了内层仍把条撑到 40px+。 */
.bfb--compact {
  padding: 3px 10px;
  gap: 10px;
}
.bfb--compact :deep(.kx-dr-trigger) {
  padding: 2px 10px;
  font-size: 11px;
}
.bfb--compact .bfb__label {
  font-size: 11px;
}
.bfb--compact .bfb__gran {
  padding: 2px 8px;
}
.bfb--compact .bfb__gran-auto {
  padding: 1px 6px;
  font-size: 10px;
}
.bfb--compact .bfb__gran-value {
  font-size: 10px;
}
.bfb--compact .bfb__refresh,
.bfb--compact .bfb__more {
  padding: 2px 10px;
  font-size: 11px;
}
.bfb--compact .bfb__src {
  padding: 2px 8px;
  font-size: 10px;
}
/* 合并行里的运维 chips 同步收紧（ops-chip 默认 4px 12px → 2px 10px），
   保证单行高度不高于原 ops-bar 行。 */
.bfb--compact .bfb__ops :deep(.ops-chip) {
  padding: 2px 10px;
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
  padding: 4px 8px;
}
.bfb__gran-auto {
  background: color-mix(in srgb, var(--accent) 16%, var(--card));
  color: var(--accent);
  font-size: 11px;
  font-weight: 700;
  padding: 2px 8px;
  border-radius: 7px;
}
.bfb__gran-value {
  font-size: 11px;
  color: var(--text-muted);
}
/* 探测口径切换：与粒度胶囊同形的二段选择器。 */
.bfb__probe {
  display: inline-flex;
  align-items: center;
  border: 1px solid var(--border);
  border-radius: 9px;
  padding: 2px;
  gap: 2px;
}
.bfb__probe-btn {
  border: 0;
  background: transparent;
  color: var(--text-muted);
  font-size: 11px;
  font-weight: 600;
  border-radius: 7px;
  padding: 2px 8px;
  cursor: pointer;
}
.bfb__probe-btn--on {
  background: color-mix(in srgb, var(--accent) 16%, var(--card));
  color: var(--accent);
}
.bfb__probe-btn:disabled {
  opacity: 0.55;
  cursor: wait;
}
.bfb--compact .bfb__probe {
  padding: 1px;
}
.bfb--compact .bfb__probe-btn {
  padding: 1px 7px;
  font-size: 10px;
}
.bfb__refresh {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text-muted);
  font-size: 12px;
  font-weight: 600;
  border-radius: 9px;
  padding: 4px 12px;
  cursor: pointer;
  transition: color 0.15s var(--kx-motion-ease-out), border-color 0.15s var(--kx-motion-ease-out);
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
  padding: 4px 12px;
  cursor: pointer;
  transition: color 0.15s var(--kx-motion-ease-out), border-color 0.15s var(--kx-motion-ease-out);
}
.bfb__more:hover {
  color: var(--accent);
  border-color: color-mix(in srgb, var(--accent) 45%, var(--border));
}
.bfb__src {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  color: var(--text-muted);
  border: 1px dashed var(--border);
  border-radius: 8px;
  padding: 3px 8px;
}
.bfb__src b {
  color: var(--text);
}
/* 运维 chips 容器：顶替数据源徽标占行尾（margin-left: auto 同位语义）。 */
.bfb__ops {
  margin-left: auto;
  display: inline-flex;
  align-items: center;
  min-width: 0;
}
.bfb__ops :deep(.ops-bar) {
  flex-wrap: wrap;
  row-gap: 4px;
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
    gap: 8px;
  }
  .bfb__item {
    justify-content: space-between;
  }
  .bfb__src {
    margin-left: 0;
  }
}
</style>

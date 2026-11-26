<script setup lang="ts">
/**
 * SaCostCell —— 会话分析族的成本格（N9 修复轮）。
 *
 * ★ 为什么要有这个组件：会话分析族的 `total_cost` = internal_cost(美元) +
 *   external_cost(积分) ⇒ **跨单位相加**（framework §3.2）⇒ 渲染成
 *   `$12.34` 等于凭空造出一个不存在的美元金额，而同一页的脚注又明说
 *   「两种单位不同、不能当作同一个金额」⇒ 表格与脚注互相打脸。
 *   ⇒ 口径：**一格两行**，各带自己的单位标签与数值（与总览页当年的
 *     「两张卡」写法同构）。
 *
 * ★ 数值口径与「—」规则见同目录 saCostSplit.ts（那里是唯一真源）。
 *   外部成本为 0 ⇒ 渲染 '—' + 标题说明（积分写侧未落地 ⇒ 不能把
 *   「没采到」讲成「测得是 0」）。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  creditsCostText,
  externalCostIsPlaceholder,
  usdCostText,
} from '../saCostSplit'

const props = defineProps<{
  /** 内部成本（美元）。 */
  internalCost?: number | null
  /** 外部成本（积分）。 */
  externalCost?: number | null
  terminalCost?: number | null
  usageCredits?: number | null
}>()

const { t } = useI18n()

const terminal = computed(() => props.terminalCost !== undefined)
const terminalText = computed(() => usdCostText(props.terminalCost, t))
const usageCreditsText = computed(() => creditsCostText(props.usageCredits, t))
const externalPlaceholder = computed(() => externalCostIsPlaceholder(props.externalCost))

/** 内部成本（美元）：缺值 ⇒ 纯占位符；否则带单位。 */
const internalText = computed(() => usdCostText(props.internalCost, t))

/** 外部成本（积分）：占位符形态 ⇒ 纯 '—'；否则带单位。 */
const externalText = computed(() => creditsCostText(props.externalCost, t))
</script>

<template>
  <div class="sa-cost-cell" data-testid="sa-cost-cell">
    <template v-if="terminal">
      <div class="sa-cost-cell__row">
        <span class="sa-cost-cell__label">{{ t('sessionAnalytics.metric.totalCost') }}</span>
        <span class="sa-cost-cell__value">{{ terminalText }}</span>
      </div>
      <div class="sa-cost-cell__row">
        <span class="sa-cost-cell__value">{{ usageCreditsText }}</span>
      </div>
    </template>
    <template v-else>
    <div class="sa-cost-cell__row">
      <span class="sa-cost-cell__label">{{ t('sessionAnalytics.metric.internalCost') }}</span>
      <span class="sa-cost-cell__value">{{ internalText }}</span>
    </div>
    <div class="sa-cost-cell__row">
      <span class="sa-cost-cell__label">{{ t('sessionAnalytics.metric.externalCost') }}</span>
      <span
        class="sa-cost-cell__value"
        :class="{ 'sa-cost-cell__value--placeholder': externalPlaceholder }"
        :title="externalPlaceholder ? t('sessionAnalytics.externalCostNotCarried') : undefined"
        >{{ externalText }}</span
      >
    </div>
    </template>
  </div>
</template>

<style scoped>
.sa-cost-cell {
  display: flex;
  flex-direction: column;
  gap: 2px;
  white-space: nowrap;
}
.sa-cost-cell__row {
  display: flex;
  align-items: baseline;
  gap: 6px;
}
.sa-cost-cell__label {
  font-size: 11px;
  color: var(--muted, var(--text-secondary));
}
.sa-cost-cell__value {
  font-variant-numeric: tabular-nums;
}
.sa-cost-cell__value--placeholder {
  color: var(--muted, var(--text-secondary));
}
</style>

<script setup lang="ts">
// StatCard.vue — 通用统计卡片（2026-09-12，方案 §4.5.3）
//
// 统一全站两套平行实现：`.stat-card`（17 个文件）与 `.summary-card`
// （4 个文件，summary-good/warn/bad 变体）。基础观感复用全局 style.css
// 的 .stat-card / .stat-card--compact（零视觉回归），tone 用左侧语义色
// 边框表达（对齐原 summary-* 与 stat-overview-card 的强调方式）。
//
//   <StatCard :label="totalLabel" :value="total" />
//   <StatCard :label="errorLabel" :value="n" tone="danger" sub="+12%" compact />
//   <StatCard :label="…"><template #value>富内容</template></StatCard>
withDefaults(defineProps<{
  /** 卡片标签（已翻译字符串） */
  label: string
  /** 主数值；复杂内容用 #value 插槽 */
  value?: string | number
  /** 数值下方的次级说明（同比/口径等） */
  sub?: string
  /** 语义色：neutral 无边框，success/warning/danger 左侧色条 */
  tone?: 'neutral' | 'success' | 'warning' | 'danger'
  /** 紧凑模式（对齐全局 .stat-card--compact：小内边距/灰底/11px） */
  compact?: boolean
}>(), {
  value: '',
  sub: '',
  tone: 'neutral',
  compact: false,
})
</script>

<template>
  <div
    class="stat-card app-stat-card"
    :class="[
      compact && 'stat-card--compact',
      tone !== 'neutral' && `app-stat-card--${tone}`,
    ]"
  >
    <div class="label app-stat-card__label">{{ label }}</div>
    <div class="value app-stat-card__value">
      <slot name="value">{{ value }}</slot>
    </div>
    <div v-if="sub || $slots.sub" class="sub app-stat-card__sub">
      <slot name="sub">{{ sub }}</slot>
    </div>
  </div>
</template>

<style scoped>
/* 基础 .stat-card/.stat-card--compact/.label/.value/.sub 观感来自全局
 * style.css（与存量 17 处使用完全一致），这里只补 tone 色条与响应式。 */
.app-stat-card--success { border-inline-start: 3px solid var(--success); }
.app-stat-card--warning { border-inline-start: 3px solid var(--warning); }
.app-stat-card--danger  { border-inline-start: 3px solid var(--danger); }

/* 紧凑模式数值：对齐 RequestLogsView 原内联实现（16px/600），
 * 取代其 20 行 inline style 重复 */
.stat-card--compact .app-stat-card__value {
  font-size: 16px;
  font-weight: 600;
  margin-top: 2px;
}

/* 小屏：非紧凑卡数值从 22px 收敛到 18px，长数字不撑破单列布局 */
@media (max-width: 480px) {
  .app-stat-card:not(.stat-card--compact) .app-stat-card__value {
    font-size: 18px;
  }
}
</style>

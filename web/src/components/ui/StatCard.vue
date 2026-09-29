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
//   <StatCard :label="…" icon="📥"><template #value>富内容</template></StatCard>
//   <StatCard :label="…" icon="📈"><template #spark>迷你趋势（svg/canvas）</template></StatCard>
//
// 2026-09-30 统计 UI 优化轮：新增 icon（语义色图标块，参考看板式 KPI 大卡）
// 与 #spark 槽（右侧迷你趋势）。两者均缺省不渲染，存量 17 处用法零影响。
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
  /** 图标（emoji 或字符），提供后左侧渲染 tone 色图标块 */
  icon?: string
}>(), {
  value: '',
  sub: '',
  tone: 'neutral',
  compact: false,
  icon: '',
})
</script>

<template>
  <div
    class="stat-card app-stat-card"
    :class="[
      compact && 'stat-card--compact',
      tone !== 'neutral' && `app-stat-card--${tone}`,
      icon && 'app-stat-card--with-icon',
    ]"
  >
    <div v-if="icon" class="app-stat-card__icon" :class="`app-stat-card__icon--${tone}`">{{ icon }}</div>
    <div class="app-stat-card__main">
      <div class="label app-stat-card__label">{{ label }}</div>
      <div class="value app-stat-card__value">
        <slot name="value">{{ value }}</slot>
      </div>
      <div v-if="sub || $slots.sub" class="sub app-stat-card__sub">
        <slot name="sub">{{ sub }}</slot>
      </div>
    </div>
    <div v-if="$slots.spark" class="app-stat-card__spark"><slot name="spark" /></div>
  </div>
</template>

<style scoped>
/* 基础 .stat-card/.stat-card--compact/.label/.value/.sub 观感来自全局
 * style.css（与存量 17 处使用完全一致），这里只补 tone 色条与响应式。 */
.app-stat-card--success { border-inline-start: 3px solid var(--success); }
.app-stat-card--warning { border-inline-start: 3px solid var(--warning); }
.app-stat-card--danger  { border-inline-start: 3px solid var(--danger); }

/* 图标形态（2026-09-30）：左侧语义色图标块 + 内容列 + 右侧 spark 槽 */
.app-stat-card--with-icon {
  display: flex;
  gap: 12px;
  align-items: flex-start;
}
.app-stat-card__main { min-width: 0; flex: 1; }
.app-stat-card__icon {
  width: 38px;
  height: 38px;
  border-radius: 10px;
  flex: none;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 18px;
}
.app-stat-card__icon--neutral { background: var(--bg-subtle); }
.app-stat-card__icon--success { background: var(--success-soft); }
.app-stat-card__icon--warning { background: var(--warning-soft); }
.app-stat-card__icon--danger  { background: var(--danger-soft); }
.app-stat-card__spark {
  align-self: flex-end;
  min-width: 72px;
  max-width: 110px;
  flex: none;
}

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

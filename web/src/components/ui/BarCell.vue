<script setup lang="ts">
// BarCell.vue — 分布表「名称 + 占比条」单元格（2026-09-30 统计 UI 优化轮）
//
// 对标数据看板分布表（模型分布 / 分组使用分布）的行首形态：主名称 +
// 副标注 + 底部 4px 占比条。pct 为该行指标占区间总额的比例（0-100），
// 由调用方按当前指标口径（Token / 金额）计算。
//
//   <BarCell :pct="43.1" tone="primary">
//     <template #name>claude-opus-5 <span class="cell-sub">Anthropic</span></template>
//   </BarCell>
withDefaults(defineProps<{
  /** 占比条的宽度（0-100，通常为该行指标 / 区间最大值 或总额） */
  pct: number
  /** 语义色：primary 蓝 / success 绿 / purple / cyan */
  tone?: 'primary' | 'success' | 'purple' | 'cyan'
}>(), {
  tone: 'primary',
})
</script>

<template>
  <div class="bar-cell">
    <div class="bar-cell__name"><slot name="name" /></div>
    <div class="bar-cell__track">
      <div class="bar-cell__fill" :class="`bar-cell__fill--${tone}`" :style="{ width: `${Math.min(100, Math.max(0, pct))}%` }" />
    </div>
  </div>
</template>

<style scoped>
.bar-cell { min-width: 140px; }
.bar-cell__name {
  font-weight: 600;
  font-size: 13px;
  margin-bottom: 4px;
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.bar-cell__track {
  height: 4px;
  border-radius: 2px;
  background: var(--border);
  overflow: hidden;
}
.bar-cell__fill {
  height: 100%;
  border-radius: 2px;
  transition: width 0.3s ease;
}
.bar-cell__fill--primary { background: var(--accent); }
.bar-cell__fill--success { background: var(--success); }
.bar-cell__fill--purple { background: var(--purple); }
.bar-cell__fill--cyan { background: var(--probe-cyan); }
</style>

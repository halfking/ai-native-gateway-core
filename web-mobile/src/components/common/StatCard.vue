<script setup lang="ts">
// StatCard — 汇总指标卡（值 tabular-nums；hint 放降级口径提示）。
defineProps<{
  label: string
  value: string
  hint?: string
}>()
</script>

<template>
  <div class="stat-card data-card">
    <div class="stat-card__label">{{ label }}</div>
    <div class="stat-card__value num">{{ value }}</div>
    <div v-if="hint" class="stat-card__hint">{{ hint }}</div>
  </div>
</template>

<style scoped>
.stat-card__label {
  font-size: 0.75rem;
  color: var(--app-text-secondary);
}

.stat-card__value {
  font-size: 1.25rem;
  font-weight: 600;
  margin-top: 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  /* ★ 值必须允许换行（§4.6.74 / D-TRUNC-01）。
     Android `font_scale` 只放大**渲染字号**，rem / em 都不跟着放大（§4.6.72 真机：
     font_scale=2.0 时 html 计算字号 32px、但 1rem 仍 16px、9em 仍 9×14=126px），
     而外层 padding / 网格轨道 / gap 全是 rem ⇒ **版式一点没变宽**，只有数字在变大。
       font_scale 1.0 / 1.3 / 1.5 / 2.0 下的溢出：0 / 0 / 14px / 69~86px
     所以没法靠「版式让路」解决，只能让**内容**适配固定版式。

     ⚠️ `overflow-wrap` 与 `white-space` 必须**成对**：`overflow-wrap` 只在
     「允许换行」的前提下才制造断点，元素还带 `nowrap` 时它完全惰性（实测：
     只加 break-word，溢出读数与不加时逐项相同）。
     ⚠️ 千分位数字按 UAX #14 的 LB25 数值表达式规则本身**不可断**，必须靠
     `break-word` 人为造出断点（阳性对照：手动插 ZWSP 后同处立刻由单行变两行）。
     ⚠️ 选 `break-word` 而非 `anywhere`：后者参与 min-content 尺寸计算，会改动
     网格轨道下限；本组件只放量值（调用点见 statCardValueFit.spec.ts 守卫），不需要为此动版式。 */
  white-space: normal;
  overflow-wrap: break-word;
  /* text-overflow 保留为兜底：将来若有调用点传入不可断的标识符，
     break-word 之外的退化情形仍应截断而不是撑破卡片。 */
}

.stat-card__hint {
  font-size: 0.6875rem;
  color: var(--app-warning);
  margin-top: 2px;
}
</style>

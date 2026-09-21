<script setup lang="ts">
// EmptyState.vue — 全站共享空态组件（2026-09-04）
//
// 抽取自各 view 重复的 `<div class="empty-state">暂无数据</div>` 模板
// （ProbeHealthDetailView / SelfCheckPanel / ModulesView / ProxyView /
// PromptInjectionSettingsView …）。原各处仅 padding 与颜色 token 略有差异，
// 通过 padding prop 保持原视觉，颜色统一 var(--muted)（缺失时回落
// --text-secondary，与多数原实现一致）。
withDefaults(defineProps<{
  /** 空态文案（已翻译的字符串）；也可用默认插槽放富内容 */
  text?: string
  /** 与原各 view 的 .empty-state padding 保持一致 */
  padding?: string
}>(), {
  text: '',
  padding: '40px',
})
</script>

<template>
  <div class="app-empty-state" :style="{ padding }">
    <slot>{{ text }}</slot>
  </div>
</template>

<style scoped>
.app-empty-state {
  text-align: center;
  color: var(--muted, var(--text-secondary));
  font-size: 13px;
}
</style>

<script setup lang="ts">
// AppSpinner.vue — 全站共享加载态组件（2026-09-04）
//
// 抽取自各 view 重复的 `<span class="spinner"></span> 加载文案` 模板
// （OutputComplianceView / StorageOverview / ApprovalConfigView /
// ApprovalDetailView / ApprovalListView …），视觉保持一致：
// 14px 旋转圆盘 + 可选文案，颜色走既有 token（--surface-secondary / --accent / --muted）。
withDefaults(defineProps<{
  /** 加载文案（已翻译的字符串）；空则只渲染圆盘 */
  label?: string
  /** 内联场景（表格单元格内）去掉默认内边距 */
  inline?: boolean
}>(), {
  label: '',
  inline: false,
})
</script>

<template>
  <div
    class="app-spinner"
    :class="{ 'app-spinner--inline': inline }"
    role="status"
    :aria-label="label || undefined"
  >
    <span class="app-spinner__disc" aria-hidden="true"></span>
    <span v-if="label" class="app-spinner__label">{{ label }}</span>
  </div>
</template>

<style scoped>
.app-spinner {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 24px;
  color: var(--muted, var(--text-secondary));
}
.app-spinner--inline {
  display: inline-flex;
  padding: 0;
}
.app-spinner__disc {
  display: inline-block;
  width: 14px;
  height: 14px;
  border: 2px solid var(--surface-secondary);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: app-spinner-spin 0.8s linear infinite;
  flex: none;
}
.app-spinner__label {
  font-size: 13px;
}
@keyframes app-spinner-spin {
  to { transform: rotate(360deg); }
}
</style>

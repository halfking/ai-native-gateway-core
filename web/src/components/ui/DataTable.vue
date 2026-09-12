<script setup lang="ts">
/**
 * DataTable.vue — 包裹模式响应式表格容器（2026-09-13，方案 §4.5.5 姿势 1）。
 *
 * 只包一层：横向滚动容器 + 统一 loading（AppSpinner）+ 空态（EmptyState）
 * + min-width 列宽保护；视图原 <table> 结构原样放进默认插槽，零重构获得
 * 移动端基础可用性。列配置模式（行转卡片）见方案后续步骤。
 *
 *   <DataTable :loading="loading" :empty="!rows.length" empty-text="暂无数据" min-width="860px">
 *     <table>…原表格结构不动…</table>
 *   </DataTable>
 */
import AppSpinner from '../AppSpinner.vue'
import EmptyState from '../EmptyState.vue'

withDefaults(defineProps<{
  /** 加载中：以 AppSpinner 替换表格区域 */
  loading?: boolean
  /** 空数据：以 EmptyState 替换表格区域 */
  empty?: boolean
  /** 空态文案（已翻译字符串） */
  emptyText?: string
  /** 是否开启横向滚动容器 */
  scrollable?: boolean
  /** 表格最小宽度（窄屏下撑出容器内滚动，避免挤压列） */
  minWidth?: string
}>(), {
  loading: false,
  empty: false,
  emptyText: '',
  scrollable: true,
  minWidth: '720px',
})
</script>

<template>
  <div class="app-data-table">
    <div v-if="loading" class="app-data-table__loading">
      <AppSpinner />
    </div>
    <EmptyState v-else-if="empty" :text="emptyText" />
    <div
      v-else
      class="app-data-table__scroller"
      :class="{ 'app-data-table__scroller--scroll': scrollable }"
      :style="scrollable ? { '--dt-min-width': minWidth } : undefined"
    >
      <slot />
    </div>
  </div>
</template>

<style scoped>
.app-data-table__scroller--scroll {
  overflow-x: auto;
  -webkit-overflow-scrolling: touch;
}
/* 窄屏列宽保护：表格至少 min-width，超出部分容器内滚动而非挤压 */
.app-data-table__scroller--scroll > :slotted(table) {
  min-width: var(--dt-min-width, 720px);
}
.app-data-table__loading {
  padding: var(--kx-space-4) 0;
}
</style>

<script setup lang="ts">
// PaginationBar.vue — 通用分页栏（2026-09-12，方案 §4.5.6）
//
// 抽取自 10+ 视图手写的 `.pagination-bar` 骨架（RequestLogsView 同文件内
// 曾复制两份）：总数/页码信息 + 每页条数下拉 + 上一页/下一页。样式与原
// 视图实现一致（.pagination-bar 全局观感），断点统一为标准 768px
// （方案 §4.2；原 RequestLogsView 用的是 720px，属待收敛的碎片断点）。
// 文案走 common.pagination.* 词条，各视图不再硬编码中文。
//
// 状态由外部持有（配合 usePagination），组件只做展示与事件转发：
//   <PaginationBar :page="page" :page-size="pageSize" :total="total"
//     :page-sizes="[50,100,200,500]" @prev="pager.prev" @next="pager.next"
//     @change-size="onPageSizeChange" />
import { computed, useId } from 'vue'
import { useI18n } from 'vue-i18n'

const props = withDefaults(defineProps<{
  page: number
  pageSize: number
  total: number
  /** 每页条数可选项；传空数组隐藏下拉 */
  pageSizes?: number[]
}>(), {
  pageSizes: () => [20, 50, 100, 200],
})

const emit = defineEmits<{
  (e: 'prev'): void
  (e: 'next'): void
  /** 值为新的 pageSize；视图侧更新 pageSize 后通常调用 pager.reset() */
  (e: 'change-size', size: number): void
}>()

const { t } = useI18n()

const sizeSelectId = useId()
const pages = computed(() => Math.max(1, Math.ceil(props.total / props.pageSize)))

function onSizeChange(e: Event) {
  const value = Number((e.target as HTMLSelectElement).value)
  if (Number.isFinite(value) && value > 0) emit('change-size', value)
}
</script>

<template>
  <div class="pagination-bar">
    <div class="pagination-info">
      <span>{{ t('common.pagination.total', { n: total.toLocaleString() }) }}</span>
      <span>{{ t('common.pagination.pageOf', { page, pages }) }}</span>
      <span class="pagination-divider">·</span>
      <label v-if="pageSizes.length" class="page-size-label" :for="sizeSelectId">
        {{ t('common.pagination.perPage') }}
      </label>
      <select
        v-if="pageSizes.length"
        :id="sizeSelectId"
        class="page-size-select"
        :value="pageSize"
        :aria-label="t('common.pagination.perPage')"
        @change="onSizeChange"
      >
        <option v-for="size in pageSizes" :key="size" :value="size">{{ size }}</option>
      </select>
    </div>
    <div class="pagination-controls">
      <button
        class="btn btn-ghost btn-sm"
        type="button"
        :disabled="page <= 1"
        @click="emit('prev')"
      >{{ t('common.pagination.previous') }}</button>
      <button
        class="btn btn-ghost btn-sm"
        type="button"
        :disabled="page >= pages"
        @click="emit('next')"
      >{{ t('common.pagination.next') }}</button>
    </div>
  </div>
</template>

<style scoped>
.pagination-bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 12px;
  padding: 8px 12px;
  background: var(--card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  flex-wrap: nowrap;
}
.pagination-info {
  display: flex;
  align-items: center;
  gap: 10px;
  color: var(--muted);
  font-size: 12px;
  flex-wrap: nowrap;
  white-space: nowrap;
  flex-shrink: 0;
  min-width: 0;
}
.pagination-controls {
  display: flex;
  gap: 8px;
  flex-wrap: nowrap;
  flex-shrink: 0;
}
.page-size-select {
  width: auto;
  min-width: 0;
  max-width: 96px;
  padding: 2px 6px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 4px;
  color: var(--text);
  font-size: 12px;
}
.page-size-label {
  color: var(--muted);
  font-size: 12px;
  cursor: pointer;
}
.pagination-divider {
  color: var(--muted);
  opacity: 0.6;
}
@media (max-width: 768px) {
  .pagination-bar {
    flex-wrap: wrap;
  }
  .pagination-info,
  .pagination-controls {
    width: 100%;
    justify-content: space-between;
  }
  /* 触摸目标: 上一页/下一页在移动端至少 36px 高（方案 §2.3 ≥44px 由
     .btn 全局尺寸负责，这里只兜底 select 的内边距） */
  .page-size-select {
    padding: 4px 8px;
  }
}
</style>

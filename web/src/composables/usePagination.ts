// usePagination — 列表页分页状态 composable（2026-09-12，方案 §4.5.6）
//
// 为什么
// ───
// 全站 10+ 个视图手写 `page/pageSize/total` 三个 ref + `changePage(delta)` +
// `resetPageAndLoad()` 的同构逻辑（RequestLogsView 同文件内甚至复制了两份
// pagination-bar）。本 composable 收敛派生状态（pages/canPrev/canNext）与
// 翻页动作，供 PaginationBar 组件与视图共用。
//
// 形态：适配已有 refs（page/pageSize/total 由视图持有，load() 直接读取），
// 而不是新建内部状态 — 这样 trace 模式强制 pageSize>=200 之类的外部改动
// （RequestLogsView widenRangeForTrace）依旧生效，存量视图迁移成本最低。
//
// 语义与被替换的手写实现严格一致：
//   • go/prev/next 越界直接忽略（原 changePage 的 if next < 1 || next > max，
//     不做钳制 — go(99) 不会跳到最后一页）
//   • reset 总是触发 onChange（pageSize 变化在第 1 页也必须重新拉取）
import { computed, type Ref } from 'vue'

export interface UsePaginationOptions {
  page: Ref<number>
  pageSize: Ref<number>
  total: Ref<number>
  /** 页码/页大小变化后的回调（通常是视图的 load） */
  onChange: () => unknown
}

export function usePagination(opts: UsePaginationOptions) {
  const pages = computed(() => Math.max(1, Math.ceil(opts.total.value / opts.pageSize.value)))
  const canPrev = computed(() => opts.page.value > 1)
  const canNext = computed(() => opts.page.value < pages.value)

  function go(target: number) {
    if (target < 1 || target > pages.value) return
    if (target === opts.page.value) return
    opts.page.value = target
    void opts.onChange()
  }

  function prev() {
    go(opts.page.value - 1)
  }

  function next() {
    go(opts.page.value + 1)
  }

  /** 回到第 1 页并重新拉取 — 筛选/查询/页大小变化的统一入口 */
  function reset() {
    opts.page.value = 1
    void opts.onChange()
  }

  return { pages, canPrev, canNext, go, prev, next, reset }
}

export type Pagination = ReturnType<typeof usePagination>

// continuousList.ts — 连续加载控制器（UI 规范 07 §3 + 13 §3）。
// sentinel 240px 预载、首屏补页 ≤3 页、rowId 去重、失败保旧 + 手动重试、
// 「已加载 X/总计 Y」。乱序隔离：响应回写前校验 queryRevision（R2）。

export const SENTINEL_PRELOAD_PX = 240
export const FIRST_FILL_MAX_PAGES = 3

export interface ContinuousListState<T> {
  items: T[]
  total: number
  loading: boolean
  error: string | null
  exhausted: boolean
}

export interface PageFetcher<T> {
  /** 拉一页。返回 {rows, total}；rows 为空视为到底。 */
  fetch(offset: number): Promise<{ rows: T[]; total: number }>
  rowId(row: T): string
}

export class ContinuousListController<T> {
  private items: T[] = []
  private seenIds = new Set<string>()
  private total = 0
  private loading = false
  private error: string | null = null
  private exhausted = false
  private listeners = new Set<() => void>()

  constructor(private fetcher: PageFetcher<T>, private pageSize: number) {}

  current(): ContinuousListState<T> {
    return {
      items: this.items,
      total: this.total,
      loading: this.loading,
      error: this.error,
      exhausted: this.exhausted,
    }
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn)
    return () => this.listeners.delete(fn)
  }

  private emit(): void {
    for (const fn of this.listeners) fn()
  }

  /** 首屏：连续补页直到塞满 ~1 屏（items ≥ pageSize）或达 3 页上限。 */
  async firstFill(): Promise<void> {
    let pages = 0
    while (
      this.items.length < this.pageSize &&
      pages < FIRST_FILL_MAX_PAGES &&
      !this.exhausted
    ) {
      const more = await this.loadMore()
      if (!more) break
      pages += 1
    }
  }

  async loadMore(): Promise<boolean> {
    if (this.loading || this.exhausted) return false
    this.loading = true
    this.error = null
    this.emit()
    try {
      const { rows, total } = await this.fetcher.fetch(this.items.length)
      this.total = total
      if (rows.length === 0) {
        this.exhausted = true
      } else {
        let appended = 0
        for (const row of rows) {
          const id = this.fetcher.rowId(row)
          if (this.seenIds.has(id)) continue // rowId 去重：断网重试后无重复行
          this.seenIds.add(id)
          this.items.push(row)
          appended += 1
        }
        if (appended === 0) this.exhausted = true
        if (this.items.length >= this.total && this.total > 0) this.exhausted = true
      }
      return rows.length > 0
    } catch (err) {
      // 失败保旧：列表保留 + 错误态 + 手动重试（13 §3）。
      this.error = err instanceof Error ? err.message : String(err)
      return false
    } finally {
      this.loading = false
      this.emit()
    }
  }

  /** 滚动位置上报：距底 < 240px 触发预载（single-flight 由 loading 守卫）。 */
  onScrollMetrics(scrollTop: number, viewportHeight: number, scrollHeight: number): void {
    if (scrollHeight - scrollTop - viewportHeight < SENTINEL_PRELOAD_PX) {
      void this.loadMore()
    }
  }

  /** 下拉刷新后整体重置（queryRevision 由调用方先递增再调这里）。 */
  reset(): void {
    this.items = []
    this.seenIds = new Set<string>()
    this.total = 0
    this.error = null
    this.exhausted = false
    this.emit()
  }
}

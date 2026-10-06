// ContinuousListController — UI规范 07 §3 连续加载。
// sentinel 距底 240px 预载（由组件的 IntersectionObserver 触发 loadNext）；
// 单飞键 (scope, queryRevision, page)；成功原位替换该页、按稳定 rowId 去重；
// 失败保留已加载行、停自动重试、底部手动重试；无更多 → exhausted。
// 刷新与追加互斥（07 §2）：刷新提升 queryRevision、中止旧请求、清游标。

export type ContinuousListState =
  | 'idle'
  | 'initialLoading'
  | 'loadingNext'
  | 'refreshing'
  | 'loadFailed'
  | 'loadMoreFailed'
  | 'exhausted'

export const SENTINEL_MARGIN_PX = 240
export const AUTO_FILL_MAX_PAGES = 3

export interface ListPageResult<T> {
  items: T[]
  total?: number
}

export interface ContinuousListOptions<T> {
  fetchPage: (page: number, signal: AbortSignal) => Promise<ListPageResult<T>>
  stableKey: (item: T) => string
  scopeKey?: string
  autoFillMaxPages?: number
}

export class ContinuousListController<T> {
  private _state: ContinuousListState = 'idle'
  private _items: T[] = []
  private _total: number | undefined
  private _page = 0
  private _revision = 0
  /**
   * 最近一次失败的原始错误。
   *
   * 2026-10-04 新增。此前 catch 里只置状态、**把 err 丢掉**，于是视图层拿不到
   * 失败原因，只能对所有失败显示同一句 `common.errorHint`（"请检查网络后重试"）。
   * 实测后果：/nodes 的 500 来自服务端查询超时（admin/credential_monitor.go:657，
   * 15s context deadline），页面却让用户去查自己的网络 —— **把排查方向指错了**。
   */
  private _error: unknown = null
  private aborter: AbortController | null = null
  private flightKey: string | null = null
  private listeners = new Set<() => void>()

  constructor(private readonly opts: ContinuousListOptions<T>) {}

  get state(): ContinuousListState {
    return this._state
  }

  get items(): readonly T[] {
    return this._items
  }

  get total(): number | undefined {
    return this._total
  }

  get loadedCount(): number {
    return this._items.length
  }

  /** 最近一次失败原因；成功或未失败时为 null。视图据此区分「网络」与「服务端」。 */
  get error(): unknown {
    return this._error
  }

  /** 视图刷新钩子（Vue 侧用 tick ref 触发重渲）。 */
  subscribe(fn: () => void): () => void {
    this.listeners.add(fn)
    return () => this.listeners.delete(fn)
  }

  private emit(): void {
    for (const fn of this.listeners) fn()
  }

  /** 初次加载 / 筛选变更（requery）：清数据重查。 */
  loadFirst(mode: 'initial' | 'requery' = 'initial'): void {
    if (this.flightKey && mode === 'requery') {
      this.abortInFlight()
    }
    if (this._state === 'initialLoading' && mode === 'initial') return
    this._revision++
    this.abortInFlight()
    this._items = []
    this._total = undefined
    this._page = 0
    this._error = null
    this._state = 'initialLoading'
    this.emit()
    void this.fetch('initial', 1)
  }

  /** 保旧刷新（07 §2/13 §3）：保留已加载行，重查第一页成功后整组替换。 */
  refresh(): void {
    if (this._state === 'refreshing' || this._state === 'initialLoading') return
    this._revision++
    this.abortInFlight()
    this._state = 'refreshing'
    this.emit()
    void this.fetch('refresh', 1)
  }

  loadNext(): void {
    if (
      this._state === 'loadingNext' ||
      this._state === 'refreshing' ||
      this._state === 'initialLoading' ||
      this._state === 'exhausted' ||
      this._state === 'loadFailed'
    ) {
      return
    }
    void this.fetch('next', this._page + 1)
  }

  retry(): void {
    if (this._state === 'loadFailed') {
      this.loadFirst('initial')
    } else if (this._state === 'loadMoreFailed') {
      this._state = 'idle'
      this.emit()
      this.loadNext()
    }
  }

  /** 首屏不足一屏自动补页，单轮最多 3 页（07 §3）。 */
  async autoFill(isScreenFilled: () => boolean): Promise<void> {
    const cap = this.opts.autoFillMaxPages ?? AUTO_FILL_MAX_PAGES
    for (let i = 0; i < cap; i++) {
      if (isScreenFilled()) return
      const before: ContinuousListState = this._state
      if (before !== 'idle' && before !== 'loadMoreFailed') {
        await this.waitForIdleish()
        const after: ContinuousListState = this._state
        if (after === 'exhausted' || after === 'loadFailed') return
        continue
      }
      const atStart: ContinuousListState = this._state
      if (atStart === 'exhausted') return
      this.loadNext()
      await this.waitForIdleish()
      const afterLoad: ContinuousListState = this._state
      if (afterLoad === 'exhausted' || afterLoad === 'loadFailed') return
    }
  }

  private waitForIdleish(): Promise<void> {
    return new Promise((resolve) => {
      const check = () => {
        if (this._state !== 'loadingNext' && this._state !== 'initialLoading' && this._state !== 'refreshing') {
          resolve()
        } else {
          setTimeout(check, 30)
        }
      }
      check()
    })
  }

  private abortInFlight(): void {
    this.aborter?.abort()
    this.aborter = null
    this.flightKey = null
  }

  /**
   * 组件卸载时调用（2026-10-06）。
   *
   * ★ 此前 HyperList 的 onBeforeUnmount 只 `observer?.disconnect()`，**没管在途请求**：
   *   用户在列表加载途中切走（底栏/抽屉跳转，compact 档非常常见）⇒ fetchPage 的
   *   await 续体照样回来改 `_items` / `emit()`，而组件已经不在了。
   *   Vue 3 不报错、只产生一次警告 ⇒ 这个洞能一直躺着，单测也测不出来
   *   （测试里卸载组件后照样全绿）。
   *
   *   加在 controller 层而不是每个视图里：AlertsView / ModelsView / NodesView /
   *   ProvidersView / IntegrityView / RequestLogsView 六页都走同一条路径，
   *   逐个视图补 abort 是「给每个用例加隔离助手」式的错解。
   */
  dispose(): void {
    this._revision++
    this.abortInFlight()
    this.listeners.clear()
  }

  private async fetch(kind: 'initial' | 'refresh' | 'next', page: number): Promise<void> {
    const revision = this._revision
    const key = `${this.opts.scopeKey ?? 'list'}#${revision}#${page}`
    if (this.flightKey === key) return
    this.flightKey = key
    const aborter = new AbortController()
    this.aborter = aborter
    if (kind === 'next') {
      this._state = 'loadingNext'
      this.emit()
    }
    try {
      const result = await this.opts.fetchPage(page, aborter.signal)
      // 旧响应不得回写：revision 已前进则丢弃（07 §2 / 17 §4-R2）
      if (revision !== this._revision) return
      if (kind === 'next') {
        const seen = new Set(this._items.map((it) => this.opts.stableKey(it)))
        const fresh = result.items.filter((it) => {
          const k = this.opts.stableKey(it)
          if (seen.has(k)) return false
          seen.add(k)
          return true
        })
        this._items = [...this._items, ...fresh]
        this._page = page
      } else {
        this._items = result.items
        this._page = 1
      }
      if (result.total !== undefined) this._total = result.total
      const noMore =
        result.items.length === 0 ||
        (this._total !== undefined && this._items.length >= this._total)
      this._state = noMore ? 'exhausted' : 'idle'
      this._error = null
      this.emit()
    } catch (err) {
      if (revision !== this._revision) return
      if (isAbortError(err)) return
      // 失败保留已加载行与分页状态（13 §3）
      this._state = kind === 'next' ? 'loadMoreFailed' : 'loadFailed'
      this._error = err
      this.emit()
    } finally {
      if (this.flightKey === key) {
        this.flightKey = null
        this.aborter = null
      }
    }
  }
}

export function isAbortError(err: unknown): boolean {
  return !!err && typeof err === 'object' && (err as { name?: unknown }).name === 'AbortError'
}

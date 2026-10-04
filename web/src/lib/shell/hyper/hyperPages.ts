/**
 * hyperPages — 连续加载的页集合与状态机（docs/UI规范/00 §5.2 · H3，参考规范 07 §3）。
 *
 * ## 为什么不用 usePagination
 *
 * `usePagination` 是**单页**语义：一个 page 指针 + total。连续加载需要的是
 * 「已累积的 N 页 + 还能不能继续」，两者不能互相替代。桌面大屏仍用页码
 * （`usePagination`），compact 与壳内用本模块 —— 这是**两个独立维度**：
 * 布局模式（cards/table）与加载方式（continuous/paged）互不影响。
 *
 * ## 核心不变量：revision 闸门
 *
 * 筛选/租户/排序变化时 `revision++`，**旧请求即使在之后到达也被拒绝写入**。
 * 没有这道闸门会出现：切到租户 B 后，A 的慢响应回来把 A 的行追加进 B 的列表
 * —— 跨租户数据泄露，且用户完全看不到任何异常。
 *
 * 每次请求都带 `{ revision, page }` 快照；`commit` 时若 `revision` 已变则丢弃。
 * 这是本模块唯一不能省的机制。
 *
 * ## 内存预算
 *
 * 累积到 `windowStart` 行开始记录告警，达到 `evictAbove` 行清退最早的页
 * （保留游标与 anchor，返回时补取）。设计值来自参考规范 07 §3，不是实测结论。
 */

import { computed, ref, type ComputedRef, type Ref } from 'vue'

/** 加载方式。与卡片/表格模式正交。 */
export type LoadMode = 'continuous' | 'paged'

export type ContinuousState =
  | 'idle'
  | 'loadingNext'
  | 'refreshing'
  | 'failed'
  | 'exhausted'
  | 'paused'

export interface HyperPageResult<T> {
  rows: T[]
  /** 服务端总条数（可选）。缺失时按「短页即无更多」推导。 */
  total?: number
}

/** 稳定行 id。用于去重与 anchor 定位。 */
export type RowKeyFn<T> = (row: T) => string | number

export interface HyperPagesOptions<T> {
  /** 取下一页。必须支持「同一页可重取」以便失败重试。 */
  fetchPage: (page: number) => Promise<HyperPageResult<T>>
  /** 每页条数。短页（返回条数 < pageSize）即视为没有更多。 */
  pageSize: number
  rowKey: RowKeyFn<T>
  /** 累积超过该行数开始清退最早的页。默认 3000（设计值）。 */
  evictAbove?: number
}

export interface HyperPages<T> {
  /** 已累积的行（已按 rowKey 去重）。 */
  rows: Ref<T[]>
  state: Ref<ContinuousState>
  /** 当前 revision。任何会改变查询条件的动作都会提升它。 */
  revision: Ref<number>
  /** 已加载页数。 */
  loadedPages: Ref<number>
  /** 是否还有下一页。 */
  hasMore: Ref<boolean>
  /** 已加载总行数（去重后）。 */
  loadedCount: ComputedRef<number>
  /**
   * 首次加载（无旧数据）。第 1 页已在途时**加入**那次请求（不重复发、也不作废它）。
   * @returns 是否成功落地第 1 页
   */
  loadFirst(): Promise<boolean>
  /**
   * 加载下一页。无更多 / busy / paused 时**直接返回 false**，不发请求；
   * 该页已在途时**加入**那次请求，与调用方共享同一结果。
   * @returns 是否真的发起了（或加入了）请求
   */
  loadNext(): Promise<boolean>
  /**
   * 刷新：提升 revision、丢弃已累积的行、回到第一页重取。
   * 失败时**保留旧行**（规范 13 §1 的 stale 态），不把列表清空后报错。
   */
  refresh(): Promise<boolean>
  /**
   * 查询条件变化（筛选 / 租户 / 排序 / 页大小）时调用：
   * 提升 revision 并让所有在途结果失效，但**不**自动请求，由调用方决定。
   */
  invalidate(): void
  /** 失败后显式重试。 */
  retry(): Promise<boolean>
  /** 断开自动加载（页面隐藏 / 容器不可见）。 */
  pause(): void
  resume(): void
  /** 恢复被清退的页游标，返回时补取用。 */
  evictedFromPage: Ref<number>
  /** 测试与登出用。 */
  _reset(): void
}

export function createHyperPages<T>(opts: HyperPagesOptions<T>): HyperPages<T> {
  const evictAbove = opts.evictAbove ?? 3000

  const rows = ref([]) as Ref<T[]>
  const state = ref<ContinuousState>('idle')
  const revision = ref(0)
  const loadedPages = ref(0)
  const hasMore = ref(true)
  const evictedFromPage = ref(1)

  /** 每页已成功落地的行数缓存。原位替换失败重试的数据靠它。 */
  const pageRows = new Map<number, T[]>()
  /**
   * 在途请求，用于单飞。key = page。
   * `token` 用来识别「我是不是这条记录的主人」——invalidate() 清过 map 之后，
   * 旧请求落地时按 key 无条件 delete 会把新请求的条目删掉。
   * `done` **永不 reject**：它就是对外承诺的 `Promise<boolean>`，重复调用方
   * 直接 await 它。直接存 task 的话，task 的 rejection 会变成 unhandled rejection。
   */
  const inflight = new Map<number, { token: number; done: Promise<boolean> }>()
  let tokenSeq = 0
  let paused = false

  const loadedCount = computed(() => rows.value.length)

  function resetInternal() {
    rows.value = []
    pageRows.clear()
    loadedPages.value = 0
    evictedFromPage.value = 1
    hasMore.value = true
  }

  /**
   * 重建累积行：按页序遍历**已落地的页**，按 rowKey 去重。
   *
   * 刻意遍历 `pageRows` 的键而不是 `evictedFromPage..loadedPages` 这个区间：
   * 区间上界 `loadedPages` 在 commit 内部才更新，用它做上界会形成时序耦合
   * （第 1 页提交时上界还是 0，合并结果恒为空）。键集合没有这个时序依赖。
   */
  function rebuild() {
    const seen = new Set<string | number>()
    const merged: T[] = []
    for (const page of [...pageRows.keys()].sort((a, b) => a - b)) {
      for (const row of pageRows.get(page) ?? []) {
        const k = opts.rowKey(row)
        if (seen.has(k)) continue
        seen.add(k)
        merged.push(row)
      }
    }
    rows.value = merged
  }

  /**
   * 提交一页。**revision 已变则整页丢弃** —— 这是跨租户数据泄露的主防线。
   * @returns 是否真的提交了
   */
  function commit(page: number, result: HyperPageResult<T>, rev: number): boolean {
    if (rev !== revision.value) return false

    // 第 1 页落地 = 新的累积基线，必须**先**清退旧的页缓存。
    // 顺序反了会把刚写入的第 1 页一起 clear 掉，rows 恒为空。
    if (page === 1) resetAccumulated()
    pageRows.set(page, result.rows)
    loadedPages.value = Math.max(loadedPages.value, page)
    rebuild()
    return true
  }

  function resetAccumulated() {
    pageRows.clear()
    loadedPages.value = 0
    evictedFromPage.value = 1
  }

  function evictIfNeeded() {
    if (rows.value.length <= evictAbove) return
    const pages = [...pageRows.keys()].sort((a, b) => a - b)
    // 至少保留一页，否则 `pages.length > 1` 会让最后一行也清掉
    while (rows.value.length > evictAbove && pages.length > 1) {
      const oldest = pages.shift()!
      pageRows.delete(oldest)
      evictedFromPage.value = oldest + 1
      // rebuild 会刷新 rows.value，循环条件才真的在推进（否则死循环）
      rebuild()
    }
  }

  function loadPage(page: number, rev: number, force = false): Promise<boolean> {
    // 单飞：同一页已有在途请求时**加入**它，而不是再发一次、也不是直接 false。
    // 加入才能让「重复调用」与「在途请求」共享同一个结果。
    if (!force) {
      const existing = inflight.get(page)
      if (existing) return existing.done
    }

    const token = ++tokenSeq
    const task = (async () => {
      if (state.value === 'idle' || state.value === 'failed' || state.value === 'exhausted') {
        state.value = page === 1 && loadedPages.value === 0 ? 'refreshing' : 'loadingNext'
      }
      try {
        const result = await opts.fetchPage(page)
        if (rev !== revision.value) return // 过期：静默丢弃，不改任何状态
        if (!commit(page, result, rev)) return

        evictIfNeeded()

        const short = result.rows.length < opts.pageSize
        hasMore.value = short ? false : result.total != null ? loadedPages.value * opts.pageSize < result.total : true
        state.value = hasMore.value ? 'idle' : 'exhausted'
      } catch (err) {
        if (rev !== revision.value) return
        // 失败**保留已加载行**（规范 13：stale 态），不把列表清空
        state.value = 'failed'
        throw err
      } finally {
        // 只清理自己那一条。invalidate() 会清空 map，之后同名页可能已经属于新请求。
        if (inflight.get(page)?.token === token) inflight.delete(page)
      }
    })()

    const done = task.then(
      () => true,
      () => false,
    )
    inflight.set(page, { token, done })
    return done
  }

  function loadFirst() {
    // 单飞判定**先于**提升 revision：重复的 loadFirst 不得把在途请求作废。
    // 反了的话，连续调三次 loadFirst 会让唯一那次请求的 revision 过期，
    // 结果是「只发一次请求，且一行都拿不到」。
    const existing = inflight.get(1)
    if (existing) return existing.done
    const rev = ++revision.value
    resetInternal()
    state.value = 'refreshing'
    return loadPage(1, rev)
  }

  function loadNext() {
    if (paused) return Promise.resolve(false)
    if (!hasMore.value) return Promise.resolve(false)
    if (state.value === 'loadingNext' || state.value === 'refreshing') return Promise.resolve(false)
    return loadPage(loadedPages.value + 1, revision.value)
  }

  function refresh() {
    // 刷新**不清空 rows** —— 保留旧内容直到新数据就绪（保旧刷新）。
    // 只有真正提交第 1 页时才替换（commit 里 page===1 会 resetAccumulated）。
    const rev = ++revision.value
    pageRows.clear()
    loadedPages.value = 0
    evictedFromPage.value = 1
    hasMore.value = true
    state.value = 'refreshing'
    // force：刷新必须发起**新**请求，不能加入上一次还在飞的第 1 页
    return loadPage(1, rev, true)
  }

  function retry() {
    if (state.value !== 'failed') return Promise.resolve(false)
    if (paused) return Promise.resolve(false)
    // 重取**失败的那一页**（loadedPages + 1），不跳页 —— 跳页会静默丢一整页数据
    const next = loadedPages.value === 0 ? 1 : loadedPages.value + 1
    return loadPage(next, revision.value)
  }

  function invalidate() {
    revision.value += 1
    // 清掉在途标记但**不**取消真实请求 —— 它回来时会被 revision 闸门丢弃。
    // 真要取消网络请求由 fetchPage 自行接 AbortSignal。
    inflight.clear()
    state.value = 'idle'
  }

  function pause() {
    paused = true
    if (state.value === 'idle') state.value = 'paused'
  }

  function resume() {
    paused = false
    if (state.value === 'paused') state.value = 'idle'
  }

  function _reset() {
    resetInternal()
    state.value = 'idle'
    revision.value = 0
    inflight.clear()
    paused = false
  }

  return {
    rows,
    state,
    revision,
    loadedPages,
    hasMore,
    loadedCount,
    evictedFromPage,
    loadFirst,
    loadNext,
    refresh,
    retry,
    invalidate,
    pause,
    resume,
    _reset,
  }
}

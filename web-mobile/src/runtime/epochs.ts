// epochs.ts — 三层正交代次（UI 规范 17 §4-R2）。
//   sessionEpoch  会话代次：登录/登出/换号递增；所有缓存与在途请求的根键。
//   queryRevision 查询代次：下拉刷新/筛选变更递增；同 session 内隔离新旧响应。
//   renderEpoch   渲染代次：NavigationEntry 级，异步标题解析携带，不匹配即丢弃。
// 校验关系：请求提交前检查 sessionEpoch；响应回写前检查 queryRevision；
// 标题/快照回写前检查 entryId + renderEpoch。三者缺省 0，只增不减。

export interface Epochs {
  session: number
  query: number
}

let sessionEpoch = 0

/** 会话代次（模块级单例）。登录成功/登出时调用 nextSessionEpoch。 */
export function nextSessionEpoch(): number {
  sessionEpoch += 1
  return sessionEpoch
}

export function currentSessionEpoch(): number {
  return sessionEpoch
}

/** 查询代次：每页一个计数器，由 useHyperPage/列表持有。 */
export function createQueryEpoch(): { current: number; next(): number } {
  let queryRevision = 0
  return {
    get current() {
      return queryRevision
    },
    next() {
      queryRevision += 1
      return queryRevision
    },
  }
}

/** 回写守卫：响应携带的代次若已落后于当前代次，禁止回写（旧响应不得覆盖新状态）。 */
export function isStale(claimedSession: number, claimedQuery: number, now: Epochs): boolean {
  return claimedSession !== now.session || claimedQuery !== now.query
}

/** 渲染代次守卫：异步标题解析结果回写前检查 entry 仍在渲染的这一代。 */
export function isRenderStale(
  claimedEntryId: string,
  claimedRenderEpoch: number,
  current: { entryId: string; renderEpoch: number },
): boolean {
  return claimedEntryId !== current.entryId || claimedRenderEpoch !== current.renderEpoch
}

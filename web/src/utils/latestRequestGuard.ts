// latestRequestGuard.ts —— 「只有最后一次发出的请求可以落地」的序号守卫（纯函数）。
//
// ★ 为什么要有这一层（2026-11-25 请求日志页实测）：
//
//   连续切换时间范围 / 筛选时，`load()` 没有任何过期判定 ⇒
//   ① 先发出的**慢**请求（旧范围、旧筛选）会在后发出的快请求**之后**才返回，
//      把 `rows / total / aggregate` 覆盖成旧范围的数据，或把它的超时错误写进
//      `error` —— 界面上就是「切到新的时间范围后列表没数据 / 冒出一条旧报错」；
//   ② 先完成的那个请求在 `finally` 里把 `loading` 关掉，而另一个还在途 ⇒
//      用户看到的是一个静止的、显示着旧数据的页面。
//
// ★ 通用式：**响应属于「发出它的那一刻的界面」，界面已经变了，响应就该作废。**
//   判据是一个单调递增的序号：发起时领号，落地前对号。对不上的（成功或失败）
//   一律丢弃，`finally` 里的收尾也一样。
//
// ★ 为什么放 utils 而不是留在视图里：视图组件（Vue/jsdom）测不动，而本模块是
//   纯函数 ⇒ 可以被 vitest 直接钉住（同族做法见 utils/requestLogsScanOutcome.ts）。

export interface LatestRequestGuard {
  /** 发起一次新请求，领一个序号（单调递增，永不重复）。 */
  next(): number
  /**
   * 该序号的请求是否仍是最新的一次。
   * 为 false ⇒ 它的响应 / 错误 / 收尾都必须被丢弃。
   */
  isCurrent(seq: number): boolean
}

export function createLatestRequestGuard(): LatestRequestGuard {
  let current = 0
  return {
    next() {
      current += 1
      return current
    },
    isCurrent(seq) {
      return seq === current
    },
  }
}

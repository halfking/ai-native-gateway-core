// maestro/tools/result.mjs —— 断言**判定口径的唯一真源**（两个断言工具共用）。
//
// ★ 为什么要有这个文件（2026-11-18 实测踩到两次）：
//   ① 「失败计数只数第一层」⇒ **第二层（浮层 / 紧凑层）的红被统计口径吃掉、照样报 PASS**
//      ——「断言红」被吃掉＝等于没有断言。设计层工具先踩过，行为层工具后踩过。
//   ② 两个工具各写一份判定 ⇒ 口径必然漂移（一个打印「PASS（N 条断言）」、另一个打印「PASS」）。
//   ⇒ **判定逻辑单点化**：口径只在这里写一次，两个工具都调它，
//      再用 selftest.mjs 把三种结局（绿 / 红 / 量具失效）钉死。
//
// 三种结局（★ 退出码必须能区分「红」与「没测」）：
//   0 = 全绿          ⇒ RESULT: PASS（N 条）
//   1 = 有断言失败    ⇒ RESULT: FAIL（k / N 条）      ← **产品问题**
//   2 = 量具失效      ⇒ RESULT: 量具失效（…这不是产品缺陷）← **没评估到，不是不合格**
//
// ★★ 两种「优先级」不是一回事（2026-11-25 审计轮实测撞出，写清楚免得又被当成漂移）：
//   ① **同一个工具内**（summarize）：**量具失效优先于红**
//      —— 量具失效往往意味着「这一轮压根没测到」（例：页面窗口内 0 条真实请求 ⇒
//      后续断言全部未评估）⇒ 此时那条「红」可能是**量具失效的假象**
//      ⇒ 报「量具失效」比报「产品缺陷」诚实 ⇒ 见下方 summarize。
//   ② **跨工具/跨层合并**（mergeExitCodes）：**红优先于量具失效**
//      —— 工具 A 的量具失效只影响 A 的结论，**不该掩盖工具 B 已经确证的缺陷**
//      ⇒ 一旦有任一层真的评估出红，就必须报红（红被吃掉＝等于没有断言
//      —— 这正是本文件头注记录的历史事故）。
//   ⇒ 两条都写在本文件里 + 用 selftest.mjs 钉死 ⇒ 判定口径仍只有这一处。

export const EXIT_OK = 0
export const EXIT_FAIL = 1
export const EXIT_GAUGE = 2

/**
 * 汇总两层（或多层）的检查，产出**唯一**的判定。
 * @param {Array<{name:string, pass:boolean, detail?:string}>} checks 全部检查
 * @param {{skipped?: Array<{name:string}|string>}} [opts]
 *        skipped = 因「量具失效」而**未被评估**的检查（没被评估 ≠ 被评估为失败）
 */
export function summarize(checks, opts = {}) {
  const skipped = opts.skipped || []
  const skipNames = new Set(skipped.map((c) => (typeof c === 'string' ? c : c.name)))
  const total = checks.length
  const failed = checks.filter((c) => !c.pass && !skipNames.has(c.name)).length
  const skippedCount = skipped.length

  if (skippedCount > 0) {
    return {
      failed,
      total,
      skipped: skippedCount,
      exitCode: EXIT_GAUGE,
      line: `RESULT: 量具失效（${skippedCount} 条依赖真实请求的断言**未评估**；页面窗口内 0 条真实请求 ⇒ 这不是产品缺陷）`,
    }
  }
  return {
    failed,
    total,
    skipped: 0,
    exitCode: failed === 0 ? EXIT_OK : EXIT_FAIL,
    line:
      failed === 0
        ? `RESULT: PASS（${total} 条）`
        : `RESULT: FAIL（${failed} / ${total} 条）`,
  }
}

/**
 * 合并**多个工具/多层**的退出码 ⇒ 唯一判定（跨工具口径）。
 *
 * ★ 优先级：**红 > 量具失效 > 绿**（与 summarize 的「工具内 gauge 优先」不同，
 *   原因见文件头注②）：
 *   · 任一层评估出红 ⇒ 报红（**红绝不能被另一层的量具失效吃掉**）；
 *   · 否则任一层量具失效 ⇒ 报量具失效；
 *   · 全绿 ⇒ 绿。
 * ★ 为什么必须在这里（而不是在 runner 的 bash if 链里）：
 *   runner 若自己写 if 链 ⇒ 就是**第三份判定口径** ⇒ 必然漂移
 *   （本文件存在的理由就是消灭这件事）。且实测真的漂了：
 *   2026-11-25 审计轮发现某 runner 的 if 链按顺序先判「探针量具失效」，
 *   于是「探针量具失效 + 补充层红」被报成量具失效 ⇒ **红被吃掉**。
 *   ⇒ 合并只在这里写一次，runner 调它，selftest.mjs 钉它。
 *
 * @param {number[]} codes 各层退出码（0 绿 / 1 红 / 2 量具失效）
 * @returns {number} 合并后的退出码
 */
export function mergeExitCodes(codes) {
  const arr = (codes || []).map(Number).filter((n) => Number.isFinite(n))
  if (arr.includes(EXIT_FAIL)) return EXIT_FAIL
  if (arr.includes(EXIT_GAUGE)) return EXIT_GAUGE
  return EXIT_OK
}

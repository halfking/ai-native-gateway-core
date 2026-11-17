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

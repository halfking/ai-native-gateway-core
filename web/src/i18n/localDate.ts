// 本地日期戳 —— ★「今天是哪天」必须按**本地时区**判，不能按 UTC。
//
// ★ 为什么单独成模块（2026-11-03 第二十八轮）：
//   本仓的 i18n 两把尺子（localeValueQuality / hardcodedCjk）此前都用
//   `new Date().toISOString().slice(0, 10)` 取「更新日期」
//   ⇒ ⇒ **那是 UTC 日期** ⇒ 在 UTC+8 的本地时间 00:00–08:00 之间跑一次
//   `--update-baseline`，写进去的日期**会比本地日期早一天**
//   ⇒ ⇒ 实证：2026-11-03 本地凌晨跑 i18n 降额，写进去的 `updated` 是
//   `2026-11-02` ⇒ ⇒ **基线文件自己说它属于昨天**。
//
// ★ 与并行会话 `2c638eafe` 的关系：他们在运行时尺
//   （design-metric-skeleton-runtime-check.mjs）上修了同一类缺陷
//   （「本地日期（非 toISOString 的 UTC）」）⇒ ⇒ ★ **同一类缺陷在 i18n 这两把尺子上
//   还留着** ⇒ ⇒ 本模块把那个修法**提炼成一处**，两把尺子共用。
//
// ★ 为什么不能用 toISOString 取「日」：toISOString 是 **UTC 瞬时**的 ISO 串，
//   它的日期部分是**UTC 的日**，与运行机器的**本地日**在 UTC+8 的每天前 8 小时
//   相差一天 ⇒ ⇒ 「本地凌晨写的基线自称昨天」⇒ ⇒ 基线的日期字段失去意义。

/**
 * 返回**本地时区**的日期戳 `YYYY-MM-DD`。
 *
 * @param d 取哪个瞬间（默认现在）；测试用它构造边界时刻。
 */
export function localDateStamp(d: Date = new Date()): string {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${day}`
}

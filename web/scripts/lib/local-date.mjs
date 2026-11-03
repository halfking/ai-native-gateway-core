// local-date.mjs —— 本地时区的日期戳 `YYYY-MM-DD`（★ **不是** UTC）。
//
// ★ 为什么需要它（2026-11-03 12h 审计 D2/D3）：
//   本仓多把运行时尺的基线写回用 `new Date().toISOString().slice(0, 10)`
//   取「更新日期」⇒ ⇒ **那是 UTC 日期** ⇒ 在 UTC+8 的本地时间 00:00–08:00
//   之间跑一次 `--update-baseline`，写进去的日期**会比本地日期早一天**
//   ⇒ ⇒ 实证：2026-11-03 本地凌晨写进去的 `updated` 是 `2026-11-02`
//   ⇒ ⇒ **基线文件自己说它属于昨天** ⇒ ⇒ 基线的日期字段失去意义。
//
// ★ 与 `web/src/i18n/localDate.ts` 的关系：那是 i18n 两把尺子的同款修法
//   （★ 本仓的 i18n 尺与运行时尺是**两族**，且运行时尺跑在**纯 node** 下
//   ⇒ ⇒ 不引 TS 模块 ⇒ ⇒ 这里抽一份 .mjs 给运行时尺共用）。
//   ⇒ ★ 两处的实现必须**逐字同款**（判据：本地时区的年/月/日）。
//
// ★ 为什么不能用 toISOString 取「日」：toISOString 是 **UTC 瞬时**的 ISO 串，
//   它的日期部分是**UTC 的日**，与运行机器的**本地日**在 UTC+8 的每天前 8 小时
//   相差一天。

/**
 * 返回**本地时区**的日期戳 `YYYY-MM-DD`。
 *
 * @param {Date} [d] 取哪个瞬间（默认现在）。
 * @returns {string} `YYYY-MM-DD`
 */
export function localDateStamp(d = new Date()) {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${day}`
}

// requestLogsWindow.ts —— 请求日志页「页头时间范围选择器 ⇒ 实际查询窗口」的换算（纯函数）。
//
// ★ 为什么要有这一层（2026-11-25 request-logs 根修轮，Maestro 第二层探针实测撞出）：
//
//   页头的时间范围选择器（KxDateRangePicker）显示的窗口，与页面真正发给
//   /api/logs 的 from/to 此前有四处对不上——数据没错，但「选的」和「查的」不是同一扇窗：
//
//     ① 默认档「近 24 小时」的显示写成了 **24 天**：rollingWindowMinute(daysBack)
//        按天算，调用方却传了小时数（h24 传 24 ⇒ 24×24h）⇒ 触发器上显示
//        「自定义 ｜ 11/01 07:51 – 11/25 07:51」（实测），而查询是 24h。
//     ② 「近 7 天」被映射到旧预设 'd7'（滚动 7×24h）⇒ 查询窗比选择器显示的
//        日历窗（6 天前 00:00 – 今天 23:59）早开始若干小时 ⇒ 列表里出现
//        **显示范围之外**的行。
//     ③ 「本月」的查询结束于「此刻」，显示却是「~ 今天 23:59」。
//     ④ 走自定义路径的档位（昨天 / 近 14 / 30 天 / 上月 …）结束于 23:59:00.000
//        ——选择器用 'HH:mm = 23:59' 表示「到当天结束」（分钟精度写不出
//        23:59:59.999），页面原样截断 ⇒ **最后 59.999 秒的请求被漏掉**。
//
//   ⇒ 统一口径：**查询窗口 ≡ 选择器显示的窗口**。换算集中在本文件（纯函数），
//     便于 vitest 直接钉住每个档位的边界；视图只负责调用。
import type { KxDateRange } from '../components/ui/kx-date-types'
import { parseLocalMinute } from './datetime'

/** 页面内部的时间预设状态机取值（与 RequestLogsView.vue 的 TimePreset 同集）。 */
export type LogsTimePreset =
  | 'h1'
  | 'h6'
  | 'h24'
  | 'd3'
  | 'd7'
  | 'today'
  | 'thisWeek'
  | 'thisMonth'
  | 'thisYear'
  | 'custom'

/**
 * headerPresetById：页头选择器的档位 id → 页面内部 TimePreset。
 *
 * ★ 只有「TimePreset 自己的窗口 ≡ 选择器显示的窗口」的档位才映射过去；
 *   不在表里的档位（昨天 / 近 7 天 / 近 14 / 30 天 / 上月 …）一律走 custom，
 *   **原样使用选择器给出的窗口**（经 pickerRangeToQueryWindow 补齐日界）。
 *
 * ★ 为什么 last7d 不在这里（2026-11-25 订正）：此前它映射到 'd7'（滚动 7×24h，
 *   同时也是 ?preset=d7 / ?hours=168 深链的语义）——与选择器显示的日历窗不一致
 *   （见头注②）。深链语义保持不变；页头点「近 7 天」改走日历窗。
 */
export const headerPresetById: Record<string, LogsTimePreset> = {
  today: 'today',
  last24h: 'h24',
  thisMonth: 'thisMonth',
}

const pad = (n: number) => String(n).padStart(2, '0')

/** 'YYYY-MM-DD HH:mm'（浏览器本地时区）。 */
function localMinute(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/**
 * 滚动小时窗（h1 / h6 / h24）：[now − hours 小时, now]，分钟精度、浏览器本地时区。
 * ★ 参数是**小时**（修前的 rollingWindowMinute 参数是「天」，却被传了小时数 ⇒ 头注①）。
 */
export function rollingHoursWindow(now: Date, hours: number): KxDateRange {
  const start = new Date(now.getTime() - hours * 3600 * 1000)
  return { start: localMinute(start), end: localMinute(now) }
}

/** 选择器用 'HH:mm = 23:59' 表示「到当天结束」。 */
export function isEndOfDayMarker(value: string): boolean {
  return /(?:T| )23:59(?::00)?$/.test(value.trim())
}

/**
 * 页头选择器给出的范围 ⇒ 查询窗口（浏览器本地时区解释，与选择器的显示同口径）。
 * 结束时刻是日界标记（23:59）⇒ 补到 23:59:59.999（头注④）。
 */
export function pickerRangeToQueryWindow(range: KxDateRange): { start: Date; end: Date } {
  const start = parseLocalMinute(range.start)
  const end = parseLocalMinute(range.end)
  if (isEndOfDayMarker(range.end)) end.setHours(23, 59, 59, 999)
  return { start, end }
}

/** 当天结束（浏览器本地时区）。 */
export function endOfDay(d: Date): Date {
  const e = new Date(d)
  e.setHours(23, 59, 59, 999)
  return e
}

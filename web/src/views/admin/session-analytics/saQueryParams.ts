// saQueryParams.ts —— 会话分析族（/admin/session-analytics/*）的 URL 深链参数读取
// （2026-11-23 审计轮 P1-3 / P2-5）。
//
// ★ 背景（实测）：Top5 排行卡片点击跳转时带 query：
//   · SaTop5Rankings.openTask  → /admin/session-analytics/users?task=…&start_date=…&end_date=…
//   · SaTop5Rankings.openUser  → /admin/session-analytics/user-ranking/<owner>?start_date=…&end_date=…
//   而落地页**此前一个都不读**（三个排行页只写死 rangeFromSpanDays(7)；
//   users 页连 useRoute 都没有）⇒ 点过去「什么也没发生」（静默失效）。
//
// ★ 为什么抽成纯函数模块：一族四个页面要读**同一套** query 口径，
//   页面组件（Vue/jsdom）测不动路由 query ⇒ 纯函数可被 vitest 直接钉住
//   （同族做法见 utils/usageTrendDeepLink.ts）。
//
// ★ 口径纪律：
//   · start_date / end_date 必须**成对且合法**（后端 isValidDate + start<end，
//     见 admin/session_analytics/handlers.go:634-650）⇒ 半个 / 非法 ⇒ 当作没带，
//     回退调用方的默认窗口（而不是把非法值发给后端换 400）。
//   · task 是**普通字符串**（Top5 任务行的任务名）；数组形态（?task=a&task=b）
//     不认（后端没有「多任务」语义）⇒ 视为没带。

export interface SaDateRange {
  start: string
  end: string
}

/** YYYY-MM-DD（与后端 isValidDate 同形）。 */
const DATE_RE = /^\d{4}-\d{2}-\d{2}$/

/** query 值取字符串（只认单个字符串；数组 / 空串 ⇒ ''）。 */
export function readQueryString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

/**
 * URL 上的 start_date / end_date ⇒ 日期区间。
 * 缺省 / 非法 / 顺序颠倒 ⇒ null（调用方回退自己的默认窗口）。
 */
export function rangeFromQuery(query: Record<string, unknown> | null | undefined): SaDateRange | null {
  if (!query) return null
  const start = readQueryString(query.start_date)
  const end = readQueryString(query.end_date)
  if (!start || !end) return null
  if (!DATE_RE.test(start) || !DATE_RE.test(end)) return null
  if (start > end) return null
  return { start, end }
}

export function exactRangeFromQuery(query: Record<string, unknown>): SaDateRange | undefined {
  const start = readQueryString(query.start_time)
  const end = readQueryString(query.end_time)
  if (!start.includes('T') || !end.includes('T') || !(Date.parse(end) > Date.parse(start))) return
  return { start, end }
}

/** URL 上的 ?task= ⇒ 任务过滤值（空 ⇒ 不过滤）。 */
export function taskFilterFromQuery(query: Record<string, unknown> | null | undefined): string {
  if (!query) return ''
  return readQueryString(query.task)
}

/**
 * datetime.ts — 全站统一的时间格式化工具（审计 R3 #10 UI 统一批次）。
 *
 * 背景：2026-09-09 审计确认 ≥10 处视图各自实现 formatTime/formatDate/
 * formatTimestamp，空值哨兵（'—' / '-'）、无效值兜底（原样返回 /
 * 'Invalid Date'）、locale 传参各不相同。本模块收敛为单一实现：
 *
 *  - 绝对时间主格式 = `new Date(v).toLocaleString([locale], options)`，
 *    这是仓内使用最广的写法（65 处裸调用 + 47 处带 locale），本模块不发明
 *    新格式，只是把空值/无效值兜底与 locale 传递标准化。
 *  - 相对时间 = formatRelativeTime（原 views/ops/regionTopology.ts 的
 *    实现泛化，新增 `older` 回调支持「超过 N 天回退绝对时间」的形态）。
 *  - 紧凑时间 = formatCompactDateTime（原 CredentialMonitorView 的
 *    `MM-DD HH:mm[:ss]` 手拼格式，图表轴 / 窄空间场景）。
 *  - 排序友好格式 = formatDateTimeIso（原 CredentialHeatmapView
 *    formatFullTime 的 `YYYY-MM-DD HH:mm` 本地时间格式，tooltip 用）。
 *
 * 所有函数对 null / undefined / 空串返回 `empty`（默认 '—'，仓内主流哨兵）；
 * 对不可解析的字符串原样返回（与 SystemMonitorPanel / RequestJourneyQueues
 * / ProbeHealthDetailView 的既有行为一致），对不可解析的数字/Date 返回 `empty`。
 */

export type DateTimeInput = string | number | Date | null | undefined

export interface FormatDateTimeOptions {
  /** BCP-47 locale；缺省 = 浏览器默认（与仓内裸 toLocaleString() 一致）。 */
  locale?: string
  /** 空值哨兵，默认 '—'。 */
  empty?: string
  /** 透传给 Intl.DateTimeFormat 的选项（月/日/时分秒裁剪等）。 */
  options?: Intl.DateTimeFormatOptions
}

function toDate(value: DateTimeInput): Date | null {
  if (value == null) return null
  if (value instanceof Date) return Number.isNaN(value.getTime()) ? null : value
  if (typeof value === 'string' && value.trim() === '') return null
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? null : d
}

function formatWith(
  d: Date,
  method: 'toLocaleString' | 'toLocaleTimeString',
  opts: FormatDateTimeOptions | undefined,
): string {
  const args: [string | undefined, Intl.DateTimeFormatOptions] | [string | undefined] = opts?.options
    ? [opts.locale, opts.options]
    : [opts?.locale]
  return d[method](...(args as Parameters<Date['toLocaleString']>))
}

/** 绝对时间（日期 + 时分秒），仓内主格式。 */
export function formatDateTime(value: DateTimeInput, opts?: FormatDateTimeOptions): string {
  const d = toDate(value)
  if (!d) {
    // 不可解析的字符串原样返回，便于排查脏数据（与既有实现一致）。
    return typeof value === 'string' && value.trim() !== '' ? value : opts?.empty ?? '—'
  }
  return formatWith(d, 'toLocaleString', opts)
}

/** 仅时间（时分秒），列表/队列行内场景。 */
export function formatTimeOnly(value: DateTimeInput, opts?: FormatDateTimeOptions): string {
  const d = toDate(value)
  if (!d) {
    return typeof value === 'string' && value.trim() !== '' ? value : opts?.empty ?? '—'
  }
  return formatWith(d, 'toLocaleTimeString', opts)
}

export interface CompactDateTimeOptions {
  /** true → `MM-DD HH:mm:ss`，false（默认）→ `MM-DD HH:mm`。 */
  withSeconds?: boolean
  empty?: string
}

/**
 * 紧凑本地时间 `MM-DD HH:mm[:ss]`（原 CredentialMonitorView 手拼格式）。
 * 时区无关的确定性输出，适合窄空间标签与刷新时间戳。
 */
export function formatCompactDateTime(
  value: DateTimeInput,
  opts?: CompactDateTimeOptions,
): string {
  const d = toDate(value)
  if (!d) return opts?.empty ?? '—'
  const pad = (n: number) => String(n).padStart(2, '0')
  const base = `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
  return opts?.withSeconds ? `${base}:${pad(d.getSeconds())}` : base
}

/**
 * 排序友好的本地时间 `YYYY-MM-DD HH:mm`（原 CredentialHeatmapView
 * formatFullTime 格式，tooltip / 导出用）。
 */
export function formatDateTimeIso(value: DateTimeInput, opts?: { empty?: string }): string {
  const d = toDate(value)
  if (!d) return opts?.empty ?? '—'
  const pad = (n: number) => String(n).padStart(2, '0')
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}`
  )
}

export interface RelativeTimeLabels {
  justNow: string
  minutesAgo: (n: number) => string
  hoursAgo: (n: number) => string
  daysAgo: (n: number) => string
  /**
   * 超过 `olderAfterDays`（默认 7，与 ApprovalListView 既有行为一致）天的
   * 回退渲染。缺省时不回退、继续用 daysAgo（与 regionTopology 行为一致）。
   */
  older?: (d: Date) => string
  olderAfterDays?: number
}

/**
 * 相对时间。泛化自 views/ops/regionTopology.ts（其实现与测试是本函数的
 * 行为基准）：epoch / 远未来 / 超过一年的脏时间戳一律钳制。
 */
export function formatRelativeTime(
  value: DateTimeInput,
  labels: RelativeTimeLabels,
): string {
  const d = toDate(value)
  if (!d) return ''
  const diff = Date.now() - d.getTime()
  // Guard against epoch / far-future / multi-year garbage (e.g. "739816d ago")
  if (diff < -60_000 || diff > 366 * 24 * 3600_000) {
    return labels.older ? labels.older(d) : ''
  }
  if (diff < 60_000) return labels.justNow
  const mins = Math.round(diff / 60_000)
  if (mins < 60) return labels.minutesAgo(mins)
  const hours = Math.round(mins / 60)
  if (hours < 24) return labels.hoursAgo(hours)
  const days = Math.round(hours / 24)
  const olderAfter = labels.olderAfterDays ?? 7
  if (labels.older && days > olderAfter) return labels.older(d)
  return labels.daysAgo(days)
}

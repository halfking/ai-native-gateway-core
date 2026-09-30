// kxDatePresets.ts — KxDateRangePicker 默认预设工厂（2026-09-30 统一日历组件轮）。
// 口径对齐参考图 2：今天 / 昨天 / 近24小时 / 近7天 / 近14天 / 近30天 / 本月 / 上月。
//
// 时区口径：与旧 BoardPeriodSelector 一致，按 UTC 日切（后端 board/usage 接口
// 将 start/end 视为 UTC 日）；datetime 精度的「近24小时」按当前时刻回退 24h。
import type { KxDatePrecision, KxDateRange, KxDateRangePreset } from './kx-date-types'

const DAY_MS = 86_400_000

function utcDayStart(ms: number = Date.now()): number {
  const d = new Date(ms)
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate())
}

function utcDateStr(ms: number): string {
  return new Date(ms).toISOString().slice(0, 10)
}

function utcMinuteStr(ms: number): string {
  return new Date(ms).toISOString().slice(0, 16).replace('T', ' ')
}

function resolveSpan(daysBack: number): KxDateRange {
  const today = utcDayStart()
  return {
    start: utcDateStr(today - daysBack * DAY_MS),
    end: utcDateStr(today),
  }
}

/** 含首尾的滚动窗口：days=1 为今天，days=7 为近 7 天。供「只接受 days 参数」的页面回填触发器。 */
export function rangeFromSpanDays(days: number): KxDateRange {
  const n = Math.max(1, Math.round(days))
  return resolveSpan(n - 1)
}

/** 天数档位预设。id 稳定为 span{N}d，resolve 与 rangeFromSpanDays 同口径。 */
export function spanDaysPreset(days: number, labelKey: string): KxDateRangePreset {
  const n = Math.max(1, Math.round(days))
  return {
    id: `span${n}d`,
    labelKey,
    resolve: () => rangeFromSpanDays(n),
  }
}

/**
 * 构建预设列表。
 * @param precision date 精度下「近24小时」退化为 昨天→今天 两日窗；datetime 精度输出分钟级时刻。
 * @param only 可选裁剪：只保留指定 id（保持默认顺序）。
 */
export function makeDateRangePresets(precision: KxDatePrecision, only?: string[]): KxDateRangePreset[] {
  const all: KxDateRangePreset[] = [
    {
      id: 'today',
      labelKey: 'common.dateRange.preset.today',
      resolve: () => {
        const today = utcDayStart()
        return { start: utcDateStr(today), end: utcDateStr(today) }
      },
    },
    {
      id: 'yesterday',
      labelKey: 'common.dateRange.preset.yesterday',
      resolve: () => {
        const y = utcDayStart() - DAY_MS
        return { start: utcDateStr(y), end: utcDateStr(y) }
      },
    },
    {
      id: 'last24h',
      labelKey: 'common.dateRange.preset.last24h',
      resolve: () => {
        if (precision === 'datetime') {
          const now = Date.now()
          return { start: utcMinuteStr(now - DAY_MS), end: utcMinuteStr(now) }
        }
        return resolveSpan(1)
      },
    },
    {
      id: 'last7d',
      labelKey: 'common.dateRange.preset.last7d',
      resolve: () => resolveSpan(6),
    },
    {
      id: 'last14d',
      labelKey: 'common.dateRange.preset.last14d',
      resolve: () => resolveSpan(13),
    },
    {
      id: 'last30d',
      labelKey: 'common.dateRange.preset.last30d',
      resolve: () => resolveSpan(29),
    },
    {
      id: 'thisMonth',
      labelKey: 'common.dateRange.preset.thisMonth',
      resolve: () => {
        const now = new Date()
        const first = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1)
        return { start: utcDateStr(first), end: utcDateStr(utcDayStart()) }
      },
    },
    {
      id: 'lastMonth',
      labelKey: 'common.dateRange.preset.lastMonth',
      resolve: () => {
        const now = new Date()
        const first = Date.UTC(now.getUTCFullYear(), now.getUTCMonth() - 1, 1)
        const last = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 0)
        return { start: utcDateStr(first), end: utcDateStr(last) }
      },
    },
  ]
  const filtered = only?.length
    ? all.filter((p) => only.includes(p.id))
    : all.filter((p) => p.id !== 'last24h' || precision === 'datetime')
  return filtered
}

/** 短显示：2026-09-24 → 09/24；2026-09-24 08:30 → 09/24 08:30。 */
export function shortRangeLabel(range: KxDateRange): string {
  const short = (v: string) => {
    const [d, t] = v.split(' ')
    const mmdd = (d ?? '').slice(5).replace('-', '/')
    return t ? `${mmdd} ${t.slice(0, 5)}` : mmdd
  }
  return `${short(range.start)} – ${short(range.end)}`
}

/**
 * 范围天数（2026-09-30 审计 P3-1：按精度区分口径）。
 * - date 精度：含首尾日的日历天数（同日=1，7 天窗=7）= floor(diff/DAY)+1。
 * - datetime 精度：时刻差向上取整 = ceil(diff/DAY)（72h=3 天，超界 1 分钟即 4 天），
 *   与旧后端「允许恰好 72h」的钳制口径一致（RequestLogsView max-span-days=3）。
 * 分钟级串长度为 16，无秒不属规范格式，补 ':00' 后按 UTC 解析。
 */
export function rangeSpanDays(range: KxDateRange, precision: KxDatePrecision = 'date'): number {
  const parse = (v: string) => Date.parse(v.length === 10 ? `${v}T00:00:00Z` : `${v.replace(' ', 'T')}:00Z`)
  const start = parse(range.start)
  const end = parse(range.end)
  if (Number.isNaN(start) || Number.isNaN(end)) return 0
  const diff = (end - start) / DAY_MS
  return precision === 'datetime' ? Math.ceil(diff) : Math.floor(diff) + 1
}

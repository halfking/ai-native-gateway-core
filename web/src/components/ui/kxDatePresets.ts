// kxDatePresets.ts — KxDateRangePicker 默认预设工厂（2026-09-30 统一日历组件轮）。
// 口径对齐参考图 2：今天 / 昨天 / 近24小时 / 近7天 / 近14天 / 近30天 / 本月 / 上月。
//
// 时区口径：★ 2026-11-20 时区设置轮——「今天/昨天/本月/上月」的日切从 UTC
// 改为**系统时区**（settings.system.timezone，默认 Asia/Shanghai；后端
// resolveUsageTimeRange / boardPresetTimeRange 同口径），日期串即系统时区的
// 日历日。datetime 精度的「近24小时」按当前时刻回退 24h（与日切无关）。
import { partsInSystemTz } from '@/utils/datetime'
import { systemTimezone } from '@/utils/systemTimezone'
import type { KxDatePrecision, KxDateRange, KxDateRangePreset } from './kx-date-types'

const DAY_MS = 86_400_000

/** 系统时区「当天」零点的毫秒时刻：日期串（partsInSystemTz）→ 正午 → -12h。
 *  正午永远落在日历日内，无 DST/取模方向歧义（取模公式在正偏移时区会截到 UTC 日界）。 */
export function systemTzDayStartMs(ms: number = Date.now()): number {
  return systemTzNoonMs(systemTzDateStr(ms)) - 12 * 3600_000
}

/**
 * 把系统时区日期串转 UTC 毫秒：返回「系统时区挂钟 = 该日 12:00」的瞬间
 * （正午永远落在日历日内，无 DST 歧义）。供零点推导与跨日窗口用。
 */
export function systemTzNoonMs(dateStr: string): number {
  const [y, m, d] = dateStr.split('-').map((x) => Number(x))
  if (!y || !m || !d) return Number.NaN
  const targetDayUtc = Date.UTC(y, m - 1, d)
  // ① 先把日历日迭代到目标（从 UTC 正午出发，正午附近不会跨日）。
  let ms = targetDayUtc + 12 * 3600_000
  for (let i = 0; i < 3; i++) {
    const cur = partsInSystemTz(new Date(ms))
    const dayDiff = (targetDayUtc - Date.UTC(cur.year, cur.month - 1, cur.day)) / DAY_MS
    if (dayDiff === 0) break
    ms += dayDiff * DAY_MS
  }
  // ② 再把时刻校正到挂钟 12:00：offset = 挂钟字段按 UTC 读 - 真实瞬间。
  const p = partsInSystemTz(new Date(ms))
  const wallAsUtc = Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second)
  return targetDayUtc + 12 * 3600_000 - (wallAsUtc - ms)
}

/** 系统时区 YYYY-MM-DD 日期串。 */
export function systemTzDateStr(ms: number = Date.now()): string {
  const p = partsInSystemTz(new Date(ms))
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${p.year}-${pad(p.month)}-${pad(p.day)}`
}

/** 系统时区 YYYY-MM-DD HH:mm 分钟串。 */
export function systemTzMinuteStr(ms: number = Date.now()): string {
  const p = partsInSystemTz(new Date(ms))
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${p.year}-${pad(p.month)}-${pad(p.day)} ${pad(p.hour)}:${pad(p.minute)}`
}

function resolveSpan(daysBack: number): KxDateRange {
  const today = systemTzDateStr()
  const startMs = systemTzDayStartMs() - daysBack * DAY_MS
  return {
    start: systemTzDateStr(startMs),
    end: today,
  }
}

/** 含首尾的滚动窗口：days=1 为今天，days=7 为近 7 天。供「只接受 days 参数」的页面回填触发器。 */
export function rangeFromSpanDays(days: number): KxDateRange {
  const n = Math.max(1, Math.round(days))
  return resolveSpan(n - 1)
}

/**
 * datetime 精度的日界窗口：start=首日 00:00，end=今天 23:59（系统时区日切）。
 * 2026-11-19 时间范围统一轮（owner 口径「近 N 天最晚到今天结束」）：
 * 预设不再用「此刻回退」的滚动时刻，统一日界窗口；仅 last24h 保留滚动。
 * ⚠️ 23:59 不能用 endMs + 59min - 1s 推算（输入毫秒非零会算出 00:58），
 * 直接以字符串拼接日期前缀（时区轮后按系统时区日期串拼接）。
 */
export function dayWindowMinute(daysBack: number): KxDateRange {
  const startMs = systemTzDayStartMs() - daysBack * DAY_MS
  return {
    start: systemTzMinuteStr(startMs).slice(0, 11) + '00:00',
    end: systemTzMinuteStr(Date.now()).slice(0, 11) + '23:59',
  }
}

/** datetime 精度的单日窗口（00:00–23:59），供「昨天」这类单日预设。 */
function singleDayMinute(ms: number): KxDateRange {
  const prefix = systemTzMinuteStr(ms).slice(0, 11)
  return { start: prefix + '00:00', end: prefix + '23:59' }
}

/** datetime 精度的跨日窗口（首日 00:00 – 末日 23:59），供「上月」这类自然月预设。 */
function singleDayMinuteSpan(firstDateStr: string, lastDateStr: string): KxDateRange {
  return {
    start: systemTzMinuteStr(systemTzNoonMs(firstDateStr)).slice(0, 11) + '00:00',
    end: systemTzMinuteStr(systemTzNoonMs(lastDateStr)).slice(0, 11) + '23:59',
  }
}

/**
 * 天数档位预设。id 稳定为 span{N}d。
 * precision='datetime' 时输出日界窗口（start=首日 00:00，end=今天 23:59），
 * 与 makeDateRangePresets 的近 N 天预设同口径（2026-11-19 时间范围统一轮）。
 */
export function spanDaysPreset(days: number, labelKey: string, precision: KxDatePrecision = 'date'): KxDateRangePreset {
  const n = Math.max(1, Math.round(days))
  return {
    id: `span${n}d`,
    labelKey,
    resolve: () => (precision === 'datetime' ? dayWindowMinute(n - 1) : rangeFromSpanDays(n)),
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
      resolve: () => (precision === 'datetime'
        ? dayWindowMinute(0)
        : { start: systemTzDateStr(), end: systemTzDateStr() }),
    },
    {
      id: 'yesterday',
      labelKey: 'common.dateRange.preset.yesterday',
      resolve: () => {
        if (precision === 'datetime') return singleDayMinute(systemTzDayStartMs() - DAY_MS)
        const y = systemTzDateStr(systemTzDayStartMs() - DAY_MS)
        return { start: y, end: y }
      },
    },
    {
      id: 'last24h',
      labelKey: 'common.dateRange.preset.last24h',
      resolve: () => {
        if (precision === 'datetime') {
          const now = Date.now()
          return { start: systemTzMinuteStr(now - DAY_MS), end: systemTzMinuteStr(now) }
        }
        return resolveSpan(1)
      },
    },
    {
      id: 'last7d',
      labelKey: 'common.dateRange.preset.last7d',
      resolve: () => (precision === 'datetime' ? dayWindowMinute(6) : resolveSpan(6)),
    },
    {
      id: 'last14d',
      labelKey: 'common.dateRange.preset.last14d',
      resolve: () => (precision === 'datetime' ? dayWindowMinute(13) : resolveSpan(13)),
    },
    {
      id: 'last30d',
      labelKey: 'common.dateRange.preset.last30d',
      resolve: () => (precision === 'datetime' ? dayWindowMinute(29) : resolveSpan(29)),
    },
    {
      id: 'thisMonth',
      labelKey: 'common.dateRange.preset.thisMonth',
      resolve: () => {
        // 系统时区的本月 1 日：取今天的系统日历字段，把 day 换成 1。
        const todayStr = systemTzDateStr()
        const first = `${todayStr.slice(0, 7)}-01`
        if (precision === 'datetime') {
          return { start: first + ' 00:00', end: systemTzMinuteStr(Date.now()).slice(0, 11) + '23:59' }
        }
        return { start: first, end: todayStr }
      },
    },
    {
      id: 'lastMonth',
      labelKey: 'common.dateRange.preset.lastMonth',
      resolve: () => {
        // 系统时区的上月：从本月 1 日的正午回退一天到上月最后一天，
        // 再取其年月拼 01 为上月第一天。
        const firstThis = systemTzNoonMs(`${systemTzDateStr().slice(0, 7)}-01`)
        const lastPrev = systemTzDateStr(firstThis - DAY_MS)
        const firstPrev = `${lastPrev.slice(0, 7)}-01`
        if (precision === 'datetime') return singleDayMinuteSpan(firstPrev, lastPrev)
        return { start: firstPrev, end: lastPrev }
      },
    },
  ]
  const filtered = only?.length
    ? all.filter((p) => only.includes(p.id))
    : all.filter((p) => p.id !== 'last24h' || precision === 'datetime')
  return filtered
}

/** Local civil day of a calendar cell, matching date value-format YYYY-MM-DD. */
export function calendarDayKey(cell: Date): string {
  const month = String(cell.getMonth() + 1).padStart(2, '0')
  const day = String(cell.getDate()).padStart(2, '0')
  return `${cell.getFullYear()}-${month}-${day}`
}

/** True when the value's calendar day is strictly after notAfter (YYYY-MM-DD). */
export function isAfterDay(value: string, notAfter: string): boolean {
  return value.slice(0, 10) > notAfter
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

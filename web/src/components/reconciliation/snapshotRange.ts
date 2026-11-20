// snapshotRange.ts — reconciliation 的时间预设（2026-11-20 统一轮：改用站内标准预设）。
//
// ★ 为什么改：旧实现自带一套「止于昨日」的预设（昨天/近7d/近30d/本月/上月，全部
//   以 UTC 昨日收尾，并把 notAfter 钉在昨日）——理由是「当日快照凌晨才生成」。
//   但全站统一组件的标准预设（makeDateRangePresets）按 owner 口径
//   「近 N 天最晚到今天结束」（kxDatePresets.ts 2026-11-19 注释），
//   两套形状/日期口径不一致 ⇒ owner 图批「非标准的时间选择组件，并且
//   近7天的日期也不太对」。
//
// ★ 今天的快照确实还没生成：后端 reportRange 只校验 start≤end 且 ≤366 天，
//   区间含今天时今日无快照由覆盖行披露（useReconciliationPage 的
//   todayPendingHint），不再用「把今天从预设里抠掉」来掩盖。
import type { KxDateRange, KxDateRangePreset } from '../ui/kx-date-types'
import { makeDateRangePresets, rangeFromSpanDays, systemTzDateStr } from '../ui/kxDatePresets'

/** 预设 = 站内标准 date 精度预设（今天/昨天/近7天/近14天/近30天/本月/上月）。 */
export function snapshotDatePresets(): KxDateRangePreset[] {
  return makeDateRangePresets('date')
}

/** 初始区间：近 7 天（含今天，与全站口径一致）。 */
export function defaultSnapshotRange(): [string, string] {
  const range: KxDateRange = rangeFromSpanDays(7)
  return [range.start, range.end]
}

/** 可选的最晚一天：今天。今天可进区间（数据侧由覆盖行披露）；未来日期不可选。
 *  ★ 日切口径统一（2026-11-20 时区设置轮补口）：旧实现按 UTC 日历日取「今天」，
 *  而同文件的预设区间走 kxDatePresets 的系统时区日切 ⇒ CST 0:00–8:00 窗口内
 *  两者分属两个日历日（notAfter 落后区间端一天，未来一天被误放行）。统一改
 *  系统时区判据（systemTzDateStr），与本文件其余口径同源。 */
export function snapshotNotAfter(now = Date.now()): string {
  return systemTzDateStr(now)
}

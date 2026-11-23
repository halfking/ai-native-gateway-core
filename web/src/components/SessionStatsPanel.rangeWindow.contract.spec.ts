// SessionStatsPanel.rangeWindow.contract.spec.ts —— 会话统计面板的时间范围契约
// （2026-11-23 日期范围对齐轮建立；2026-11-24 统一轮改钉共享组件）。
//
// ★ 用户报的缺陷：「会话与统计」页签的日期范围选择与「看板」页签不一致」。
//
// ★ 旧契约钉的是**上一轮**（2026-11-23 审计轮 P2-1/P2-3）的形态：
//   「端点只收 days ⇒ 必须 presets-only（禁自定义）+ 显示滚动窗口起止」。
//   那个结论在当时是对的（端点确实只认 days，显式区间会被**静默压成跨度天数**），
//   但它解决的只是「别撒谎」，没解决「与看板一致」——
//   两页从此是**另一套控件 + 另一把尺**（看板＝日历日+可自定义；会话统计＝滚动档位）。
//
// ★ 本契约钉的四条（同源、同一把尺）：
//   ① **同一组件**：会话统计面板、看板页、会话分析·用户页必须用**同一个**
//      时间范围组件（StatsDateRangePicker.vue）；
//   ② **固定档位 + 自定义起止**：该组件的预设来自 makeDateRangePresets('date')
//      （今天/昨天/近7/14/30天/本月/上月），且**允许自定义起止**（不 presets-only）；
//   ③ **同一份状态**：仪表盘页里会话统计与看板共用同一份 BoardTimeRange
//      （dashboardBoard 提供 timeRange/setTimeRange）⇒ 切页签看到的区间相同；
//   ④ **同一把尺**：请求必须带**显式日历窗口** start_date/end_date
//      （后端 admin/dashboardapi/dashboard_window.go 据此把窗口谓词换成参数化的
//      [start, end)）⇒ 界面显示的区间 = 数据反映的区间。
//      ⇒ 「允许自定义」的前提就是 ④；没有 ④ 就必须回到 presets-only。
//
// ★ 为什么用源码契约而不是 mount：面板依赖 i18n / router / element-plus / echarts，
//   mount 成本高；本门钉的是**代码形状**，与同族做法一致
//   （components/SessionStatsPanel.probeScopeReload.test.ts）。
import { describe, it, expect } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

const CANDS = (p: string) => [
  resolve(process.cwd(), `web/src/${p}`),
  resolve(process.cwd(), `src/${p}`),
]

async function read(rel: string): Promise<string> {
  for (const c of CANDS(rel)) {
    try {
      return await readFile(c, 'utf8')
    } catch {
      /* next */
    }
  }
  throw new Error(`${rel} not found`)
}

/** 截取从 start 标记起的一段源码（标记缺失时抛错而不是静默通过）。 */
function slice(src: string, start: string, label: string, length = 600): string {
  const i = src.indexOf(start)
  expect(i, `${label}：找不到起点标记 ${start}`).toBeGreaterThan(-1)
  return src.slice(i, i + length)
}

describe('会话统计面板：日期范围必须与看板一致', () => {
  it('① 三处统计页必须用**同一个**时间范围组件（共享组件是唯一真源）', async () => {
    const picker = await read('components/analytics/StatsDateRangePicker.vue')
    const panel = await read('components/SessionStatsPanel.vue')
    const users = await read('views/admin/session-analytics/SessionAnalyticsUsersView.vue')
    const boardBar = await read('components/board/BoardFilterBar.vue')
    // 共享组件内部必须用的是全站统一控件 KxDateRangePicker
    expect(picker).toContain("import KxDateRangePicker from '../ui/KxDateRangePicker.vue'")
    // 两页都指向这个共享组件
    expect(panel).toContain('StatsDateRangePicker')
    expect(users).toContain('StatsDateRangePicker')
    // ★ 看板页也必须指向同一组件（2026-11-24 统一轮补）。
    //   原先看板页自己直写 KxDateRangePicker + 同一预设工厂（**形态等价**但两处代码）：
    //   等价时看不出差，可下一次改档位/跨度就会只改一边 ⇒ 门在这里钉死「同一个文件」。
    expect(boardBar).toContain('StatsDateRangePicker')
    // 会话分析页与看板页都不得再自持一套 KxDateRangePicker / 自造预设
    expect(users).not.toContain('<KxDateRangePicker')
    expect(boardBar).not.toContain('<KxDateRangePicker')
    // ★ 断言落在**导入语句**上而不是裸字符串：文件注释里可以（也应该）写明
    //   「原写法是 KxDateRangePicker + makeDateRangePresets('date')」，
    //   裸字符串断言会把这段说明当成违规 ⇒ 假红。
    //   真正要禁的是「自己 import 一份预设工厂」。
    expect(boardBar).not.toMatch(/import\s*\{[^}]*makeDateRangePresets/)
    expect(users).not.toMatch(/import\s*\{[^}]*makeDateRangePresets/)
  })

  it('② 共享组件的预设必须是固定档位（与看板同一工厂），且**允许自定义区间**', async () => {
    const picker = await read('components/analytics/StatsDateRangePicker.vue')
    expect(picker).toMatch(/makeDateRangePresets\('date'\)/)
    // 自定义入口必须开着（端点已支持显式区间 ⇒ 不需要 presets-only）
    expect(picker).not.toContain(':presets-only="true"')
    // 跨度上限与看板同款（92 天）
    expect(picker).toContain(':max-span-days="92"')
  })

  it('③ 面板必须与看板共用**同一份**范围状态（dashboardBoard.timeRange/setTimeRange）', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    const injectBlock = slice(src, 'inject<{', 'dashboardBoard inject', 400)
    expect(injectBlock).toContain('timeRange?: Ref<BoardTimeRange>')
    expect(injectBlock).toContain('setTimeRange?: (next: BoardTimeRange) => void')
    // 生效区间 = 共享状态换算来的
    expect(src).toMatch(/boardRangeToKxRange\(boardState\.timeRange\.value\)/)
    // 应用新区间 ⇒ 写回共享状态（⇒ 看板同步）
    expect(src).toMatch(/boardState\.setTimeRange\(boardRangeFromKxRange\(range\)\)/)
  })

  it('④ 请求必须带**显式日历窗口**（start_date/end_date）——界面显示的 = 数据反映的', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    const params = slice(src, 'extraParams: computed', 'extraParams', 500)
    expect(params).toContain('start_date: effectiveRange.value.start')
    expect(params).toContain('end_date: effectiveRange.value.end')
    // 范围变化必须触发重载（否则参数变了没人重新请求 ⇒ 区间与数据脱节）
    expect(src).toMatch(/watch\(effectiveRange,[\s\S]{0,200}void refresh\(\)/)
  })

  it('⑤ 看板与会话统计必须用**同一对**映射函数（不得各写一套映射）', async () => {
    const board = await read('components/board/BoardPanel.vue')
    const panel = await read('components/SessionStatsPanel.vue')
    expect(board).toContain('boardRangeToKxRange(timeRange.value)')
    expect(board).toContain('boardRangeFromKxRange(range)')
    expect(panel).toContain('boardRangeToKxRange(boardState.timeRange.value)')
    expect(panel).toContain('boardRangeFromKxRange(range)')
  })

  it('⑥ 旧的滚动窗口档位机制必须消失（4 个滚动档位 + 滚动起止回填）', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    expect(src).not.toContain('STATS_RANGE_BUCKETS')
    expect(src).not.toContain('rollingWindowRange')
    expect(src).not.toMatch(/import\s*\{[^}]*useSpanDaysRange/)
  })

  it('⑦ 会话分析页也必须把本页时间窗交给面板（同一页内两个口径不许打架）', async () => {
    const src = await read('views/admin/session-analytics/SessionAnalyticsUsersView.vue')
    expect(src).toMatch(/<SessionStatsPanel[\s\S]{0,300}:range="range"/)
    expect(src).toMatch(/@update:range="onApplyRange"/)
  })
})

describe('「统计说明」必须收成刷新按钮旁的点击提示符', () => {
  it('① 面板不再常驻说明横幅（说明已移出内容区）', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    expect(src).not.toContain('data-testid="stats-scope-info"')
    expect(src).not.toContain('sessions.stats.scopeNoticeBody')
  })

  it('② 提示符组件：点击才展开（popover trigger=click），内容用同一份 i18n 键', async () => {
    const src = await read('components/analytics/StatsScopeHint.vue')
    expect(src).toContain('trigger="click"')
    expect(src).toContain('sessions.stats.scopeNoticeBody')
    expect(src).toContain('sessions.stats.scopeNoticeHint')
    expect(src).toContain('sessions.stats.scopeHintLabel')
  })

  it('③ 两页的刷新按钮旁都必须挂这枚提示符', async () => {
    const dash = await read('views/DashboardViewV2.vue')
    const users = await read('views/admin/session-analytics/SessionAnalyticsUsersView.vue')
    expect(dash).toContain('<StatsScopeHint')
    expect(users).toContain('<StatsScopeHint')
    // 仪表盘页：提示符只在 stats 页签出现（其余页签没有那块面板）
    expect(dash).toMatch(/<StatsScopeHint v-if="activeTab === 'stats'"/)
  })

  it('④ 提示符的 days 必须是当前生效窗口的日历日跨度（说明文字不许说假话）', async () => {
    const users = await read('views/admin/session-analytics/SessionAnalyticsUsersView.vue')
    expect(users).toContain('rangeSpanDays(range.value)')
    expect(users).toContain(':days="scopeDays"')
  })

  it('⑤ 仪表盘页的 days 同样取自共享范围状态（自定义区间下 = 真实跨度，不是固定档位）', async () => {
    const dash = await read('views/DashboardViewV2.vue')
    expect(dash).toContain(':days="days"')
    // 共享状态在 custom 档位下把 days 写成**真实日历日跨度**
    // （boardRangeFromKxRange ⇒ preset:'custom', days: spanDays）
    // ⇒ 提示符那句「近 {days} 天」在自定义区间下依然成立。
    const util = await read('utils/boardTimeRange.ts')
    expect(util).toMatch(/preset: 'custom', days: spanDays/)
  })
})

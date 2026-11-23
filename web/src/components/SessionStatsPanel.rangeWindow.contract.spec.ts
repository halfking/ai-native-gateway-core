// SessionStatsPanel.rangeWindow.contract.spec.ts —— 会话统计面板的时间范围契约
// （2026-11-23 日期范围对齐轮重写）。
//
// ★ 用户报的缺陷：「会话与统计」页签的日期范围选择与「看板」页签不一致」。
//
// ★ 旧契约钉的是**上一轮**（2026-11-23 审计轮 P2-1/P2-3）的形态：
//   「端点只收 days ⇒ 必须 presets-only（禁自定义）+ 显示滚动窗口起止」。
//   那个结论在当时是对的（端点确实只认 days，显式区间会被**静默压成跨度天数**），
//   但它解决的只是「别撒谎」，没解决「与看板一致」——
//   ���页从此是**另一套控件 + 另一把尺**（看板＝日历日+可自定义；会话统计＝滚动档位）。
//
// ★ 本轮的新契约（三条都要钉）：
//   ① **同形**：控件的预设来自与看板**同一个工厂** makeDateRangePresets('date')，
//      且**允许自定义起止**（不再 presets-only）。
//   ② **同源**：仪表盘页里会话统计与看板共用**同一份** BoardTimeRange 状态
//      （dashboardBoard 提供 timeRange/setTimeRange）⇒ 切页签看到的区间相同，
//      改任一侧另一侧同步。
//   ③ **同尺**：请求必须带**显式日历窗口** start_date/end_date
//      （后端 admin/dashboardapi/dashboard_window.go 据此把窗口谓词换成参数化的
//      [start, end)）⇒ 界面显示的区间 = 数据反映的区间。
//      ⇒ 「允许自定义」的前提就是 ③；没有 ③ 就必须回到 presets-only。
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
  it('① 控件预设必须来自与看板同一个工厂（makeDateRangePresets），且**允许自定义区间**', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    // 预设来源：与 BoardFilterBar 同源（看板用的是 makeDateRangePresets('date')）
    expect(src).toMatch(/makeDateRangePresets\('date'\)/)
    // 自定义入口必须开着（端点已支持显式区间 ⇒ 不再需要 presets-only）
    const picker = slice(src, '<KxDateRangePicker', '时间范围控件', 400)
    expect(picker).not.toContain(':presets-only="true"')
    // 跨度上限与看板同款（92 天）
    expect(picker).toContain(':max-span-days="92"')
  })

  it('② 面板必须与看板共用**同一份**范围状态（dashboardBoard.timeRange/setTimeRange）', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    const injectBlock = slice(src, "inject<{", 'dashboardBoard inject', 400)
    expect(injectBlock).toContain('timeRange?: Ref<BoardTimeRange>')
    expect(injectBlock).toContain('setTimeRange?: (next: BoardTimeRange) => void')
    // 生效区间 = 共享状态换算来的
    expect(src).toMatch(/boardRangeToKxRange\(boardState\.timeRange\.value\)/)
    // 应用新区间 ⇒ 写回共享状态（⇒ 看板同步）
    expect(src).toMatch(/boardState\.setTimeRange\(boardRangeFromKxRange\(range\)\)/)
  })

  it('③ 请求必须带**显式日历窗口**（start_date/end_date）——界面显示的 = 数据反映的', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    const params = slice(src, 'extraParams: computed', 'extraParams', 500)
    expect(params).toContain('start_date: effectiveRange.value.start')
    expect(params).toContain('end_date: effectiveRange.value.end')
    // 范围变化必须触发重载（否则参数变了没人重新请求 ⇒ 区间与数据脱节）
    expect(src).toMatch(/watch\(effectiveRange,[\s\S]{0,200}void refresh\(\)/)
  })

  it('④ 看板与会话统计必须用**同一对**映射函数（不得各写一套映射）', async () => {
    const board = await read('components/board/BoardPanel.vue')
    const panel = await read('components/SessionStatsPanel.vue')
    expect(board).toContain('boardRangeToKxRange(timeRange.value)')
    expect(board).toContain('boardRangeFromKxRange(range)')
    expect(panel).toContain('boardRangeToKxRange(boardState.timeRange.value)')
    expect(panel).toContain('boardRangeFromKxRange(range)')
  })

  it('⑤ 旧的滚动窗口档位机制必须消失（4 个滚动档位 + 滚动起止回填）', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    expect(src).not.toContain('STATS_RANGE_BUCKETS')
    expect(src).not.toContain('rollingWindowRange')
    expect(src).not.toContain('statsDayPresets')
    expect(src).not.toMatch(/import\s*\{[^}]*useSpanDaysRange/)
  })

  it('⑥ 会话分析页也必须把本页时间窗交给面板（同一页内两个口径不许打架）', async () => {
    const src = await read('views/admin/session-analytics/SessionAnalyticsUsersView.vue')
    expect(src).toMatch(/<SessionStatsPanel[\s\S]{0,300}:range="range"/)
    expect(src).toMatch(/@update:range="onApplyRange"/)
  })
})

// SessionStatsPanel.rangeWindow.contract.spec.ts —— 会话统计面板的时间范围口径
// （2026-11-23 审计轮 P2-1 / P2-3）。
//
// ★ 两条缺陷同族：**界面显示的区间 ≠ 后端真实查询的窗口**。
//
//	① P2-3：服务端是**滚动窗口**（admin/dashboardapi/session_overview.go 的每条腿
//	   都是 `first_request_at >= NOW() - INTERVAL 'N days'`，响应自报
//	   window_mode="rolling_days"）⇒ 真实起点比「含首尾的 N 个日历日」早一天；
//	   而控件此前显示日历日区间（useSpanDaysRange ⇒ rangeFromSpanDays）
//	   ⇒ 用户看到的起止与真实范围对不上（起点差一天）。
//	② P2-1：端点只收 days ⇒ 自定义历史区间会被静默压成「跨度天数」
//	   ⇒ 面板漏加 :presets-only（AGENTS.md「12 页面 100% 收口」的表述因此不准确）。
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

describe('会话统计面板：时间范围口径必须与后端一致', () => {
  it('① P2-1 必须禁掉自定义入口（端点只收 days，手选区间会被静默改写）', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    const picker = src.slice(src.indexOf('<KxDateRangePicker'))
    expect(picker.slice(0, 400)).toContain(':presets-only="true"')
  })

  it('② P2-3 控件显示的必须是**滚动窗口**的起止（[today-N, today]）', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    // 不再走 useSpanDaysRange 的日历日回填（注释里的历史说明不算）
    expect(src).not.toMatch(/import\s*\{[^}]*useSpanDaysRange/)
    expect(src).toMatch(/function rollingWindowRange\(days: number\): KxDateRange/)
    // rangeFromSpanDays(n) = [today-(n-1), today] ⇒ days+1 才是 [today-days, today]
    expect(src).toContain('rangeFromSpanDays(days + 1)')
    // 控件值 = 当前 days 对应的滚动窗口
    expect(src).toContain('const statsRange = computed<KxDateRange>(() => rollingWindowRange(days.value))')
  })

  it('③ 应用档位时必须按**档位**回写 days（滚动窗口的日历日跨度是 N+1，按跨度回写会多一天）', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    const block = src.slice(src.indexOf('function applyStatsRange'))
    expect(block).toContain('STATS_RANGE_BUCKETS.find')
    expect(block).toContain('rangeSpanDays(range) - 1')
    expect(block).toContain('void changeDays(next)')
  })

  it('④ days=1 的档位必须标「近24小时」而不是「今天」（滚动 24 小时窗口不是今天）', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    const block = src.slice(src.indexOf('const STATS_RANGE_BUCKETS'))
    expect(block.slice(0, 400)).toContain("labelKey: 'common.dateRange.preset.last24h'")
    expect(block.slice(0, 400)).not.toContain("labelKey: 'common.dateRange.preset.today'")
  })

  it('⑤ 跨度上限必须容得下最宽档位（滚动窗口 N 天的日历日跨度是 N+1 ⇒ 91）', async () => {
    const src = await read('components/SessionStatsPanel.vue')
    const picker = src.slice(src.indexOf('<KxDateRangePicker'))
    expect(picker.slice(0, 400)).toContain(':max-span-days="91"')
  })
})

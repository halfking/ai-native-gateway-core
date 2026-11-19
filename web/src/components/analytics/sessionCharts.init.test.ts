// sessionCharts.init.test.ts —— 会话统计 tab 图表初始化竞态 + 工具栏布局契约（2026-11-20 轮）
//
// ★ 背景：owner 图批三指示——时间选择器移左上（原「统计周期」位置）、删「统计周期」
//   信息块、修两个「不渲染」的图表。图表空白的根因是 init 竞态：mount 时数据未到
//   ⇒ v-if="hasData" 没渲染 chartRef ⇒ initChart 空转；数据到位后 watch 只调
//   updateChart（要求已有实例）⇒ 图表从未初始化（320px 空盒子，无 canvas）。
//
// ★ 为什么用源码契约而不是 mount：echarts.init 在 jsdom 无 canvas 会炸，
//   竞态行为本身在真实浏览器才可复现 ⇒ 钉住「watch 里必须补 init」的代码形状。
import { describe, it, expect } from 'vitest'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

const CANDS = (p: string) => [
  resolve(process.cwd(), `web/src/components/${p}`),
  resolve(process.cwd(), `src/components/${p}`),
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

describe('会话统计图表初始化竞态（数据晚到必须补 init）', () => {
  it('① SessionTrendChart：data watch 在实例缺失且 chartRef 已在时 initChart', async () => {
    const src = await read('analytics/SessionTrendChart.vue')
    expect(src).toMatch(/if \(!chartInstance && chartRef\.value && !isDestroyed\.value\) initChart\(\)/)
    // watch 必须包 nextTick（v-if 把 chartRef 挂出来要等 DOM 更新）
    const watchBlock = src.slice(src.indexOf('watch(() => props.data'))
    expect(watchBlock.slice(0, 400)).toContain('nextTick')
  })

  it('② HealthGradeChart：distribution/avgScore watch 同款补 init', async () => {
    const src = await read('analytics/HealthGradeChart.vue')
    expect(src).toMatch(/if \(!chartInstance && chartRef\.value && !isDestroyed\.value\) initChart\(\)/)
    expect(src).toContain('ensureChartThenUpdate')
  })
})

describe('会话统计工具栏布局（owner 图批：选择器左上 + 删统计周期块）', () => {
  it('③ KxDateRangePicker 在 panel-toolbar 里排在 meta 之前（左侧）', async () => {
    const src = await read('SessionStatsPanel.vue')
    const pickerIdx = src.indexOf('<KxDateRangePicker')
    const metaIdx = src.indexOf('class="panel-toolbar__meta"')
    expect(pickerIdx).toBeGreaterThanOrEqual(0)
    expect(metaIdx).toBeGreaterThan(pickerIdx)
  })

  it('④ 「统计周期/周期新会话/日均新会话」信息块与死代码不得回魂', async () => {
    const src = await read('SessionStatsPanel.vue')
    expect(src).not.toContain('period-banner')
    expect(src).not.toContain('periodLabel')
    expect(src).not.toContain('trendSummary')
    expect(src).not.toContain('sessions.stats.periodRange')
  })
})

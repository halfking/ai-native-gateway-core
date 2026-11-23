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

describe('会话统计工具栏布局（owner 图批：选择器左上 + 删统计周期块 + 紧凑轮）', () => {
  it('③ 时间范围选择器在 panel-toolbar 的起始组内（左侧）', async () => {
    const src = await read('SessionStatsPanel.vue')
    // ★ 2026-11-24 统一轮：控件换成共享组件 StatsDateRangePicker
    //   （固定档位 + 自定义起止，与看板/会话分析页同一套）⇒ 布局契约不变。
    const pickerIdx = src.indexOf('<StatsDateRangePicker')
    const startIdx = src.indexOf('class="panel-toolbar__start"')
    expect(pickerIdx).toBeGreaterThanOrEqual(0)
    expect(startIdx).toBeGreaterThanOrEqual(0)
    // 选择器必须落在起始组**内部**（start 组标签在它之前，其闭合标签在它之后）
    expect(pickerIdx).toBeGreaterThan(startIdx)
  })

  it('④ 「统计周期/周期新会话/日均新会话」信息块与死代码不得回魂', async () => {
    const src = await read('SessionStatsPanel.vue')
    expect(src).not.toContain('period-banner')
    expect(src).not.toContain('periodLabel')
    expect(src).not.toContain('trendSummary')
    expect(src).not.toContain('sessions.stats.periodRange')
  })

  // 2026-11-23 owner 图批（紧凑轮）：右侧「最后更新 · Nms」与「查看探测流量」
  // 批「不需要，请移除」⇒ 这两个元素不得回魂（回魂＝右侧又长出一块，与
  // 「让页面更紧凑」的诉求相反，且是主人明确划掉的）。
  it('⑤ 工具栏右侧「最后更新 / 查看探测流量」不得回魂', async () => {
    const src = await read('SessionStatsPanel.vue')
    expect(src).not.toContain('sessions.stats.lastUpdated')
    expect(src).not.toContain('stats-probe-link')
    expect(src).not.toContain('openProbeTraffic')
    // 8 语种里那个失去引用的键也必须删干净（否则 i18n 死键棘轮门变红）
    for (const loc of ['zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA']) {
      const localeSrc = await readFile(
        resolve(process.cwd(), `web/src/locales/${loc}/sessions.ts`),
        'utf8',
      ).catch(() => readFile(resolve(process.cwd(), `src/locales/${loc}/sessions.ts`), 'utf8'))
      expect(localeSrc).not.toContain('lastUpdated')
    }
  })

  // 「包含探测」开关必须紧贴时间范围选择的**右边**（原先被 space-between
  // 顶到页面中间，离控件很远——主人图批「放到时间范围选择的右边」）。
  it('⑥ 「包含探测」开关紧跟时间范围选择器（同组、其后）', async () => {
    const src = await read('SessionStatsPanel.vue')
    const pickerIdx = src.indexOf('<StatsDateRangePicker')
    const probeIdx = src.indexOf('class="session-probe-scope"')
    expect(pickerIdx).toBeGreaterThanOrEqual(0)
    expect(probeIdx).toBeGreaterThan(pickerIdx)
    // 两者必须在同一个起始组容器里（组起点在选择器之前）
    const startIdx = src.indexOf('class="panel-toolbar__start"')
    expect(startIdx).toBeGreaterThanOrEqual(0)
    expect(startIdx).toBeLessThan(pickerIdx)
  })
})

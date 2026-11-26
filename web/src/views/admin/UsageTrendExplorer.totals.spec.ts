// UsageTrendExplorer.totals.spec.ts — 「总额展示」页面契约钉测（2026-11-26 总额展示轮）。
//
// ★ owner 诉求：/admin/usage-trends 页面上要能看到总额（当前时间窗 + 过滤条件下的
//   全量合计），此前汇总表只有逐模型行与「当前指标占比」列，没有总额。
//
// ★ 数据口径（判据的核心，坏掉必须红）：
//   总额 = series 逐行求和（含 '__others__' 折叠序列）——后端把 top-N 之外的模型
//   折进 '__others__' 且保留全部指标 ⇒ 行集合对当前过滤条件闭合 ⇒ 求和即真总额。
//   ⇒ 页面**不得**另起一个请求或只对可见行求和（长尾折叠时会少算）。
//
// ★ 为什么是源码契约而不是 mount：同 pinned.spec 的取舍——本页依赖 echarts
//   （jsdom 无 canvas）+ 路由 + 注入；纯函数口径由 usageTrend.test.ts 负责，
//   本文件钉「页面接线」。
//
// ★ 判据纪律：每条断言都必须能在被测对象坏掉时变红。
import { describe, expect, it } from 'vitest'
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

/** 去注释（组件头注释里会引用口径说明，别把说明文字当成实现）。 */
function stripComments(src: string): string {
  return src
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .split('\n')
    .map((line) => {
      const idx = line.indexOf('//')
      if (idx === -1) return line
      const before = line.slice(0, idx)
      if (before.trimEnd().endsWith(':')) return line
      return before
    })
    .join('\n')
}

describe('用量趋势：总额（合计行）', () => {
  it('① 汇总表启用合计行（el-table show-summary + summary-method）', async () => {
    const src = stripComments(await read('views/admin/UsageTrendExplorer.vue'))
    expect(src, '汇总表必须启用 show-summary（总额展示的载体）').toContain('show-summary')
    expect(src, '合计行内容必须由 summary-method 提供（默认求和只算当前页且不懂折叠口径）')
      .toContain(':summary-method="sumsToTotalCell"')
    expect(src, 'sumsToTotalCell 必须存在').toContain('function sumsToTotalCell(')
  })

  it('② 总额数据来自 usageTrendTotals（逐行求和、含 __others__），不是另起请求', async () => {
    const src = stripComments(await read('views/admin/UsageTrendExplorer.vue'))
    expect(src, '总额必须走单一真相纯函数 usageTrendTotals').toContain('usageTrendTotals(series.value)')
    // 不得在页面里对 points 重新求和（那会漏掉服务端已算好的 total_* 口径）
    expect(src, '页面不得对 points 重求总额（口径必须与占比列分母同源）')
      .not.toMatch(/points\s*\?\?|points\)\.reduce/)
  })

  it('③ 四个指标的总额列都要有（requests / tokens / credits / cost）', async () => {
    const src = stripComments(await read('views/admin/UsageTrendExplorer.vue'))
    const fn = src.slice(src.indexOf('function sumsToTotalCell('), src.indexOf('function sumsToTotalCell(') + 1200)
    expect(fn).toContain("total_requests:")
    expect(fn).toContain("total_tokens:")
    expect(fn).toContain("total_credits:")
    expect(fn).toContain("total_cost_usd:")
    // 合计行按列序返回数组（element-plus 2.8 SummaryMethod 签名），列身份用 property 匹配
    expect(fn).toContain("param.columns.map")
    expect(fn).toContain("c.property")
  })

  it('④ 合计行文案走 i18n usageTrend.totalRow（8 语言齐平）', async () => {
    const src = stripComments(await read('views/admin/UsageTrendExplorer.vue'))
    expect(src).toContain("t('usageTrend.totalRow')")
    for (const loc of ['zh-CN', 'zh-TW', 'en-US', 'ja-JP', 'de-DE', 'fr-FR', 'es-ES', 'ar-SA']) {
      const messages = await read(`locales/${loc}/usageTrend.ts`)
      expect(messages, `${loc} 必须有 totalRow 键（8 语言同步）`).toMatch(/totalRow:\s*'[^']+'/)
    }
  })

  it('⑤ 数值列必须有 prop（summary-method 按 property 分列取数）', async () => {
    const src = stripComments(await read('views/admin/UsageTrendExplorer.vue'))
    for (const prop of ['total_requests', 'total_tokens', 'total_credits', 'total_cost_usd']) {
      expect(src, `列 ${prop} 必须声明 prop（否则合计行取不到列）`)
        .toContain(`prop="${prop}"`)
    }
  })
})

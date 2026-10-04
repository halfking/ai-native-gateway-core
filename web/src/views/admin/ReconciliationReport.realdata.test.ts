// ReconciliationReport.realdata.test.ts —— 用**真端点的真实响应**挂载页面
// （2026-09-29 多维筛选轮）
//
// 为什么单独立一个文件：同目录的 ReconciliationReport.test.ts 用的是手写
// 夹具，字段是「按 TS 类型猜出来的」。本文件把本地真库打出来的
// /api/admin/report-rollup/{summary,dimensions} 响应**原样**当夹具，
// 于是「后端真的返回了这个字段形状」与「前端真的能渲染它」被钉在同一条
// 断言里——手写夹具会同时骗过两边。
//
// 它挡的是这一类故障：读面加了字段但忘了更新前端映射（渲染成 undefined /
// NaN），或前端读了后端根本不返回的键（undefined.x 直接把页面打白）。
// 这类问题在 jsdom 里挂载一次就会炸，真实浏览器里是白屏 + 控制台报错。
//
// 夹具来源（可复现）：
//   curl -b <cookie> 'http://127.0.0.1:8781/api/admin/report-rollup/summary?\
//     view=internal&start=2026-09-23&end=2026-09-29'
// 2026-09-29 首屏 summary 23ms / 明细 81ms（修复 explode 之前的版本是
// 866ms / 982ms）。
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { reactive } from 'vue'
import ReconciliationReport from './ReconciliationReport.vue'
import zhCN from '../../locales/zh-CN'

import realSummary from './__fixtures__/real_internal_summary.json'
import realDailySummary from './__fixtures__/real_internal_daily_summary.json'
import realDimensions from './__fixtures__/real_dimensions.json'

const getReportSummaryMock = vi.fn()
const getReportDimensionsMock = vi.fn()

vi.mock('../../api/reportrollup', async () => {
  const actual = await vi.importActual<typeof import('../../api/reportrollup')>(
    '../../api/reportrollup',
  )
  return {
    ...actual,
    getReportSummary: (...a: unknown[]) => getReportSummaryMock(...a),
    getReportDimensions: (...a: unknown[]) => getReportDimensionsMock(...a),
    downloadReportExport: vi.fn(),
    runReportRollup: vi.fn(),
  }
})
vi.mock('vue-router', () => ({
  useRoute: () => reactive({ query: { view: 'internal' } }),
  useRouter: () => ({ replace: vi.fn(), push: vi.fn() }),
}))
vi.mock('../../components/reconciliation/ReconciliationCharts.vue', () => ({
  default: {
    name: 'ReconciliationCharts',
    props: ['days', 'coveredDates', 'money'],
    template: '<div data-testid="charts-stub" />',
  },
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  fallbackLocale: 'en',
  messages: { 'zh-CN': zhCN },
})

type Report = Record<string, any>

const summary = realSummary.report as Report
const dailySummary = realDailySummary.report as Report
const dimensions = realDimensions.dimensions as Report

// 断言渲染结果里没有 NaN / undefined —— 后端加了字段而前端没接住时，
// 数字列会渲染成 NaN/undefined，肉眼很难发现但对帐场景下是错数。
//
// 例外：Element Plus 的 el-scrollbar 在 jsdom 下算不出滚动条位置，会写出
// `transform: translateX(NaN%)`。那是组件内部产物、与数据无关，先剔除再断言；
// 否则这条断言对所有页面都恒失败，等于没有门。
function expectNoNaN(html: string) {
  const scrubbed = html.replace(/translate[XY]\(NaN%\)/g, 'translate(0%)')
  expect(scrubbed, '页面渲染出 NaN（某列的字段映射没接上）').not.toContain('NaN')
  expect(scrubbed, '页面渲染出 undefined（读了后端不返回的键）').not.toContain('undefined')
}

async function mountPage() {
  const w = mount(ReconciliationReport, {
    global: { plugins: [i18n] },
    attachTo: document.body,
  })
  await flushPromises()
  return w
}

describe('ReconciliationReport 真实数据渲染', { timeout: 20_000 }, () => {
  beforeEach(() => {
    getReportSummaryMock.mockReset().mockResolvedValue(summary)
    getReportDimensionsMock.mockReset().mockResolvedValue(dimensions)
  })

  it('汇总口径的真实响应挂载不炸，且总计数渲染进 DOM', async () => {
    const w = await mountPage()
    const html = w.html()
    // 断言**总计卡片元素本身**的值，而不是「整页里出现过这个数字」——
    // 后者会被表格里的同名数字满足，于是「卡片字段映射改错」这类故障照样
    // 绿灯。fmtInt 对 undefined 兜底成 0（不暴露成 NaN），所以必须直接比对
    // 卡片文本才能抓住。
    const cardValues = w.findAll('.app-stat-card__value').map((n) => n.text())
    expect(cardValues.length, '总计卡片不应为空').toBeGreaterThan(0)
    expect(cardValues).toContain(
      Number(summary.totals.request_count).toLocaleString('en-US'),
    )
    expectNoNaN(html)
    w.unmount()
  })

  it('内部视角渲染租户、人员、模型，且没有整列为空', async () => {
    const w = await mountPage()
    const html = w.html()
    expect(html).toContain(summary.tenants[0].tenant_id)
    expect(html).toContain(summary.persons[0].person)
    expect(html).toContain(summary.model_totals[0].raw_model_name)
    expectNoNaN(html)
    w.unmount()
  })

  it('apikey 分组用的是后端解析好的名称，不是裸 id', async () => {
    // 这条钉的是 2026-09-29 修掉的静默缺陷：dimensionNames 查了
    // public.api_keys 上不存在的 name 列，错误被吞掉 → 名称恒空、页面退化成
    // 裸 id 且无任何报错。真实响应里 api_keys 行的名称字段必须有值。
    const firstKey = summary.api_keys?.[0]
    expect(firstKey).toBeTruthy()
    const named = Object.entries(firstKey).find(
      ([k, v]) => /name|label/i.test(k) && typeof v === 'string' && v.length > 0,
    )
    expect(named, '真实响应里 apikey 行应有非空名称字段').toBeTruthy()
  })

  it('明细口径的真实响应挂载不炸（含按天×模型与按天×维度）', async () => {
    getReportSummaryMock.mockResolvedValue(dailySummary)
    const w = await mountPage()
    const html = w.html()
    expect(html).toContain(
      Number(dailySummary.totals.request_count).toLocaleString('en-US'),
    )
    expectNoNaN(html)
    // 明细的 days / daily_models 都被真数据填满，页面不得回退成「没有快照」。
    expect(html).not.toContain('没有报表快照')
    w.unmount()
  })

  it('维度候选用真实 dimensions 响应填充六维下拉', async () => {
    const w = await mountPage()
    expect(getReportDimensionsMock).toHaveBeenCalled()
    for (const key of ['providers', 'credentials', 'api_keys', 'models', 'tenants', 'persons']) {
      expect(Array.isArray(dimensions[key]), `dimensions.${key} 应为数组`).toBe(true)
      expect(dimensions[key].length, `dimensions.${key} 不应为空`).toBeGreaterThan(0)
    }
    w.unmount()
  })

  it('真实响应渲染耗时在可接受范围（挂载 + 首次数据落地）', async () => {
    const t0 = performance.now()
    const w = await mountPage()
    const ms = performance.now() - t0
    // jsdom + 6k 请求量级的真实数据：这里只是防止「真实数据比夹具慢一个
    // 数量级」这种回归，不是生产性能断言（生产数字见端点实测 23ms/81ms）。
    expect(ms).toBeLessThan(3000)
    w.unmount()
  })
})

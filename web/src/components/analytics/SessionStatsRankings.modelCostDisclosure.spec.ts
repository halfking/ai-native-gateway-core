// SessionStatsRankings.modelCostDisclosure.spec.ts —— F1「模型榜按会话展开 ⇒ 重复计数」的行为门。
//
// ★ 背景（缺陷 F1）：admin/dashboardapi/session_overview.go 的模型腿把
//   ss.models_used 展开（unnest）后，对**每个模型行**都把该会话的整段
//   total_cost_usd 与 request_count 加了一遍 ⇒ 一个会话用了 N 个模型，它的
//   成本与请求数就在 N 个模型行各计一次 ⇒
//     · 本列合计**大于**面板 KPI；
//     · 各模型请求数之和不等于 KPI 请求数。
//   ⇒ 数字本身本轮不改，但必须**可见披露**，否则读者会拿这一列去和 KPI
//     求和/对账（对不上时只会怀疑数据，不会怀疑口径）。
//
// ★ 附带（同一组件的第二个口径问题）：模型/客户端/任务三张表的花费列都来自
//   SUM(ss.total_cost_usd) ⇒ **纯美元**，但字段名叫 total_cost ⇒ 列头必须
//   把单位写明。
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { testI18n } from '../../test-utils/i18n'
import SessionStatsRankings from './SessionStatsRankings.vue'

const MODEL_USAGE = [
  {
    model: 'claude-sonnet-4-5',
    session_count: 2,
    request_count: 6,
    total_cost: 12.5,
    success_rate: 1,
    avg_latency_ms: 120,
  },
]

const TOP_CLIENTS = [{ client_id: 'c-1', session_count: 3, total_cost: 4.25 }]
const TOP_TASKS = [{ task_id: 't-1', session_count: 5, total_cost: 6.5 }]

function factory() {
  return mount(SessionStatsRankings, {
    props: {
      topClients: TOP_CLIENTS,
      topTasks: TOP_TASKS,
      modelUsage: MODEL_USAGE,
    },
    global: { plugins: [testI18n] },
  })
}

describe('F1 · 模型榜必须披露「按会话展开 ⇒ 重复计数」', () => {
  it('① 模型表上方必须有一则可见披露', () => {
    const w = factory()
    const note = w.find('[data-testid="stats-model-cost-note"]')
    expect(note.exists(), '模型表缺少重复计数披露').toBe(true)
    const text = note.text()
    // ★ 披露必须点明三件事：按会话展开 / 合计大于 KPI / 不可与 KPI 相加。
    expect(text).toContain('会话')
    expect(text).toContain('KPI')
  })

  it('② 三张表的花费列头都必须写明单位是美元', () => {
    const w = factory()
    const headers = w.findAll('th').map((n) => n.text())
    const usdHeaders = headers.filter((h) => h.includes('（美元）'))
    expect(usdHeaders.length, '三张表都该用带单位的列头').toBe(3)
    // 旧的、不带单位的列头不得再出现（字段名叫 total_cost，读者会以为是总额）。
    expect(headers.filter((h) => h === '总成本')).toHaveLength(0)
  })
})

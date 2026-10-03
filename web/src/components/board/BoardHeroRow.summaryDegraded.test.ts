// BoardHeroRow.summaryDegraded.test.ts —— 首屏汇总降级必须有横幅
//
// ## 挡住的是什么
//
// `fallbackBoardSummary` 在 42P01（汇总聚合视图未迁移）时返回一张
// **全 0** 的 summary，并带 `degraded_summary` / `summary_missing_view`。
//
// 修前：前端 `BoardSummary` 类型里**根本没有**这几个字段，
// 首屏照常显示「总请求 0 / Token 0 / $0.00」——
// 而真相是「没算出来」。这是整个看板**最不该骗人的一屏**：
// 用户会据此判断「这段时间没有流量」，甚至去排查业务。
//
// 这也是「产出正确但没到消费者」的最后一环：
// 后端自 §17 起就写了降级标记，前端从来没读过它。
//
// ## 与 credits 降级是两个作用域（用例 ④ 固化这条边界）
//
// | 键 | 作用域 | 含义 |
// |---|---|---|
// | `degraded_summary` | 整个 summary | 请求/Token/费用全是 0 且不可作结论 |
// | `credits_missing_view` | 其中一个指标 | 只有积分没算出来，其余照常 |
//
// 2026-10-03 修掉的那处缺陷正是两者共用 `degraded` 一个键：
// 整屏降级会把积分卡也标成「不可信」，可那一屏明明有真实数字。
//
// ## 反向对照（实测，变异先断言确实生效）
//
// · 删掉横幅分支、保留 summaryDegraded 计算 → ①② 红（有状态没消费者）
// · 横幅判定恒真（`v-if="true"`）           → ③ ④ 红（防恒绿）
// · credits 判据改回读 `degraded`           → ④ 红（作用域混淆回归）

import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import BoardHeroRow from './BoardHeroRow.vue'

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: {
    'zh-CN': {
      dashboard: {
        stat: { totalRequests: '总请求', successRate: '成功率', totalCost: '总费用' },
        v2: {
          totalCredits: '总积分',
          creditsSub: '按定价 × token 计算',
          creditsDegraded: '积分聚合视图 {view} 尚未初始化，此数不可信',
          totalTokensShort: '总 Token',
          summaryDegraded: '汇总聚合视图 {view} 尚未初始化 —— 下方请求/Token/费用数字是「没算出来」，而不是 0，不可作为结论。',
        },
        board: { heroErrors: '错误数', heroInput: '输入', heroOutput: '输出', heroCreditsEq: '{n} 积分' },
      },
    },
  },
})

const board = (summary: Record<string, unknown>) => ({
  summary: {
    total_requests: 0,
    total_prompt_tokens: 0,
    total_completion_tokens: 0,
    total_tokens: 0,
    total_cost_usd: 0,
    total_credits_charged: 0,
    success_rate: 0,
    ...summary,
  },
  pies: { clients: [], client_ips: [], identity_hashes: [], models: [], errors: [], tenants: [], providers: [] },
  trends: [],
  days: 7,
})

const render = (summary: Record<string, unknown>) =>
  mount(BoardHeroRow, { props: { board: board(summary) as never }, global: { plugins: [i18n] } })

describe('首屏汇总降级三态', () => {
  it('① 整屏降级 → 显示横幅，且说清「不是 0」', () => {
    const w = render({ degraded_summary: true, summary_missing_view: 'request_stats_minute' })
    const text = w.text()
    expect(text).toContain('汇总聚合视图')
    expect(text).toContain('request_stats_minute')
    // 关键：横幅必须点明「0 ≠ 没有流量」，否则用户仍会误读下面那排 0。
    expect(text).toContain('没算出来')
  })

  it('② 健康（真的零流量）→ 不显示横幅', () => {
    const w = render({ total_requests: 0, total_cost_usd: 0 })
    expect(w.text()).not.toContain('没算出来')
    expect(w.text()).not.toContain('汇总聚合视图')
  })

  it('③ 有真实数字时（未降级）→ 不显示横幅', () => {
    const w = render({ total_requests: 100, total_cost_usd: 1.5 })
    const text = w.text()
    expect(text).toContain('100')
    expect(text).not.toContain('汇总聚合视图')
  })

  it('④ 作用域边界：整屏 degraded=true 但 credits 正常时，积分卡不得写「不可信」', () => {
    // 这是本轮修掉的那个缺陷：credits 判据曾读 `degraded`，
    // 而 `degraded` 在载荷顶层是 board 整体（pies/trends）的标记。
    // 整屏 pies 降级 + 积分正常 ⇒ 积分卡必须照常显示数字。
    const w = render({
      total_requests: 100,
      total_cost_usd: 1.5,
      total_credits_charged: 42,
      degraded: true,
      degraded_pies: { dimensions: ['clients'], reason: 'query failed' },
    })
    const text = w.text()
    expect(text).toContain('42')            // 积分数值照常
    expect(text).toContain('按定价 × token 计算') // 口径说明不被降级提示挤掉
    expect(text).not.toContain('此数不可信')
  })

  it('⑤ 边界反向：只有积分降级时，积分卡必须写「不可信」', () => {
    const w = render({
      total_requests: 100,
      total_credits_charged: 0,
      credits_missing_view: 'maas_credit_consumption_buckets',
    })
    expect(w.text()).toContain('此数不可信')
    expect(w.text()).not.toContain('没算出来') // 整屏横幅不该被点亮
  })
})

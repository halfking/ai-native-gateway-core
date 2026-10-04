// BoardHeroRow.creditsDegraded.test.ts —— 积分降级不得显示为 0
//
// ## 挡住的是什么
//
// 2026-10-03 之前 credits 降级时服务端静默返回 0，且：
//   · usage.go 的 summary 载荷里没有 degraded（主查询成功，标记不触发）；
//   · BoardHeroRow 的「总积分消耗」是**首屏高亮卡**，直接把 0 fmt 出来。
// 用户看到的是「这段时间没消耗积分」，而真相是「积分聚合视图没迁移，没算出来」。
//
// 与 T1 那 5 处裸数组降级、UserProfileList 的「降级时不说没有用户」同源：
// **把「不知道」渲染成「知道」比不显示更糟。**
//
// ## 反向对照
//
// 把 v-if="creditsDegraded" 去掉（退回无条件显示数字）后，
// ② 用例会红；把 degraded 判定改回 `?? false` 之类的错误写法也在 ① ③ 上有对照。

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
        stat: { totalRequests: '总请求', successRate: '成功率' },
        v2: { totalCredits: '总积分', creditsSub: '按定价 × token 计算', creditsDegraded: '积分聚合视图 {view} 尚未初始化，此数不可信' },
        board: { heroErrors: '错误数', heroInput: '输入', heroOutput: '输出', heroCreditsEq: '{n} 积分' },
      },
    },
  },
})

const board = (summary: Record<string, unknown>) => ({
  summary: {
    total_requests: 100,
    total_prompt_tokens: 10,
    total_completion_tokens: 20,
    total_tokens: 30,
    total_cost_usd: 1.5,
    total_credits_charged: 0,
    success_rate: 0.99,
    avg_latency_ms: 100,
    ...summary,
  },
  pies: { errors: [] },
  trends: [],
})

function render(summary: Record<string, unknown>) {
  return mount(BoardHeroRow, { props: { board: board(summary) as never }, global: { plugins: [i18n] } })
}

describe('BoardHeroRow 积分卡降级', () => {
  it('① 健康：显示真实积分数值与口径说明', () => {
    const w = render({ total_credits_charged: 1234 })
    const text = w.text()
    expect(text).toContain('1.2K')
    expect(text).toContain('按定价 × token 计算')
    expect(text).not.toContain('不可信')
  })

  it('② 降级：不得显示 0，必须给出「不可信」说明', () => {
    // 反向对照：去掉 v-if="creditsDegraded" 之后，这条会红。
    const w = render({ degraded: true, degraded_reason: 'credits: maas_credit_consumption_buckets', credits_missing_view: 'maas_credit_consumption_buckets' })
    const text = w.text()
    // 最要害的一条：高亮积分卡的**大数字**不能是 0（费用卡副行的
    // 「0 积分」是 total_cost_usd 的换算值，与本判据无关）。
    expect(w.text()).toContain('总积分—')
    expect(text).toContain('maas_credit_consumption_buckets')
    expect(text).toContain('不可信')
    // 口径说明必须让位——降级时那句「按定价 × token 计算」是误导。
    expect(text).not.toContain('按定价 × token 计算')
  })

  it('③ 真的消耗为 0（未降级）：显示 0，不误报', () => {
    // 反向对照：把 creditsDegraded 判定写成恒真的话，这条会红 ——
    // 那等于把「降级提示」变成常亮的装饰。
    const w = render({ total_credits_charged: 0 })
    const text = w.text()
    expect(text).toContain('0')
    expect(text).toContain('按定价 × token 计算')
    expect(text).not.toContain('不可信')
  })

  it('④ 降级时请求数/费用仍照常显示（降级只影响积分那一个指标）', () => {
    // 载荷是「真实数字 + 一个没算出来的 0」，所以不能整页都变成不可信。
    //
    // ⚠ 2026-10-03 修正：这一版**去掉了 payload.degraded: true**。
    // 原来的载荷带着它，而判据当时读的就是 summary.degraded ——
    // 于是这条用例把「顶层降级标记」和「积分降级」混成了一件事。
    // 判据收窄到 credits_missing_view 之后，degraded 属于**另一个作用域**
    // （board 整体的 pies/trends，见 applyBoardDegradation），
    // 留在载荷里只会让人误以为它仍在驱动这一条。
    // 作用域边界的正向/反向对照移到了 BoardHeroRow.summaryDegraded.test.ts 的 ④⑤。
    const w = render({ total_credits_charged: 0, credits_missing_view: 'v_credits' })
    expect(w.text()).toContain('100')   // 总请求
    expect(w.text()).toContain('1.50')   // 总费用
  })
})

// BoardOpsBar.opsDegraded.test.ts —— 运维 chip 不得在「没查出来」时宣称健康
//
// ## 挡住的是什么
//
// 2026-10-03 之前 `queryBoardBackgroundTasks` / `queryBoardSelfCheck` 三处
// `_ = ...Scan(...)` 丢弃查询错误，载荷里是 `checks_last_10m: 0`、
// `success_rate: 0.0`，而 BoardOpsBar 的模板是：
//
//     {{ ...opsChecks({ n: checks_last_10m ?? 0 }) }}
//     <span class="ops-dot ops-dot--ok">   ← 恒亮绿点
//
// ⇒ 查询失败时页面显示「近 10 分钟检查 0 次」+ **绿色健康灯**。
// 这比显示不出数字更糟：这个 chip 的职责恰恰是陈述系统健康状况，
// 它在**不知道**的时候主动宣称「一切正常」。
//
// ## 判据钉住的是「不许说谎」，不是「必须有提示」
//
// 三态：健康（绿点 + 真数字）/ 降级（中性点 + 明确说未知）/
// 真的为 0（绿点 + 0，不能误报成未知）。

import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import BoardOpsBar from './BoardOpsBar.vue'

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: {
    'zh-CN': {
      dashboard: {
        board: {
          bgTasks: '后台任务', discoveryStatus: '发现任务',
          opsRunning: '运行中', opsChecks: '近 10 分钟检查 {n} 次',
          opsChecksUnknown: '近 10 分钟检查次数未知',
          selfcheckTitle: '系统自检', opsRate24h: '近 24 小时成功率 {n}',
          opsRateUnknown: '近 24 小时成功率未知',
        },
      },
    },
  },
})

const render = (operational: unknown) =>
  mount(BoardOpsBar, { props: { operational: operational as never }, global: { plugins: [i18n] } })

const healthy = () => ({
  background_tasks: { discovery: { running: false, status: 'idle' }, probe_loop: { checks_last_10m: 7 } },
  selfcheck: { total_runs_24h: 12, success_rate: 0.95 },
})

describe('BoardOpsBar 降级态', () => {
  it('① 健康：绿点 + 真数字', () => {
    const w = render(healthy())
    expect(w.find('.ops-dot--ok').exists()).toBe(true)
    expect(w.text()).toContain('近 10 分钟检查 7 次')
    expect(w.text()).toContain('95.0%')
    expect(w.text()).not.toContain('未知')
  })

  it('② 后台任务查询失败：不得显示 0、不得亮绿灯', () => {
    // 反向对照：把 v-if="...probe_degraded" 去掉（退回 `?? 0`）后这条会红。
    const w = render({
      background_tasks: { discovery: { running: false }, probe_loop: { checks_last_10m: 0 }, degraded: true, probe_degraded: true },
      selfcheck: { total_runs_24h: 0, success_rate: 0 },
    })
    expect(w.text()).toContain('近 10 分钟检查次数未知')
    expect(w.text()).not.toContain('近 10 分钟检查 0 次')
    // 关键：后台任务那颗点不能是绿灯。
    // ⚠️ 只能查「第一个」点 —— 自检 chip 在这个夹具里是健康的，
    // 它那颗绿点是**应该**存在的（第一版断言 `ops-dot--ok 不存在` 就红了，
    // 那是我把「后台任务未知」写成了「整条 bar 未知」）。
    const dots = w.findAll('.ops-dot')
    expect(dots.length).toBe(2)
    expect(dots[0].classes()).toContain('ops-dot--unknown')
    expect(dots[0].classes()).not.toContain('ops-dot--ok')
    // 第二颗（自检，夹具里健康）应当仍是绿的。
    expect(dots[1].classes()).toContain('ops-dot--ok')
  })

  it('③ 自检查询失败：不得显示 0.0% 成功率', () => {
    // 反向对照：把 selfDegraded 判定去掉后这条会红。
    const w = render({
      background_tasks: { discovery: { running: false }, probe_loop: { checks_last_10m: 3 } },
      selfcheck: { total_runs_24h: 0, success_rate: 0, degraded: true },
    })
    expect(w.text()).toContain('近 24 小时成功率未知')
    expect(w.text()).not.toContain('0.0%')
  })

  it('④ 真的为 0 且未降级：显示 0，不误报成未知', () => {
    // 反向对照：把降级判定写成恒真的话，①④ 两条都会红 ——
    // 那等于把「未知提示」变成常亮装饰。
    const w = render({
      background_tasks: { discovery: { running: false, status: 'idle' }, probe_loop: { checks_last_10m: 0 }, degraded: false },
      selfcheck: { total_runs_24h: 0, success_rate: 0, degraded: false },
    })
    expect(w.text()).toContain('近 10 分钟检查 0 次')
    expect(w.text()).toContain('0.0%')
    expect(w.text()).not.toContain('未知')
    expect(w.find('.ops-dot--ok').exists()).toBe(true)
  })
})

// saCostSplit.contract.spec.ts —— N9「混合单位成本格」的跨文件契约门。
//
// ★ 背景（缺陷 N9）：会话分析族的 `total_cost` = internal_cost(美元) +
//   external_cost(积分) ⇒ **跨单位相加**（framework §3.2）⇒ 渲染成一个
//   带 $ 的数 = 凭空造出一个不存在的美元金额。
//
// ★ 为什么用源码契约而不是 mount：这些页面挂载成本高（i18n / router /
//   element-plus），本门钉的是**代码形状**（哪几格必须拆、哪几格**有意**
//   不拆）；渲染层的行为由 SaCostCell.spec.ts 断。
//
// ★ 三条判据：
//   ① 五个排行页 + Top5 四件套的成本格一律走 SaCostCell（双单位）；
//   ② 人员详情页的「指标卡 / 项目 / 任务」三处拆开，而**模型 / API Key**
//     两处**有意不拆**（那两条端点的 internal/external 语义与本族相反，
//     后端已单独登记 ⇒ 口径澄清前不得贴「美元 / 积分」标签）；
//   ③ TS 类型必须声明拆分字段（否则前端拿不到 ⇒ 又只能显示合计数）。
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

const REPO = resolve(process.cwd(), '..')
const SA = join(REPO, 'web/src/views/admin/session-analytics')

function src(rel: string): string {
  return readFileSync(join(SA, rel), 'utf8')
}

/** 必须拆开成本格的页面（含 Top5 四件套）。 */
const SPLIT_VIEWS = [
  'SessionAnalyticsUsersView.vue',
  'SessionAnalyticsUserRankingView.vue',
  'SessionAnalyticsAgentRankingView.vue',
  'SessionAnalyticsModelRankingView.vue',
  'components/SaTop5Rankings.vue',
]

describe('N9 · 会话分析族：混合单位成本必须分列两个单位', () => {
  it('① 五个排行页 / Top5 的成本格一律走 SaCostCell', () => {
    for (const v of SPLIT_VIEWS) {
      const s = src(v)
      expect(s, `${v} 没有引入 SaCostCell`).toContain('SaCostCell.vue')
      expect(s, `${v} 没有渲染 SaCostCell`).toContain('<SaCostCell')
      // ★ 旧写法（把 total_cost 当一个数渲染）必须消失。
      expect(s, `${v} 仍在渲染 money(...total_cost)`).not.toMatch(/money\([^)]*total_cost/)
    }
  })

  it('② 四个排行页都读了 internal_cost / external_cost 两个字段', () => {
    for (const v of SPLIT_VIEWS) {
      const s = src(v)
      expect(s, `${v} 没有读 internal_cost`).toContain('internal-cost=')
      expect(s, `${v} 没有读 external_cost`).toContain('external-cost=')
    }
  })

  it('③ 人员详情页：指标卡 / 项目 / 任务三处拆开；模型 / API Key 两处有意不拆', () => {
    const s = src('SessionAnalyticsUserDetailView.vue')
    // 指标卡：两张卡（内部成本美元 / 外部成本积分），锚点仍在「成本组」容器上。
    expect(s).toContain('data-testid="sa-metric-total-cost"')
    expect(s).toContain("t('sessionAnalytics.metric.internalCost')")
    expect(s).toContain("t('sessionAnalytics.metric.externalCost')")
    // 项目 / 任务两格走 SaCostCell。
    expect(s).toMatch(/<SaCostCell\s+:internal-cost="p\.internal_cost"/)
    expect(s).toMatch(/<SaCostCell\s+:internal-cost="task\.internal_cost"/)
    // ★ 模型 / API Key 两格**有意保持原样**（端点语义相反，后端已单独登记）。
    expect(s, '模型格的 money() 渲染被改动了（口径未澄清前不得动）').toMatch(
      /<!--[\s\S]{0,400}?-->\s*<td>\{\{ money\(m\.total_cost\) \}\}<\/td>/,
    )
    expect(s, 'API Key 格的 money() 渲染被改动了（口径未澄清前不得动）').toMatch(
      /<!--[\s\S]{0,400}?-->\s*<td>\{\{ money\(k\.total_cost\) \}\}<\/td>/,
    )
    // 且这两格不得贴「美元 / 积分」标签。
    expect(s).not.toMatch(/:internal-cost="m\.internal_cost"/)
    expect(s).not.toMatch(/:external-cost="m\.external_cost"/)
    expect(s).not.toMatch(/:internal-cost="k\.internal_cost"/)
    expect(s).not.toMatch(/:external-cost="k\.external_cost"/)
  })

  it('④ costNote 必须仍然可见（口径注脚不许被删掉）', () => {
    for (const v of [
      'SessionAnalyticsUsersView.vue',
      'SessionAnalyticsUserRankingView.vue',
      'SessionAnalyticsAgentRankingView.vue',
      'SessionAnalyticsModelRankingView.vue',
      'SessionAnalyticsUserDetailView.vue',
    ]) {
      expect(src(v), `${v} 把 costNote 注脚删掉了`).toContain("t('sessionAnalytics.costNote')")
    }
  })

  it('⑤ TS 类型必须声明拆分字段（前端拿不到就只能显示合计数）', () => {
    const api = readFileSync(join(REPO, 'web/src/api/sessionAnalytics.ts'), 'utf8')
    for (const iface of [
      'OverviewTopUser',
      'UserRankingRow',
      'UserDetailSummary',
      'UserProjectRow',
      'ProjectTaskRow',
      'AgentRankingRow',
      'ModelRankingRow',
    ]) {
      const m = api.match(new RegExp(`export interface ${iface} \\{([\\s\\S]*?)\\n\\}`))
      expect(m, `接口 ${iface} 不存在`).toBeTruthy()
      expect(m![1], `${iface} 没声明 internal_cost`).toContain('internal_cost')
      expect(m![1], `${iface} 没声明 external_cost`).toContain('external_cost')
      // total_cost 保留（向后兼容 + 排序），但必须标注它是跨单位合计。
      expect(m![1], `${iface} 丢掉了 total_cost（向后兼容字段）`).toContain('total_cost')
    }
  })

  it('⑥ 「—」规则必须写在唯一真源里（saCostSplit.ts）并被组件引用', () => {
    const helper = readFileSync(join(SA, 'saCostSplit.ts'), 'utf8')
    expect(helper).toContain('externalCostIsPlaceholder')
    expect(helper).toContain('Number(v) === 0')
    const cell = readFileSync(join(SA, 'components/SaCostCell.vue'), 'utf8')
    expect(cell).toContain('externalCostIsPlaceholder')
    expect(cell).toContain('sessionAnalytics.externalCostNotCarried')
  })
})

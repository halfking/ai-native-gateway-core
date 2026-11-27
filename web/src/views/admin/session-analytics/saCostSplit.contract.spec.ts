// Contracts for the separately measured USD cost and charged credits in V2.
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

  it('② 排行与 Top5 分别显示 V2 美元金额和积分', () => {
    for (const v of SPLIT_VIEWS) {
      const s = src(v)
      expect(s).toMatch(/:internal-cost="\w+\.total_cost"/)
      expect(s).toMatch(/:external-cost="\w+\.credits_charged"/)
    }
  })

  it('③ 人员详情的汇总、项目、任务、模型和密钥分开显示两种单位', () => {
    const s = src('SessionAnalyticsUserDetailView.vue')
    expect(s).toContain('data-testid="sa-metric-total-cost"')
    expect(s).toContain("t('sessions.userProfile.projectCostUsd')")
    expect(s).toContain("t('sessions.userProfile.projectCredits')")
    for (const row of ['p', 'task', 'm', 'k']) {
      expect(s).toContain(`:internal-cost="${row}.total_cost" :external-cost="${row}.credits_charged" projected`)
    }
  })

  it('④ 成本口径说明可见', () => {
    const detail = 'SessionAnalyticsUserDetailView.vue'
    for (const v of [...SPLIT_VIEWS.filter(v => !v.startsWith('components/')), detail]) {
      expect(src(v)).toContain("t('sessionAnalytics.costNote')")
    }
  })

  it('⑤ V2 类型声明美元金额和积分字段', () => {
    const api = readFileSync(join(REPO, 'web/src/api/sessionAnalytics.ts'), 'utf8')
    for (const iface of [
      'OverviewTopUser',
      'UserRankingRow',
      'UserDetailSummary',
      'UserProjectRow',
      'ProjectTaskRow',
      'AgentRankingRow',
      'ModelRankingRow',
      'UserModelUsage',
      'UserAPIKeyUsage',
    ]) {
      const declaration = api.split(`export interface ${iface} {`)[1]?.split('\n}')[0]
      expect(declaration, `接口 ${iface} 不存在`).toBeTruthy()
      expect(declaration).toContain('total_cost')
      expect(declaration).toContain('credits_charged')
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

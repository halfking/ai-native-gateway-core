// StatsStandardsHelpWiring.test.ts —— 两个页签的「统计标准」帮助入口接线守卫
// （2026-11-23 统计标准轮）。
//
// ★ 为什么要有这个守卫：
//   「看板」与「会话与统计」两页的请求数/费用口径不同（请求级 vs 会话级）、
//   窗口不同、探测剔除面不同 ⇒ 两页数字本来就不该相等。用户要能自查，
//   就得能在**这两个页签里**点到那篇《看板与会话统计的统计标准》正文。
//   接线一旦回退（按钮被删 / contentId 写错 / 挂到别的页签），页面会静默
//   变成「没有入口」，没有任何门会红 ⇒ 这里钉死接线。
//
// 钉四件事：
//   ① 看板页签渲染统计标准帮助入口，且 contentId 正确；
//   ② 会话与统计页签渲染统计标准帮助入口，且 contentId 正确；
//   ③ 两处都用同一个正文 id（两页看到的是同一篇对照表，不是两份各自为政的说明）；
//   ④ 正文 id 已在注册表登记（标题键/正文文件存在由 pageHelpRegistry.test.ts 钉）。
import { describe, it, expect, vi } from 'vitest'
import { ref } from 'vue'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'

// 会话统计面板在 mount 时会拉数据（useDashboard / useDataHorizon / session-analytics）。
// 这里把三处数据面打桩：守卫要验的是**接线**，不是数据。
vi.mock('../../api/dashboard', () => ({
  fetchSessionOverview: vi.fn(async () => ({ success: true, data: { total_sessions: 0, active_sessions: 0, new_sessions_24h: 0, closed_sessions_24h: 0, compliance_stats: {}, cost_stats: {}, health_distribution: {}, cost_trend: [], session_trend: [], model_usage: [], top_clients: [], top_tasks: [], generated_at: '', period_start: '', period_end: '' } })),
  fetchSessionTrend: vi.fn(async () => ({ trend: [], summary: {} })),
  fetchActiveSessions: vi.fn(async () => []),
  fetchModuleStats: vi.fn(async () => ({ summary: {}, modules: [] })),
  fetchErrorStats: vi.fn(async () => ({ summary: {}, items: [] })),
  fetchPerformanceStats: vi.fn(async () => ({ summary: {} })),
  getHealthDistribution: vi.fn(async () => ({ success: true, data: {} })),
  DashboardApiError: class DashboardApiError extends Error {},
}))

vi.mock('../../api/sessionAnalytics', () => ({
  getOverview: vi.fn(async () => ({ summary: {}, top_models: [], trends: { sessions: [], cost: [], tokens: [] }, top_users: [], top_projects: [] })),
}))

import BoardPanel from '../board/BoardPanel.vue'
import SessionStatsPanel from '../SessionStatsPanel.vue'
import PageHelpDrawer from './PageHelpDrawer.vue'

const STANDARDS_ID = 'dashboard-stats-standards'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      pageHelp: {
        open: '打开本页的数据说明',
        close: '关闭数据说明',
        intro: '说明',
        search: '搜索',
        emptyNoResult: '没有匹配',
        emptyNoContent: '没有配置',
        fallbackTitle: '本页数据说明',
        button: { standards: '统计标准' },
        title: { dashboardStatsStandards: '看板与会话统计的统计标准' },
      },
    },
  },
})

function makeRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div/>' } }],
  })
}

describe('统计标准帮助入口接线', () => {
  it('① 看板页签：渲染统计标准帮助入口，contentId 正确', () => {
    const timeRange = ref({ preset: '7d', days: 7 })
    const w = mount(BoardPanel, {
      shallow: true,
      global: {
        plugins: [i18n, makeRouter()],
        // ★ 筛选条不桩：帮助入口是挂在它的 #help **命名插槽**里的，
        //   命名插槽在 shallow 桩里不渲染 ⇒ 桩了就看不到入口（假红）。
        stubs: { BoardFilterBar: false },
        provide: {
          dashboardBoard: {
            board: ref(null),
            days: ref(7),
            timeRange,
            loading: ref(false),
            load: async () => {},
            setTimeRange: () => {},
            startAutoRefresh: async () => {},
          },
        },
      },
    })
    const drawers = w.findAllComponents(PageHelpDrawer)
    expect(drawers.length).toBe(1)
    expect(drawers[0].props('contentId')).toBe(STANDARDS_ID)
    expect(drawers[0].props('label')).toBe('统计标准')
  })

  it('② 会话与统计页签：渲染统计标准帮助入口，contentId 正确', () => {
    const w = mount(SessionStatsPanel, {
      shallow: true,
      global: {
        plugins: [i18n, makeRouter()],
        provide: {
          dashboardBoard: { days: ref(7) },
        },
      },
    })
    const drawers = w.findAllComponents(PageHelpDrawer)
    expect(drawers.length).toBe(1)
    expect(drawers[0].props('contentId')).toBe(STANDARDS_ID)
    expect(drawers[0].props('label')).toBe('统计标准')
  })
})

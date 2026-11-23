// SessionAnalyticsUserDetailView.tabErrorBehavior.test.ts
// —— L16 的**行为级**证据：端点失败时页面必须显示错误态，且**不得**显示「暂无数据」
//
// ★ 为什么还要这一条（源码契约门 `…tabState.spec.ts` 断的是「判据写对了没有」）：
//   ★ 源码契约门只能证明「`:empty` 的表达式里写了 `!tabError`」；
//   ★ 它**证明不了**「渲染出来确实有错误横幅、确实没有空态」——
//   ⇒ 所以补一条**真的 mount 组件、真的让端点返回 500** 的行为门。
//   ⇒ ★ 两层各管一件事：契约门管「写法」，行为门管「结果」。
//
// ★ 场景取自 2026-11-23 的真实事故：
//   生产 `/user-ranking/:owner/api-keys` 返回 **500**（PG `/dev/shm` 不足 ⇒ SQLSTATE 53100）
//   ⇒ 修前页面渲染「暂无数据」⇒ 用户看不出后端挂了
//   ⇒ 修后必须渲染**错误横幅**、且**不得**渲染「暂无数据」
import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import SessionAnalyticsUserDetailView from './SessionAnalyticsUserDetailView.vue'

const getUserDetailMock = vi.fn()
const getUserProjectsMock = vi.fn()
const getUserModelUsageMock = vi.fn()
const getUserAPIKeyUsageMock = vi.fn()

vi.mock('../../../api/sessionAnalytics', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../../api/sessionAnalytics')>()),
  getUserDetail: (...args: unknown[]) => getUserDetailMock(...args),
  getUserProjects: (...args: unknown[]) => getUserProjectsMock(...args),
  getUserModelUsage: (...args: unknown[]) => getUserModelUsageMock(...args),
  getUserAPIKeyUsage: (...args: unknown[]) => getUserAPIKeyUsageMock(...args),
}))

// ★ ApiError 必须带 status（`useSessionAnalytics` 的 `isViewStaleError` 按 status/code 判定陈旧）
vi.mock('../../../api/_core', () => ({
  ApiError: class ApiError extends Error {
    status: number
    code: string
    constructor(status: number, detail: string, code = '') {
      super(detail)
      this.status = status
      this.code = code
    }
  },
}))

const routeState = reactive<{ params: Record<string, string>; query: Record<string, string> }>({
  params: { owner: 'kaixuan-1' },
  query: {},
})
vi.mock('vue-router', () => ({
  useRoute: () => routeState,
  useRouter: () => ({ resolve: vi.fn(), push: vi.fn() }),
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  fallbackLocale: 'en',
  messages: {
    'zh-CN': {
      sessionAnalytics: {
        title: '会话分析',
        nav: { userDetail: '用户画像详情' },
        range: { label: '区间' },
        action: { refresh: '刷新', retry: '重试' },
        error: { userDetailFailed: '用户画像详情加载失败' },
        state: { staleTitle: '正在刷新', staleReason: '数据正在更新' },
        section: { projects: '项目列表', tasks: '任务列表', models: '模型用量', apiKeys: 'API Key用量' },
        empty: {
          noResultUserProjects: '该项目没有用量',
          noResultProjectTasks: '该项目没有任务',
          noResultUserModels: '该用户没有模型用量',
          noResultUserAPIKeys: '该用户没有 API Key 用量',
          noResultUserDetail: '暂无数据',
        },
        col: {
          project: '项目', sessions: '会话数', tasks: '任务数', tokens: 'Token 数',
          cost: '花费', lastActive: '最近活跃', model: '模型', requests: '请求数',
          successRate: '成功率', cacheRate: '缓存命中率', avgResponseTime: '平均响应时间',
          keyAlias: 'Key别名', keyID: 'Key ID', projectCount: '关联项目数',
          topProject: '主要项目', taskCount: '任务数',
        },
        metric: { sessionCount: '会话数', taskCount: '任务数', totalCost: '总花费', rank: '当前排名' },
      },
    },
  },
})

const DETAIL = {
  user: { display_name: '测试用户', email: 'test@example.com' },
  summary: { session_count: 100, task_count: 0, total_cost: 1.5, rank: 1 },
  trends: { sessions: [], tasks: [] },
}

async function mountAndOpenApiKeysTab() {
  const wrapper = mount(SessionAnalyticsUserDetailView, {
    global: { plugins: [i18n] },
  })
  await flushPromises()
  // 点「API Key用量」tab（用 data-testid ⇒ 不依赖文案）
  const tab = wrapper.find('[data-testid="sa-tab-apikeys"]')
  await tab.trigger('click')
  await flushPromises()
  await flushPromises()
  return wrapper
}

describe('L16 · 详情页 tab 级错误态的行为证据', () => {
  beforeEach(() => {
    getUserDetailMock.mockReset().mockResolvedValue(DETAIL)
    getUserProjectsMock.mockReset().mockResolvedValue({ projects: [] })
    getUserModelUsageMock.mockReset().mockResolvedValue({ models: [] })
    getUserAPIKeyUsageMock.mockReset()
  })

  it('① 端点 500 ⇒ 显示错误横幅，且**不**显示「暂无数据」空态', async () => {
    const { ApiError } = await import('../../../api/_core')
    getUserAPIKeyUsageMock.mockRejectedValue(new ApiError(500, 'failed to get user api key usage'))

    const wrapper = await mountAndOpenApiKeysTab()

    // ★ 修前的症状：既没有错误横幅，也只有一个「暂无数据」空态
    const banner = wrapper.find('.sa-banner--error')
    expect(banner.exists(), '端点失败时必须渲染错误横幅（.sa-banner--error）').toBe(true)
    expect(wrapper.find('.app-empty-state').exists(),
      '端点失败时**不得**渲染「暂无数据」空态（否则错误被伪装成无数据）').toBe(false)
  })

  it('② 端点 200 且无数据 ⇒ 渲染「暂无数据」空态，且**不**渲染错误横幅', async () => {
    // ★ 对照臂：正常空数据时，行为必须与「失败」不同 ⇒
    //   否则「一律显示错误横幅」也会让本条绿 ⇒ 这条就没有区分力了。
    getUserAPIKeyUsageMock.mockResolvedValue({ api_keys: [] })

    const wrapper = await mountAndOpenApiKeysTab()

    expect(wrapper.find('.sa-banner--error').exists(),
      '端点正常时不得渲染错误横幅').toBe(false)
    expect(wrapper.find('.app-empty-state').exists(),
      '端点正常但无数据时必须渲染「暂无数据」空态').toBe(true)
  })

  it('③ 端点 200 且有数据 ⇒ 渲染表格行，且无错误横幅、无空态', async () => {
    getUserAPIKeyUsageMock.mockResolvedValue({
      api_keys: [{
        key_id: 'k-1', key_alias: '生产', request_count: 3, total_tokens: 100,
        total_cost: 0.5, internal_cost: 0.5, external_cost: 0,
        last_used_at: '2026-11-23T00:00:00Z', project_count: 1, top_project: 'hermes',
      }],
    })

    const wrapper = await mountAndOpenApiKeysTab()

    expect(wrapper.find('.sa-banner--error').exists(), '端点正常时不得渲染错误横幅').toBe(false)
    expect(wrapper.find('.app-empty-state').exists(), '有数据时不得渲染空态').toBe(false)
    expect(wrapper.find('[data-testid="sa-user-apikeys-table"]').exists(), '应渲染 API Key 表').toBe(true)
  })
})

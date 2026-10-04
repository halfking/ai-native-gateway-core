// UserProfileListView.degraded.test.ts —— 「降级 / 失败 / 空」必须三态可区分
//
// 这一页此前把三种完全不同的情况渲染成**同一个页面**：
//   ① 请求成功且确实没有用户        → 空表格 +「没有用户」
//   ② 后端降级（200 + 空列表）      → 空表格 +「没有用户」   ← 把「不知道」说成「知道」
//   ③ 请求失败（500）                → 空表格 +「没有用户」   ← 且 catch 不记 console
//
// ②③ 两处都被 2026-10-03 的审计抓到，但**上一提交只修了 API 层**：本轮
// 复核时发现标记已经到达后端、却没有任何前端消费者 —— 产出正确、没到消费者。
//
// 判据要钉住的不只是「有横幅」，还包括最要害的一条：**降级时不得显示
// 「没有用户」**。那句话说出去就是错的。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { h } from 'vue'

const getUserProfileListMock = vi.fn()
vi.mock('../api/admin', () => ({
  getUserProfileList: (...a: unknown[]) => getUserProfileListMock(...a),
}))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))

// element-plus 的表格在 jsdom 下不需要真实渲染逻辑；桩件只保留「有没有数据」
// 这一个被断言的点，避免把测试变成 element-plus 的集成测试。
vi.mock('element-plus', () => ({
  ElButton: { setup: (_: unknown, { slots }: any) => () => h('button', slots.default?.()) },
  ElCard: { setup: (_: unknown, { slots }: any) => () => h('div', [slots.header?.(), slots.default?.()]) },
  ElInput: { setup: () => () => h('input') },
  ElPagination: { setup: () => () => h('div') },
  ElTable: { setup: (_: unknown, { slots }: any) => () => h('div', { class: 'el-table' }, slots.default?.()) },
  ElTableColumn: { setup: () => () => h('div') },
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      sessions: {
        userProfile: {
          title: '会话分析中心', ownerUser: 'Owner', sessionCount: '会话数',
          requestCount: '请求数', totalCost: '总花费', avgCostPerSession: '单会话均值',
          endUserCount: '终端用户', firstSeenAt: '首次出现', lastSeenAt: '末次出现',
          actionDetail: '详情', empty: '没有用户', userProfileSearchPlaceholder: '搜索',
        },
      },
      dataLifecycle: {
        usageCost: {
          degraded: { title: '部分指标不可用：', hint: '这些数字是占位，不是真实测量值。' },
        },
      },
    },
  },
})

async function render() {
  const View = (await import('./UserProfileListView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] } })
  await flushPromises()
  return w
}

const listPayload = (o: Record<string, unknown>) => ({ users: [], total: 0, limit: 20, offset: 0, ...o })

describe('UserProfileListView 三态', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  it('① 确实没有用户 → 显示「没有用户」且无任何告警', async () => {
    getUserProfileListMock.mockResolvedValue(listPayload({ degraded: false }))
    const w = await render()
    expect(w.find('.empty').exists()).toBe(true)
    expect(w.find('.alert-warning').exists()).toBe(false)
    expect(w.find('.alert-danger').exists()).toBe(false)
  })

  it('② 后端降级 → 显示告警，且**不得**说「没有用户」', async () => {
    getUserProfileListMock.mockResolvedValue(
      listPayload({ degraded: true, degraded_reason: 'missing relation: session_owners' }),
    )
    const w = await render()
    expect(w.find('.alert-warning').exists()).toBe(true)
    expect(w.find('.alert-warning').text()).toContain('missing relation: session_owners')
    // ★ 本页最要害的一条：降级时说「没有用户」是把「不知道」讲成「知道」。
    // 去掉 v-if 里的 !degraded.active 之后，这条会红。
    expect(w.find('.empty').exists()).toBe(false)
  })

  it('③ 请求失败 → 显示错误、不得说「没有用户」、且 console 有记录', async () => {
    getUserProfileListMock.mockRejectedValue(new Error('boom'))
    const w = await render()
    expect(w.find('.alert-danger').exists()).toBe(true)
    expect(w.find('.alert-danger').text()).toContain('boom')
    expect(w.find('.empty').exists()).toBe(false)
    // 反向对照的可对照面：修复前是 `catch { users.value = [] }`，无参 catch
    // 连 console 都进不去 ⇒ 这条断言对原实现必然为红。
    expect(console.error).toHaveBeenCalled()
  })

  it('反向对照：老服务端没有 degraded 字段 → 按「确实为空」处理，不得误报降级', async () => {
    getUserProfileListMock.mockResolvedValue({ users: [], total: 0, limit: 20, offset: 0 })
    const w = await render()
    expect(w.find('.alert-warning').exists()).toBe(false)
    expect(w.find('.empty').exists()).toBe(true)
  })
})

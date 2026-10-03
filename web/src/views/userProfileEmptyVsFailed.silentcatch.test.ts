// userProfileEmptyVsFailed.silentcatch.test.ts —— 「暂无数据」与「没取到」必须分开说
//
// ## 挡住的是什么
//
// `UserProfileView.load` 原来是：
//
//     try { data.value = await getUserProfile(owner, days); … }
//     catch { data.value = null }
//
// 而模板是 `<div v-if="!loading && !data">暂无用户画像数据</div>`。
// 于是加载失败时，页面**肯定地**告诉用户「这个用户没有画像数据」——
// 而真相是「没取到」。两者在页面上完全同形。
//
// 这与本轮其它几处同族：危害不在「少了提示」，在**页面替失败的加载编了一个说法**。
//
// 判据要断言的是那句话**不再出现**：
//   ① 加载失败 → 显示原因，且不出现「暂无用户画像数据」
//   ② 成功但确实没数据 → 仍然显示「暂无用户画像数据」（正向对照）
//   ③ 成功且有数据 → 渲染数据，且无任何空态
//
// 反向对照：把 catch 改回 `data.value = null`（撤掉本轮修复），
// ① 报红，②③ 保持绿 —— 只红失败那条，正例没被牵连。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const getUserProfileMock = vi.fn()

// 桩挂在视图**真正 import 的那个模块**上（views/admin 同一个桶）。
// 挂错模块时 vi.mock 不生效，症状是「卡片没渲染 / 断言空跑」，
// 看起来像产品问题。本轮已经因此栽了三次。
// ⚠️ `getUserProfile` 来自 **'../api/admin'**，不是桶文件 '../api'
// （本轮第四次栽在「桩挂错模块」上：症状是 mock 不生效、组件去调真实导出，
//  而报错长得像产品坏了）。先读 import 块，再写桩。
vi.mock('../api/admin', () => ({
  getUserProfile: (...a: unknown[]) => getUserProfileMock(...a),
}))

vi.mock('../store', async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>()
  return {
    ...actual,
    isSuperAdmin: () => true,
    isDefaultTenant: () => true,
    isReadOnlyMode: () => false,
    getCurrentTenantId: () => '',
    store: { get: () => null, set: () => {}, userInfo: { role: 'super_admin', username: 'admin' } },
    getLocale: () => 'zh-CN',
  }
})

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  // 组件读的是 `route.params.owner`（不是 query）—— 先看 import 块与
  // 路由消费点再写桩，否则报 `undefined.owner`，症状仍像产品问题。
  useRoute: () => ({ params: { owner: 'alice' }, query: { days: '7' } }),
  params: {},
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: {
    'zh-CN': {
      sessions: { userProfile: { empty: '暂无用户画像数据', loadFailed: '用户画像加载失败' } },
    },
  },
})

async function render() {
  const View = (await import('./UserProfileView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] }, shallow: false })
  await flushPromises()
  await flushPromises()
  return w
}

const EMPTY_TEXT = '暂无用户画像数据'

describe('UserProfileView：加载失败不得说成「暂无数据」', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
  })

  it('① 加载失败（Error）→ 显示服务端原因，且不出现「暂无用户画像数据」', async () => {
    // 产品优先展示 e.message（合理设计），所以这里断言 message 本身。
    getUserProfileMock.mockRejectedValue(new Error('502 Bad Gateway'))
    const w = await render()
    expect(getUserProfileMock).toHaveBeenCalled()
    const text = w.text()
    expect(text).not.toContain(EMPTY_TEXT)
    expect(text).toContain('502 Bad Gateway')
  })

  it('①b 加载失败（非 Error）→ 落 i18n 兜底文案，且仍不出现「暂无」', async () => {
    // 抛非 Error 才走 t('sessions.userProfile.loadFailed')。
    // 两种失败都要有话可说 —— 判据的意图是「原因一定会显示」，
    // 而不是「某一条具体字符串会显示」。
    getUserProfileMock.mockRejectedValue('boom')
    const w = await render()
    const text = w.text()
    expect(text).not.toContain(EMPTY_TEXT)
    expect(text).toContain('用户画像加载失败')
  })

  it('② 成功但确实没数据 → 仍然显示「暂无用户画像数据」', async () => {
    // 「真的没有」在这个接口里就是返回 null（data 的类型是 Detail | null），
    // 不是一个空对象。mock 成空对象的话 v-if="!data" 不成立，断言会落空。
    getUserProfileMock.mockResolvedValue(null)
    const w = await render()
    expect(w.text()).toContain(EMPTY_TEXT)
    expect(w.text()).not.toContain('用户画像加载失败')
  })

  it('③ 成功且有数据 → 渲染数据，无空态', async () => {
    getUserProfileMock.mockResolvedValue({
      daily_cost_trend: [],
      models: [{ model: 'gpt-test', requests: 3, cost: 0.02 }],
    })
    const w = await render()
    expect(w.text()).not.toContain(EMPTY_TEXT)
  })
})

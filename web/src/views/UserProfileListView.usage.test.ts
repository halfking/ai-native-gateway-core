// UserProfileListView.usage.test.ts —— 列表页「窗口内用量 + 数字出处 + 帮助抽屉」钉测
//
// 2026-11-20 数字出处轮。钉三件事：
//   ① 出处行（usage_meta 行）来自后端响应并在页面可见 —— 数字必须有出处；
//   ② 窗口用量五列的取值来自 window_usage（缺 = 「—」，不是 0）；
//   ③ 帮助按钮 + 右侧抽屉存在，且抽屉引用后端 usage_meta（不在前端硬编码口径）；
//   ④ 展开行按 owner + 当前窗口拉取员工×模型矩阵（用户/模型两个下钻维度）。
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { h, nextTick } from 'vue'
import zhCNSessions from '../locales/zh-CN/sessions'
import zhCNCommon from '../locales/zh-CN/common'

const getUserProfileListMock = vi.fn()
const getUserUsageMatrixMock = vi.fn()
vi.mock('../api/admin', () => ({
  getUserProfileList: (...a: unknown[]) => getUserProfileListMock(...a),
  getUserUsageMatrix: (...a: unknown[]) => getUserUsageMatrixMock(...a),
}))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }), useRouter: () => ({ push: vi.fn() }) }))
vi.mock('element-plus', () => ({
  ElButton: { setup: (_: unknown, { slots }: any) => () => h('button', slots.default?.()) },
  ElCard: { setup: (_: unknown, { slots }: any) => () => h('div', [slots.header?.(), slots.default?.()]) },
  ElInput: { setup: () => () => h('input') },
  ElPagination: { setup: () => () => h('div') },
  ElDrawer: { setup: (_: unknown, { slots }: any) => () => h('div', { class: 'drawer-stub' }, slots.default?.()) },
  ElTable: {
    setup: (_: unknown, { attrs, slots }: any) => () => h('div', {
      class: 'el-table',
      'data-expand-handler': typeof attrs.onExpandChange === 'function' ? 'yes' : 'no',
    }, slots.default?.()),
  },
  ElTableColumn: { setup: (_: unknown, { slots }: any) => () => h('div', slots.default?.()) },
  ElPopover: { setup: (_: unknown, { slots }: any) => () => h('div', [slots.default?.(), slots.reference?.()]) },
  ElDatePicker: { setup: () => () => h('input') },
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  messages: { 'zh-CN': { sessions: { userProfile: zhCNSessions.userProfile }, common: zhCNCommon } },
})

async function render() {
  const View = (await import('./UserProfileListView.vue')).default
  const w = mount(View, { global: { plugins: [i18n] } })
  await flushPromises()
  return w
}

const meta = { source_view: 'request_metrics', window_start: '2026-11-13T00:00:00Z', window_end: '2026-11-20T00:00:00Z' }

beforeEach(() => {
  vi.clearAllMocks()
  vi.spyOn(console, 'error').mockImplementation(() => {})
})

describe('UserProfileListView 数字出处轮', () => {
  it('① 出处行随响应渲染（source_view + 真实窗口）', async () => {
    getUserProfileListMock.mockResolvedValue({
      users: [], total: 0, limit: 20, offset: 0, usage_meta: meta,
    })
    const w = await render()
    const line = w.find('[data-testid="user-profile-usage-meta"]')
    expect(line.exists()).toBe(true)
    expect(line.text()).toContain('request_metrics')
  })

  it('② 帮助抽屉显示积分、USD 与投影来源', async () => {
    getUserProfileListMock.mockResolvedValue({ users: [], total: 0, limit: 20, offset: 0, usage_meta: meta })
    const w = await render()
    const btn = w.find('[data-testid="user-profile-list-help"]')
    expect(btn.exists()).toBe(true)
    const drawer = w.find('.drawer-stub')
    expect(drawer.exists()).toBe(true)
    expect(drawer.text()).toContain('积分')
    expect(drawer.text()).toContain('USD')
    expect(drawer.text()).toContain('request_metrics')
  })

  it('③ 展开行把 owner 与当前窗口传给矩阵端点（分用户/模型下钻的数据面）', async () => {
    getUserProfileListMock.mockResolvedValue({
      users: [{ owner_user: 'alice', session_count: 1, window_usage: { request_count: 5, total_tokens: 900, internal_credits: 0.1, external_cost_usd: 0.2, session_count: 1, model_count: 2 } }],
      total: 1, limit: 20, offset: 0, usage_meta: meta,
    })
    getUserUsageMatrixMock.mockResolvedValue({ rows: [], start_date: meta.window_start, end_date: meta.window_end })
    const w = await render()
    const table = w.find('.el-table')
    // ElTable stub 把 onExpandChange 暴露成 data 属性 —— 验证处理器真的被注册了
    expect(table.attributes('data-expand-handler')).toBe('yes')
    const comp = w.vm as unknown as { onExpandChange: (r: unknown, rows: Array<{ owner_user: string }>) => Promise<void> }
    await comp.onExpandChange({}, [{ owner_user: 'alice' }])
    await flushPromises()
    await nextTick()
    expect(getUserUsageMatrixMock).toHaveBeenCalledTimes(1)
    const arg = getUserUsageMatrixMock.mock.calls[0][0] as Record<string, string>
    expect(arg.owner).toBe('alice')
    // 窗口参数来自后端回传的真实窗口（不是前端脑补的 date_from）
    expect(arg.start_date).toBe(meta.window_start)
    expect(arg.end_date).toBe(meta.window_end)
  })

  it('④ 源码钉测：窗口列从 window_usage 取值，缺窗口显示 —（不是 0）', async () => {
    const { readFile } = await import('node:fs/promises')
    const { resolve } = await import('node:path')
    // vitest jsdom 下 import.meta.url 非 file: scheme ⇒ 与 BoardPanel.filterTop.test.ts
    // 同款用 process.cwd() 解析（仓内先例）。
    const src = await readFile(resolve(process.cwd(), 'src/views/UserProfileListView.vue'), 'utf8')
    expect(src).toContain('window_usage?.request_count ?? \'—\'')
    expect(src).toContain('window_usage ? formatTokens(scope.row.window_usage.total_tokens) : \'—\'')
    // 模板不得出现硬编码中文（口径文案全部走 i18n；HTML 注释不算 —— 与 CJK 棘轮同口径）
    const template = src.slice(src.indexOf('<template>')).replace(/<!--[\s\S]*?-->/g, '')
    expect(template).not.toMatch(/[\u4e00-\u9fff]/)
  })
})

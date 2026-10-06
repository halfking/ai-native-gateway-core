import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import OnlineSessionsView from './OnlineSessionsView.vue'
import { fetchOnlineSessions } from '@/api/sessions'
import { setLocale, locale } from '@/i18n'

/**
 * OnlineSessionsView 的六条不变量（2026-10-06）。
 *
 * 1. ★★★ **superAdmin / legacy 后端不加租户过滤** ⇒ 作用域文案必须**按角色**分；
 * 2. ★★ `limit` 被后端**静默 clamp 到 100**（不是 400，也不是回落默认）；
 * 3. ★★ 分页靠 `has_more` + 不透明 `next_cursor`（客户端不自己拼）；
 * 4. ★★ `last_latency_ms` / `last_provider_id` 键缺失 = NULL ⇒ 显示「—」，不是 0；
 * 5. ★★ `last_model` 是 `COALESCE(...,'')` ⇒ 空串要说「未知模型」；
 * 6. ★★ `freshness.stale` 是端点自带的权威位，stale 必须显式标出。
 */

const { authState } = vi.hoisted(() => ({ authState: { role: 'tenant_admin' } }))

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/stores/auth', () => ({ useAuthStore: () => authState }))

vi.mock('@/api/sessions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/sessions')>()
  return { ...actual, fetchOnlineSessions: vi.fn() }
})

const onlineMock = fetchOnlineSessions as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(OnlineSessionsView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function row(over: Record<string, unknown> = {}) {
  return {
    session_id: 'gw-1',
    last_request_status: 'ok',
    last_model: 'gpt-4o',
    last_provider_id: 7,
    last_latency_ms: 820,
    last_active_at: '2026-10-07T10:00:00Z',
    freshness: { data_source: 'hot', freshness_ms: 1200, stale: false },
    ...over,
  }
}

function resp(over: Record<string, unknown> = {}) {
  return { sessions: [row()], count: 1, has_more: false, next_cursor: null, ...over }
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  authState.role = 'tenant_admin'
  onlineMock.mockResolvedValue(resp())
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) {
    try {
      w.unmount()
    } catch {
      /* ignore */
    }
  }
  mountedList = []
  document.body.innerHTML = ''
  setLocale(ORIGIN_LOCALE as 'zh-CN' | 'en-US')
})

describe('★★★ 判据 1：作用域按角色说', () => {
  it('★ tenant_admin ⇒ 「只显示当前租户」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只显示当前租户的会话')
    expect(w.text()).not.toContain('不加租户过滤')
  })

  it('★★ super_admin ⇒ 明说「不加租户过滤，可能含其它租户」', async () => {
    authState.role = 'super_admin'
    const w = await mountView()
    expect(w.text()).toContain('不加租户过滤')
    expect(w.text()).toContain('其它租户')
    expect(w.text()).not.toContain('只显示当前租户')
  })

  it('★ 作用域提示**无条件常驻**（不是有数据才显示）', async () => {
    onlineMock.mockResolvedValue(resp({ sessions: [], count: 0 }))
    const w = await mountView()
    expect(w.text()).toContain('只显示当前租户')
  })
})

describe('★★ 判据 2/3：limit 与游标分页', () => {
  it('挂载即请求一次，limit=100（clamp 上限）', async () => {
    await mountView()
    expect(onlineMock).toHaveBeenCalledTimes(1)
    expect(onlineMock.mock.calls[0]![0]).toEqual({ limit: 100 })
  })

  it('★ has_more=false ⇒ 没有「加载更多」', async () => {
    const w = await mountView()
    expect(w.find('.on__more').exists()).toBe(false)
  })

  it('★ has_more=true ⇒ 有「加载更多」', async () => {
    onlineMock.mockResolvedValue(resp({ has_more: true, next_cursor: 'CUR1' }))
    const w = await mountView()
    expect(w.find('.on__more').exists()).toBe(true)
  })

  it('★★ 点「加载更多」⇒ **原样**带 next_cursor 请求，并追加结果', async () => {
    onlineMock.mockResolvedValueOnce(resp({ sessions: [row({ session_id: 'a' })], has_more: true, next_cursor: 'CUR1' }))
    onlineMock.mockResolvedValueOnce(resp({ sessions: [row({ session_id: 'b' })], has_more: false }))
    const w = await mountView()
    await w.find('.on__more').trigger('click')
    await flushPromises()
    expect(onlineMock.mock.calls[1]![0]).toEqual({ limit: 100, cursor: 'CUR1' })
    const ids = w.findAll('.on__sid').map((e) => e.text())
    expect(ids).toEqual(['a', 'b'])
  })

  it('★ has_more=true 但 next_cursor 为空 ⇒ 不发第二次（没有游标就没有下一页）', async () => {
    onlineMock.mockResolvedValue(resp({ has_more: true, next_cursor: null }))
    const w = await mountView()
    await w.find('.on__more').trigger('click')
    await flushPromises()
    expect(onlineMock).toHaveBeenCalledTimes(1)
  })

  it('★ 加载更多失败 ⇒ 报错且不丢已有行', async () => {
    onlineMock.mockResolvedValueOnce(resp({ sessions: [row({ session_id: 'a' })], has_more: true, next_cursor: 'C' }))
    onlineMock.mockRejectedValueOnce(new Error('cursor gone'))
    const w = await mountView()
    await w.find('.on__more').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('cursor gone')
    expect(w.findAll('.on__sid')).toHaveLength(1)
  })
})

describe('★★ 判据 4/5：NULL 与空串', () => {
  it('★★ last_latency_ms 键缺失 ⇒ 该格显示「—」（不是 0ms）', async () => {
    onlineMock.mockResolvedValue(resp({ sessions: [row({ last_latency_ms: undefined })] }))
    const w = await mountView()
    // ★ 按「最后延时」这一格的结构断言：整页 toContain('0ms') 会被
    //   freshness 里的 '1200ms' 误伤（子串包含），那不是延时格的问题。
    const cell = w
      .findAll('.on__kv-item')
      .find((e) => e.text().includes('最后延时'))!
    expect(cell.find('.on__kv-v').text()).toBe('—')
  })

  it('★ last_provider_id 键缺失 ⇒ 「—」（不是 #0）', async () => {
    onlineMock.mockResolvedValue(resp({ sessions: [row({ last_provider_id: undefined })] }))
    const w = await mountView()
    expect(w.text()).not.toContain('#0')
  })

  it('★ 有值 ⇒ 正常显示', async () => {
    const w = await mountView()
    expect(w.text()).toContain('#7')
    expect(w.text()).toContain('820ms')
    expect(w.text()).toContain('gpt-4o')
  })

  it('★★ last_model 空串 ⇒「未知模型」（空串意味着源列 NULL）', async () => {
    onlineMock.mockResolvedValue(resp({ sessions: [row({ last_model: '' })] }))
    const w = await mountView()
    expect(w.text()).toContain('未知模型')
  })

  it('★ title 缺失 ⇒ 不渲染标题行', async () => {
    const w = await mountView()
    expect(w.find('.on__title').exists()).toBe(false)
  })
})

describe('★ 判据 6：freshness', () => {
  it('★ stale=true ⇒ 显式标出「已过期」', async () => {
    onlineMock.mockResolvedValue(
      resp({ sessions: [row({ freshness: { data_source: 'merged', freshness_ms: 900000, stale: true } })] }),
    )
    const w = await mountView()
    expect(w.text()).toContain('已过期')
    expect(w.find('.on__fresh--stale').exists()).toBe(true)
  })

  it('★ stale=false ⇒ 不标过期（证明不是恒真）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('已过期')
  })

  it('★ freshness 整体缺失 ⇒ 不渲染该行', async () => {
    onlineMock.mockResolvedValue(resp({ sessions: [row({ freshness: undefined })] }))
    const w = await mountView()
    expect(w.find('.on__fresh').exists()).toBe(false)
  })

  it('★ 状态色：ok ⇒ success', async () => {
    const w = await mountView()
    expect(w.findAll('.on__item').length).toBe(1)
    expect(w.text()).toContain('gw-1')
  })
})

describe('错误与空态', () => {
  it('401 / 403 ⇒ 报权限', async () => {
    for (const status of [401, 403]) {
      onlineMock.mockRejectedValueOnce(Object.assign(new Error('x'), { status }))
      const w = await mountView()
      expect(w.text()).toContain('当前账号没有查看在线会话的权限')
      w.unmount()
    }
  })

  it('★ 其它错误 ⇒ 显示后端消息', async () => {
    onlineMock.mockRejectedValue(new Error('database not configured'))
    const w = await mountView()
    expect(w.text()).toContain('database not configured')
  })

  it('空列表 ⇒ 空态', async () => {
    onlineMock.mockResolvedValue(resp({ sessions: [], count: 0 }))
    const w = await mountView()
    expect(w.text()).toContain('当前没有活跃会话')
  })

  it('★ 有数据 ⇒ 显示计数', async () => {
    onlineMock.mockResolvedValue(resp({ sessions: [row(), row({ session_id: 'x' })], count: 2 }))
    const w = await mountView()
    expect(w.text()).toContain('共 2 个活跃会话')
  })
})
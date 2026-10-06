import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import SessionAuditView from './SessionAuditView.vue'
import { fetchSessionList, fetchSessionTimeline } from '@/api/sessions'
import { setLocale, locale } from '@/i18n'

/**
 * SessionAuditView 的六条不变量（2026-10-06）。
 *
 * 1. ★★★ `has_title` / `has_summary` / `missing_session_ids` / `has_session_id`
 *    是**恒定值**（SQL 没查那些表 / 空行被 continue 跳过）⇒ 页面**不得**渲染成
 *    「无标题」「会话 ID 缺失」，且要常驻图例解释为什么没有；
 * 2. ★★ `limit` 被后端**回显** ⇒ `sessions.length >= limit` 是**精确**的截断信号；
 * 3. ★★ `tenant` 也被回显 ⇒ 显示后端认下来的那个租户；
 * 4. ★★ `total_cost_usd = 0` 是 COALESCE 兜底值，不是免费；
 * 5. ★★★ `models_used` 可能是 `null`；
 * 6. ★★★ 展开拉 timeline：`numeric_fallback_resolved` = 后端**猜**的 id，必须标出。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/sessions', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/sessions')>()
  return { ...actual, fetchSessionList: vi.fn(), fetchSessionTimeline: vi.fn() }
})

const listMock = fetchSessionList as unknown as ReturnType<typeof vi.fn>
const tlMock = fetchSessionTimeline as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(SessionAuditView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function item(over: Record<string, unknown> = {}) {
  // ★ audit 必须**合并**进默认体，不能让末尾的 ...over 把它整个覆盖掉
  const { audit: auditOver, ...rest } = over
  return {
    id_kind: 'gw_session_id',
    primary_key: 'gw-abc',
    ...rest,
    audit: {
      session_id: 'gw-abc',
      has_compression: false,
      compression_hits: 0,
      total_turns: 3,
      has_session_id: true,
      missing_session_ids: 0,
      has_title: false,
      has_summary: false,
      models_used: ['gpt-4o'],
      total_prompt_tokens: 100,
      total_resp_tokens: 50,
      total_cost_usd: 0.0123,
      latest_at: '2026-10-07T10:00:00Z',
      audit_at: '2026-10-07T10:00:00Z',
      ...((auditOver as Record<string, unknown>) ?? {}),
    },
  }
}

function resp(over: Record<string, unknown> = {}) {
  return { tenant: 't1', limit: 50, id_kind: 'gw_session_id', sessions: [item()], ...over }
}

function tl(over: Record<string, unknown> = {}) {
  return {
    session_id: 'gw-abc',
    session_pk: null,
    session_id_source: 'path_gw_session_id',
    turns: [
      { request_id: 'r1', request_type: 'main', status: 'ok', outcome: 'final_success', is_final_success: true, model: 'gpt-4o', latency_ms: 800 },
      { request_id: 'r2', request_type: 'ext', status: 'ok', children: [{ request_id: 'r2a', request_type: 'ext2', status: 'ok' }] },
    ],
    count: 3,
    has_more: false,
    truncated: false,
    ...over,
  }
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  listMock.mockResolvedValue(resp())
  tlMock.mockResolvedValue(tl())
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

describe('入口与 limit', () => {
  it('挂载即请求一次，默认 limit=50', async () => {
    await mountView()
    expect(listMock).toHaveBeenCalledTimes(1)
    expect(listMock.mock.calls[0]![0]).toEqual({ limit: 50 })
  })

  it('★ 切 limit chip ⇒ 带新值重查', async () => {
    const w = await mountView()
    const chip = w.findAll('button').find((b) => b.text() === '前 100')!
    await chip.trigger('click')
    await flushPromises()
    expect(listMock.mock.calls[1]![0]).toEqual({ limit: 100 })
  })

  it('★ 显示后端**回显**的租户', async () => {
    listMock.mockResolvedValue(resp({ tenant: 'tenant-42' }))
    const w = await mountView()
    expect(w.text()).toContain('本列表的租户：tenant-42')
  })
})

describe('★★★ 判据 1：三个恒定字段不渲染 + 图例解释', () => {
  it('★ 不出现「无标题」这类断言', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('无标题')
    expect(w.text()).not.toContain('会话 ID 缺失')
  })

  it('★★ 图例常驻并说明「查询根本没关联标题表」', async () => {
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('这一页刻意不显示的三项')
    expect(text).toContain('根本没关联标题表')
    expect(text).toContain('恒为「无」')
  })

  it('★ 图例说明 ID 类型不是数值主键', async () => {
    const w = await mountView()
    expect(w.text()).toContain('gw_session_id')
  })

  it('★ 空列表时不显示图例', async () => {
    listMock.mockResolvedValue(resp({ sessions: [] }))
    const w = await mountView()
    expect(w.find('.sa__legend').exists()).toBe(false)
  })
})

describe('★★ 判据 2：limit 回显 ⇒ 精确截断', () => {
  it('★ sessions.length === 回显 limit ⇒ 提示「后面还有更多」', async () => {
    listMock.mockResolvedValue(resp({ limit: 2, sessions: [item({ primary_key: 'a' }), item({ primary_key: 'b' })] }))
    const w = await mountView()
    expect(w.text()).toContain('后面还有更多会话')
  })

  it('★ 用回显 limit 而不是请求 limit（请求 500、回显 2、返回 2 ⇒ 截断）', async () => {
    // 若错用「请求的 500」，2 < 500 会漏报
    listMock.mockResolvedValue(resp({ limit: 2, sessions: [item({ primary_key: 'a' }), item({ primary_key: 'b' })] }))
    const w = await mountView()
    await w.findAll('button').find((b) => b.text() === '前 500')!.trigger('click')
    await flushPromises()
    // 后端仍回显 2 ⇒ 仍应判截断
    expect(w.text()).toContain('后面还有更多会话')
  })

  it('★ sessions.length < limit ⇒ 显示正常计数（证明不是恒真）', async () => {
    listMock.mockResolvedValue(resp({ limit: 50, sessions: [item()] }))
    const w = await mountView()
    expect(w.text()).toContain('共 1 个会话')
    expect(w.text()).not.toContain('后面还有更多会话')
  })
})

describe('★★ 判据 3/4：成本兜底与稀疏模型', () => {
  it('★★ cost=0 ⇒ 弱化标记 + 图例说明不是免费', async () => {
    listMock.mockResolvedValue(resp({ sessions: [item({ audit: { total_cost_usd: 0 } })] }))
    const w = await mountView()
    expect(w.findAll('.sa__kv-item--maybe').length).toBeGreaterThan(0)
    expect(w.text()).toContain('不是「这次请求免费」')
  })

  it('★ cost 有值 ⇒ 不弱化（证明不是恒真）', async () => {
    const w = await mountView()
    expect(w.findAll('.sa__kv-item--maybe')).toHaveLength(0)
  })

  it('★ models_used=null ⇒ 「未记录模型名」，不是空白', async () => {
    listMock.mockResolvedValue(resp({ sessions: [item({ audit: { models_used: null } })] }))
    const w = await mountView()
    expect(w.text()).toContain('未记录模型名')
  })

  it('★ models_used 有值 ⇒ 列出模型', async () => {
    const w = await mountView()
    expect(w.text()).toContain('gpt-4o')
  })
})

describe('★★★ 判据 5：展开轮次树', () => {
  it('★ 默认不请求 timeline（懒加载）', async () => {
    await mountView()
    expect(tlMock).not.toHaveBeenCalled()
  })

  it('★ 点行 ⇒ 请求该会话 timeline', async () => {
    const w = await mountView()
    await w.find('.sa__item-head').trigger('click')
    await flushPromises()
    expect(tlMock).toHaveBeenCalledTimes(1)
    expect(tlMock.mock.calls[0]![0]).toBe('gw-abc')
  })

  it('★ 再点一次 ⇒ 收起（不再请求）', async () => {
    const w = await mountView()
    const head = w.find('.sa__item-head')
    await head.trigger('click')
    await flushPromises()
    await head.trigger('click')
    await flushPromises()
    expect(tlMock).toHaveBeenCalledTimes(1)
    expect(w.find('.sa__turns').exists()).toBe(false)
  })

  it('★ 拍平后子轮次也在（递归）', async () => {
    const w = await mountView()
    await w.find('.sa__item-head').trigger('click')
    await flushPromises()
    const ids = w.findAll('.sa__turn-meta').map((e) => e.text())
    expect(ids.some((s) => s.includes('r1'))).toBe(true)
    expect(ids.some((s) => s.includes('r2a'))).toBe(true)
  })

  it('★ 子轮次有缩进（层级可见）', async () => {
    const w = await mountView()
    await w.find('.sa__item-head').trigger('click')
    await flushPromises()
    const styles = w.findAll('.sa__turn').map((e) => e.attributes('style') ?? '')
    const depths = styles.map((s) => Number(/padding-left:\s*(\d+)px/.exec(s)?.[1] ?? 'NaN'))
    expect(Math.min(...depths.filter((d) => !Number.isNaN(d)))).toBe(8)
    expect(Math.max(...depths.filter((d) => !Number.isNaN(d)))).toBe(20)
  })

  it('★ 「唯一最终成功」标出', async () => {
    const w = await mountView()
    await w.find('.sa__item-head').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('本会话唯一最终成功')
  })

  it('★★★ 猜的 id ⇒ 必须警告', async () => {
    tlMock.mockResolvedValue(tl({ session_id_source: 'numeric_fallback_resolved' }))
    const w = await mountView()
    await w.find('.sa__item-head').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('后端是按数值主键「猜」出来的')
  })

  it('★ 正常文本 id ⇒ **不**出现猜 id 警告（证明不是恒真）', async () => {
    const w = await mountView()
    await w.find('.sa__item-head').trigger('click')
    await flushPromises()
    expect(w.text()).not.toContain('猜」出来的')
  })

  it('★ 未知来源 ⇒ 如实显示来源字符串', async () => {
    tlMock.mockResolvedValue(tl({ session_id_source: 'brand_new' }))
    const w = await mountView()
    await w.find('.sa__item-head').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('未知的会话 ID 来源：brand_new')
  })

  it('★★ 后端自带 truncated ⇒ 提示（不自己猜）', async () => {
    tlMock.mockResolvedValue(tl({ truncated: true, has_more: true, count: 2 }))
    const w = await mountView()
    await w.find('.sa__item-head').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('轮次被截断')
  })

  it('★ truncated=false ⇒ 不提示', async () => {
    const w = await mountView()
    await w.find('.sa__item-head').trigger('click')
    await flushPromises()
    expect(w.text()).not.toContain('轮次被截断')
  })

  it('★ 轮次为空 ⇒ 空态', async () => {
    tlMock.mockResolvedValue(tl({ turns: [], count: 0 }))
    const w = await mountView()
    await w.find('.sa__item-head').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('这个会话没有查到轮次')
  })

  it('★ timeline 失败 ⇒ 显示错误，且能再点重试', async () => {
    tlMock.mockRejectedValueOnce(new Error('boom')).mockResolvedValue(tl())
    const w = await mountView()
    // ★ 每次点击后组件会重渲染，必须**重新查询**按钮，不能复用旧 wrapper
    await w.find('.sa__item-head').trigger('click')
    await flushPromises()
    expect(w.text()).toContain('boom')
    await w.find('.sa__item-head').trigger('click') // 收起
    await flushPromises()
    await w.find('.sa__item-head').trigger('click') // 再展开 → 重试
    await flushPromises()
    expect(w.text()).toContain('r1')
  })
})

describe('错误与空态', () => {
  it('★★ 404 ⇒ 报「没有权限」（list 的未登录就是 404，不是 401）', async () => {
    listMock.mockRejectedValue(Object.assign(new Error('not found'), { status: 404 }))
    const w = await mountView()
    expect(w.text()).toContain('当前账号没有查看会话审计的权限')
  })

  it('401 / 403 ⇒ 同样报权限', async () => {
    for (const status of [401, 403]) {
      listMock.mockRejectedValueOnce(Object.assign(new Error('x'), { status }))
      const w = await mountView()
      expect(w.text()).toContain('当前账号没有查看会话审计的权限')
      w.unmount()
    }
  })

  it('★ 其它错误 ⇒ 显示后端消息', async () => {
    listMock.mockRejectedValue(new Error('query sessions failed'))
    const w = await mountView()
    expect(w.text()).toContain('query sessions failed')
  })

  it('空列表 ⇒ 空态', async () => {
    listMock.mockResolvedValue(resp({ sessions: [] }))
    const w = await mountView()
    expect(w.text()).toContain('这个租户下还没有会话记录')
  })

  it('压缩会话才显示压缩轮次', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('压缩轮次')
    listMock.mockResolvedValue(resp({ sessions: [item({ audit: { has_compression: true, compression_hits: 2 } })] }))
    const w2 = await mountView()
    expect(w2.text()).toContain('压缩轮次')
  })
})
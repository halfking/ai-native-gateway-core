import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import CacheStateView from './CacheStateView.vue'
import { fetchCacheState, CACHE_STATE_KEY_CAP } from '@/api/probeTimelineCache'
import { setLocale, locale } from '@/i18n'

/**
 * CacheStateView 的五条不变量（2026-10-07）。
 *
 * 1. ★★★ **503 ≠ 空**：两个来源（`availability reader not wired` /
 *    `redis client unavailable`）都表示「读不到」，绝不能显示成「缓存里没有条目」；
 * 2. ★★ 枚举被 `ScanKeys` 截断在 4096 且**无标记** ⇒ 撞上限只能说「可能被截断」；
 * 3. ★★★ 非法凭据 id（含 **"0"**）必须**本地拦下**：后端静默忽略 = 返回全量，
 *    用户会以为「筛了凭据 0」；
 * 4. ★★ `state`（探测判定）与 `available`（是否参与路由）是**两个独立字段**，
 *    「健康却不参与路由」正是本页要暴露的根因；
 * 5. ★ `entries` 后端显式初始化为 `[]`，但 null 也不能崩。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
});

vi.mock('@/api/probeTimelineCache', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/probeTimelineCache')>()
  return { ...actual, fetchCacheState: vi.fn() }
});

const csMock = fetchCacheState as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(CacheStateView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function entry(over: Record<string, unknown> = {}) {
  return {
    credential_id: 12,
    raw_model_name: 'gpt-4o',
    state: 'healthy',
    available: true,
    last_status: 'ok',
    consecutive_successes: 5,
    consecutive_failures: 0,
    updated_at: '2026-10-07T10:00:00Z',
    next_retry_at: null,
    source: 'direct',
    ...over
  }
}

function resp(over: Record<string, unknown> = {}) {
  return { reader: 'model_availability', key_prefix: 'model_avail:', credential_id: 0, model: '', count: 1, entries: [entry()], ...over }
}

/** 503：后端两个来源各自的 message。 */
function err503(msg: string) {
  return Object.assign(new Error(msg), { status: 503 })
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  csMock.mockResolvedValue(resp())
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

describe('入口与筛选', () => {
  it('挂载即请求一次，两个筛选都留空', async () => {
    await mountView()
    expect(csMock).toHaveBeenCalledTimes(1)
    expect(csMock.mock.calls[0]![0]).toEqual({ credentialId: undefined, model: undefined })
  })

  it('填两个筛选后带参数', async () => {
    const w = await mountView()
    const inputs = w.findAll('input.cs__input')
    await inputs[0]!.setValue('12')
    await inputs[1]!.setValue('gpt-4o')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(csMock.mock.calls[1]![0]).toEqual({ credentialId: 12, model: 'gpt-4o' })
  })

  it('只填模型 ⇒ credentialId 仍是 undefined（不把空串发成 0）', async () => {
    const w = await mountView()
    await w.findAll('input.cs__input')[1]!.setValue('gpt-4o')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(csMock.mock.calls[1]![0]).toEqual({ credentialId: undefined, model: 'gpt-4o' })
  })

  it('★ 没有筛选时不显示「清空筛选」（没东西可清）', async () => {
    const w = await mountView()
    const labels = w.findAll('button').map((b) => b.text())
    expect(labels).toContain('查询')
    expect(labels).not.toContain('清空筛选')
  })

  it('★ 填了筛选才出现「清空筛选」，且**不是**「取消」', async () => {
    const w = await mountView()
    await w.findAll('input.cs__input')[1]!.setValue('gpt-4o')
    const labels = w.findAll('button').map((b) => b.text())
    expect(labels).toContain('清空筛选')
    expect(labels).not.toContain('取消')
  })

  it('点「清空筛选」⇒ 两个输入都归零并重新请求全量', async () => {
    const w = await mountView()
    const inputs = w.findAll('input.cs__input')
    await inputs[0]!.setValue('12')
    await inputs[1]!.setValue('gpt-4o')
    const btn = w.findAll('button').find((b) => b.text() === '清空筛选')!
    await btn.trigger('click')
    await flushPromises()
    expect((w.findAll('input.cs__input')[0]!.element as HTMLInputElement).value).toBe('')
    expect(csMock.mock.calls[1]![0]).toEqual({ credentialId: undefined, model: undefined })
  })
})

describe('★★★ 判据 1：503 是「读不到」，绝不是「空」', () => {
  it('★ reader not wired ⇒ 显示部署问题，且**不**显示空态文案', async () => {
    csMock.mockRejectedValue(err503('availability reader not wired'))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('该部署未接入可用性缓存读取器')
    expect(text).not.toContain('缓存里没有匹配的条目')
    expect(text).not.toContain('Redis 客户端不可用')
  })

  it('★ redis client unavailable ⇒ 显示另一套文案，且**不**显示空态文案', async () => {
    csMock.mockRejectedValue(err503('redis client unavailable'))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('Redis 客户端不可用')
    expect(text).not.toContain('缓存里没有匹配的条目')
    expect(text).not.toContain('该部署未接入可用性缓存读取器')
  })

  it('★★ 两类都必须明说「不是缓存里什么都没有」', async () => {
    csMock.mockRejectedValue(err503('availability reader not wired'))
    const a = (await mountView()).text()
    csMock.mockRejectedValue(err503('redis client unavailable'))
    const b = (await mountView()).text()
    expect(a).toContain('不是「缓存里什么都没有」')
    expect(b).toContain('不是「缓存里什么都没有」')
  })

  it('★ 不把后端英文原文整句抛给用户（组件名可以留，原始 message 不行）', async () => {
    csMock.mockRejectedValue(err503('availability reader not wired'))
    const w = await mountView()
    expect(w.text()).not.toContain('availability reader not wired')
  })

  it('★ redis 分支同理', async () => {
    csMock.mockRejectedValue(err503('redis client unavailable'))
    const w = await mountView()
    expect(w.text()).not.toContain('redis client unavailable')
  })

  it('★ 503 但两种来源都不匹配 ⇒ 走普通错误，不误判成「未接线」', async () => {
    csMock.mockRejectedValue(err503('some other outage'))
    const w = await mountView()
    const text = w.text()
    expect(text).not.toContain('该部署未接入可用性缓存读取器')
    expect(text).not.toContain('Redis 客户端不可用')
    expect(text).toContain('some other outage')
    expect(text).not.toContain('缓存里没有匹配的条目')
  })

  it('★ 403 ⇒ 报「没有权限」，**不**归到「读不到」那两类', async () => {
    csMock.mockRejectedValue(Object.assign(new Error('forbidden'), { status: 403 }))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('当前账号没有查看可用性缓存的权限')
    expect(text).not.toContain('该部署未接入可用性缓存读取器')
    expect(text).not.toContain('Redis 客户端不可用')
  })
})

describe('★★ 判据 2：撞 4096 必须说「可能被截断」', () => {
  it('★ count=4096 ⇒ 提示截断，且**不**显示「共 4096 条」', async () => {
    csMock.mockResolvedValue(resp({ count: CACHE_STATE_KEY_CAP }))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('可能被截断')
    expect(text).not.toContain('共 4096 条')
  })

  it('★ count=4095 ⇒ 显示正常计数（证明上一条不是恒真）', async () => {
    csMock.mockResolvedValue(resp({ count: CACHE_STATE_KEY_CAP - 1 }))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('共 4095 条')
    expect(text).not.toContain('可能被截断')
  })
})

describe('★★★ 判据 3：非法凭据 id 必须本地拦下', () => {
  it('★ 非数字 ⇒ 不发请求，直接报「必须是正整数」', async () => {
    const w = await mountView()
    await w.findAll('input.cs__input')[0]!.setValue('abc')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(w.text()).toContain('凭据 ID 必须是正整数')
    expect(csMock).toHaveBeenCalledTimes(1)
  })

  it('★★ "0" ⇒ 同样本地拦下（`^\d+$` 会放行它，API 层 `>0` 又会丢掉 ⇒ 拿到全量）', async () => {
    const w = await mountView()
    await w.findAll('input.cs__input')[0]!.setValue('0')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(w.text()).toContain('凭据 ID 必须是正整数')
    expect(csMock).toHaveBeenCalledTimes(1)
  })

  it('负数 ⇒ 拦下', async () => {
    const w = await mountView()
    await w.findAll('input.cs__input')[0]!.setValue('-3')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(w.text()).toContain('凭据 ID 必须是正整数')
    expect(csMock).toHaveBeenCalledTimes(1)
  })

  it('★ 合法正整数 ⇒ 正常发请求（证明判据不是恒真）', async () => {
    const w = await mountView()
    await w.findAll('input.cs__input')[0]!.setValue('7')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(w.text()).not.toContain('凭据 ID 必须是正整数')
    expect(csMock).toHaveBeenCalledTimes(2)
    expect(csMock.mock.calls[1]![0]).toEqual({ credentialId: 7, model: undefined })
  })

  it('★ 前后空格被 trim 后仍判定为合法', async () => {
    const w = await mountView()
    await w.findAll('input.cs__input')[0]!.setValue('  7  ')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(csMock.mock.calls[1]![0]).toEqual({ credentialId: 7, model: undefined })
  })
})

describe('★★ 判据 4：state 与 available 矛盾', () => {
  it('★★ state=healthy + available=false ⇒ 标出矛盾（本页存在的理由）', async () => {
    csMock.mockResolvedValue(resp({ entries: [entry({ state: 'healthy', available: false })] }))
    const w = await mountView()
    expect(w.text()).toContain('不参与路由')
    expect(w.find('.cs__item--contra').exists()).toBe(true)
  })

  it('★ state=healthy + available=true ⇒ 不标矛盾（证明不是恒真）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('不参与路由')
    expect(w.find('.cs__item--contra').exists()).toBe(false)
  })

  it('★ state=failing + available=false ⇒ 不标矛盾（本来就故障，不是矛盾）', async () => {
    csMock.mockResolvedValue(resp({ entries: [entry({ state: 'failing', available: false })] }))
    const w = await mountView()
    expect(w.text()).not.toContain('不参与路由')
    expect(w.find('.cs__item--contra').exists()).toBe(false)
  })

  it('★ healthy_confirmed 别名也覆盖', async () => {
    csMock.mockResolvedValue(resp({ entries: [entry({ state: 'healthy_confirmed', available: false })] }))
    const w = await mountView()
    expect(w.find('.cs__item--contra').exists()).toBe(true)
  })

  it('★ state 大写也覆盖（后端枚举大小写不该决定结论）', async () => {
    csMock.mockResolvedValue(resp({ entries: [entry({ state: 'HEALTHY', available: false })] }))
    const w = await mountView()
    expect(w.find('.cs__item--contra').exists()).toBe(true)
  })

  it('★ 只有矛盾的那一条被标（多条混排时逐条判断）', async () => {
    csMock.mockResolvedValue(
      resp({
        entries: [
          entry({ credential_id: 1, raw_model_name: 'a', state: 'healthy', available: false }),
          entry({ credential_id: 2, raw_model_name: 'b', state: 'healthy', available: true })
        ]
      })
    )
    const w = await mountView()
    expect(w.findAll('.cs__item--contra')).toHaveLength(1)
  })
})

describe('空态与字段渲染', () => {
  it('entries=[] ⇒ 空态文案', async () => {
    csMock.mockResolvedValue(resp({ count: 0, entries: [] }))
    const w = await mountView()
    expect(w.text()).toContain('缓存里没有匹配的条目')
  })

  it('★ entries=null ⇒ 同样空态而不是崩溃', async () => {
    csMock.mockResolvedValue(resp({ count: 0, entries: null }))
    const w = await mountView()
    expect(w.text()).toContain('缓存里没有匹配的条目')
  })

  it('★ 未知 state ⇒ 显示「未知状态」而不是空标签', async () => {
    csMock.mockResolvedValue(resp({ entries: [entry({ state: 'brand_new_enum' })] }))
    const w = await mountView()
    expect(w.text()).toContain('未知状态')
  })

  it('★ 逐个枚举都能翻出中文标签（证明只有「未知」才回落）', async () => {
    csMock.mockResolvedValue(
      resp({
        entries: [
          entry({ credential_id: 1, raw_model_name: 'a', state: 'healthy' }),
          entry({ credential_id: 2, raw_model_name: 'b', state: 'healthy_confirmed' }),
          entry({ credential_id: 3, raw_model_name: 'c', state: 'available' }),
          entry({ credential_id: 4, raw_model_name: 'd', state: 'suspicious' }),
          entry({ credential_id: 5, raw_model_name: 'e', state: 'probing' }),
          entry({ credential_id: 6, raw_model_name: 'f', state: 'failing' }),
          entry({ credential_id: 7, raw_model_name: 'g', state: 'broken_confirmed' }),
          entry({ credential_id: 8, raw_model_name: 'h', state: 'unavailable' })
        ]
      })
    )
    const w = await mountView()
    const text = w.text()
    for (const label of [
      '健康',
      '已确认健康',
      '可用',
      '可疑',
      '探测中',
      '故障',
      '已确认故障',
      '不可用'
    ]) {
      expect(text).toContain(label)
    }
    expect(text).not.toContain('未知状态')
  })

  it('★ available=false ⇒ 「参与路由 否」', async () => {
    csMock.mockResolvedValue(resp({ entries: [entry({ available: false })] }))
    const w = await mountView()
    const av = w.find('.cs__avail')
    expect(av.text()).toContain('参与路由')
    expect(av.text()).toContain('否')
    expect(av.classes()).toContain('cs__avail--no')
  })

  it('available=true ⇒ 「参与路由 是」且无 --no 类', async () => {
    const w = await mountView()
    const av = w.find('.cs__avail')
    expect(av.text()).toContain('是')
    expect(av.classes()).not.toContain('cs__avail--no')
  })

  it('★ 连续失败 >0 ⇒ 标红；=0 ⇒ 不标', async () => {
    csMock.mockResolvedValue(resp({ entries: [entry({ consecutive_failures: 3, consecutive_successes: 0 })] }))
    const a = await mountView()
    expect(a.find('.cs__bad').exists()).toBe(true)
    csMock.mockResolvedValue(resp({ entries: [entry({ consecutive_failures: 0, consecutive_successes: 9 })] }))
    const b = await mountView()
    expect(b.find('.cs__bad').exists()).toBe(false)
  })

  it('模型名与凭据 id 都显示', async () => {
    const w = await mountView()
    expect(w.text()).toContain('gpt-4o')
    expect(w.text()).toContain('12')
  })

  it('next_retry_at 缺失 ⇒ 不渲染「下次重试」', async () => {
    csMock.mockResolvedValue(resp({ entries: [entry({ next_retry_at: null, updated_at: null })] }))
    const w = await mountView()
    expect(w.text()).not.toContain('下次重试')
  })

  it('next_retry_at 有值 ⇒ 渲染', async () => {
    csMock.mockResolvedValue(resp({ entries: [entry({ next_retry_at: '2026-10-07T12:00:00Z' })] }))
    const w = await mountView()
    expect(w.text()).toContain('下次重试')
  })
})
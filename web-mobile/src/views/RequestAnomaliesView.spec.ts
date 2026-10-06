import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import RequestAnomaliesView from './RequestAnomaliesView.vue'
import { fetchAnomalyList, fetchAnomalyCounts } from '@/api/requestAnomalies'
import { setLocale, locale } from '@/i18n'

/**
 * RequestAnomaliesView 的不变量（2026-10-07）。
 *
 * 1. ★★★★ 同一筛选面板里**大小写敏感度不一致**（provider/model 敏感度低，
 *    day/trigger 敏感）⇒ 类型只给三个小写按钮，且**明说**手填大写查不到；
 * 2. ★★★★ `model` 筛的是「客户端 **或** 出站模型」⇒ 两个模型都显示，
 *    网关重写过时要明说；
 * 3. ★★★★ `counts` **只含未解决**，与列表条数天然对不上 ⇒ 必须说明；
 * 4. ★★★ 指纹含 day ⇒ 同一问题**每天一行**，`occurrences` 只是今天的次数；
 * 5. ★★★ `param` 是逗号连接的多个 ⇒ 逐个显示成一个 tag 一个；
 * 6. ★★★ 档位是 **superAdmin** ⇒ 路由与抽屉席都带 requiresRole；
 * 7. ★★ list 与 counts **各自独立**失败。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/requestAnomalies', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/requestAnomalies')>()
  return { ...actual, fetchAnomalyList: vi.fn(), fetchAnomalyCounts: vi.fn() }
})

const listMock = fetchAnomalyList as unknown as ReturnType<typeof vi.fn>
const countsMock = fetchAnomalyCounts as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(RequestAnomaliesView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function rec(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    fingerprint: 'abc123',
    day: '2026-10-07',
    provider_id: 7,
    provider_code: 'openai',
    client_model: 'gpt-4o',
    outbound_model: 'gpt-4o-2024-11-20',
    protocol: 'chat',
    trigger: 'param_rejected',
    param: 'reasoning_effort',
    suggest_mode: '',
    http_status: 400,
    error_kind: 'unsupported_feature',
    error_sample: 'Unrecognized request argument supplied: reasoning_effort',
    occurrences: 5,
    recovered_count: 2,
    first_seen: '2026-10-07T09:00:00Z',
    last_seen: '2026-10-07T10:00:00Z',
    last_request_id: 'req-1',
    resolved: false,
    ...over,
  }
}

function listResp(over: Record<string, unknown> = {}) {
  return { anomalies: [rec()], count: 1, limit: 50, offset: 0, ...over }
}

function countsResp(over: Record<string, unknown> = {}) {
  return { unresolved: 3, new_today: 2, ...over }
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  listMock.mockResolvedValue(listResp())
  countsMock.mockResolvedValue(countsResp())
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

describe('入口与两个端点', () => {
  it('★★★ 挂载即请求 list 与 counts', async () => {
    await mountView()
    expect(listMock).toHaveBeenCalledTimes(1)
    expect(countsMock).toHaveBeenCalledTimes(1)
    expect(listMock.mock.calls[0]![0]).toMatchObject({ limit: 50, offset: 0, unresolvedOnly: false })
  })

  it('★ counts 显示 unresolved 与 new_today', async () => {
    const w = await mountView()
    expect(w.text()).toContain('未解决')
    expect(w.text()).toContain('今日新增')
    expect(w.text()).toContain('3')
  })
})

describe('★★★★ 判据 1：大小写敏感度', () => {
  it('★★★ 明说「供应商和模型不区分，但日期和类型区分」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('供应商和模型不区分大小写')
    expect(w.text()).toContain('日期和异常类型区分')
    expect(w.text()).toContain('手填大写会查不到')
  })

  it('★★★ 类型筛选只有三个按钮，取值与后端常量一致', async () => {
    const w = await mountView()
    const chips = w.findAll('.ra__chip')
    // 前三个是 trigger chips（后三个是 limit）
    expect(chips.slice(0, 3).map((c) => c.text())).toEqual(['参数被拒', '请求形态不符', '上游其它 4xx'])
  })

  it('★★ 没有任何 trigger 文本输入框（避免手填大小写错）', async () => {
    const w = await mountView()
    expect(w.findAll('select')).toHaveLength(0)
    const placeholders = w.findAll('input').map((i) => i.attributes('placeholder') ?? '')
    for (const p of placeholders) expect(p).not.toContain('异常类型')
  })

  it('★★★ 点类型按钮 ⇒ 带上后端字面值重查', async () => {
    const w = await mountView()
    await w.findAll('.ra__chip')[1]!.trigger('click')
    await flushPromises()
    expect(listMock.mock.calls[1]![0]).toMatchObject({ trigger: 'mode_mismatch' })
  })

  it('★★ 再点一次 ⇒ 取消该筛选', async () => {
    const w = await mountView()
    await w.findAll('.ra__chip')[1]!.trigger('click')
    await flushPromises()
    await w.findAll('.ra__chip')[1]!.trigger('click')
    await flushPromises()
    expect(listMock.mock.calls[2]![0]).toMatchObject({ trigger: undefined })
  })
})

describe('★★★★ 判据 2：两个模型都要显示', () => {
  it('★★★ 同时显示「请求的模型」与「实际发出去的模型」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('请求的模型')
    expect(w.text()).toContain('实际发出去的模型')
    expect(w.text()).toContain('gpt-4o')
    expect(w.text()).toContain('gpt-4o-2024-11-20')
  })

  it('★★★ 两模型不同时明说「网关做了模型重写」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('网关做了模型重写')
  })

  it('★★ 两模型相同时**不**说重写（证明不是恒真）', async () => {
    listMock.mockResolvedValue(listResp({ anomalies: [rec({ outbound_model: 'gpt-4o' })] }))
    const w = await mountView()
    expect(w.text()).not.toContain('网关做了模型重写')
  })

  it('★★ 模型键缺失 ⇒ 显示「—」，不是 undefined', async () => {
    const r = rec()
    delete (r as Record<string, unknown>).outbound_model
    listMock.mockResolvedValue(listResp({ anomalies: [r] }))
    const w = await mountView()
    expect(w.text()).toContain('—')
    expect(w.text()).not.toContain('undefined')
  })
})

describe('★★★ 判据 3：counts 只含未解决', () => {
  it('★★★ 明说「只统计未解决」且「与列表条数不会相等」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('只统计「未解决」的记录')
    expect(w.text()).toContain('本来就不会相等')
  })

  it('★★ 明说「今天」是网关时区，不是手机时区', async () => {
    const w = await mountView()
    expect(w.text()).toContain('按网关所在时区算')
    expect(w.text()).toContain('不是你手机的时区')
  })
})

describe('★★★ 判据 4：同一问题每天一行', () => {
  it('★★★ 明说「每天各记一行」且「出现次数是今天这一行的」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('「每天」会各记一行')
    expect(w.text()).toContain('不是这个问题的累计次数')
  })

  it('★ 出现次数行区分「今日出现」与「重试成功」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('今日出现 5 次')
    expect(w.text()).toContain('重试成功 2 次')
  })

  it('★ 首现日期与最近一次分开显示', async () => {
    const w = await mountView()
    expect(w.text()).toContain('首现日期 2026-10-07')
  })
})

describe('★★★ 判据 5：param 是逗号连接的多个', () => {
  it('★★★ 两个参数 ⇒ 渲染两个 tag', async () => {
    listMock.mockResolvedValue(listResp({ anomalies: [rec({ param: 'reasoning_effort, top_p' })] }))
    const w = await mountView()
    expect(w.findAll('.ra__tag').map((e) => e.text())).toEqual(['reasoning_effort', 'top_p'])
  })

  it('★★★ 无 param ⇒ 不渲染被拒参数区', async () => {
    listMock.mockResolvedValue(listResp({ anomalies: [rec({ param: undefined })] }))
    const w = await mountView()
    expect(w.findAll('.ra__tag')).toHaveLength(0)
    expect(w.text()).not.toContain('被拒参数')
  })

  it('★ 尾随逗号不留空 tag', async () => {
    listMock.mockResolvedValue(listResp({ anomalies: [rec({ param: 'top_p,' })] }))
    const w = await mountView()
    expect(w.findAll('.ra__tag')).toHaveLength(1)
  })

  it('★ suggest_mode 有值才显示「建议改用请求形态」', async () => {
    listMock.mockResolvedValue(listResp({ anomalies: [rec({ trigger: 'mode_mismatch', param: undefined, suggest_mode: 'chat' })] }))
    const w = await mountView()
    expect(w.text()).toContain('建议改用请求形态：chat')
  })
})

describe('★★ resolved 标记与错误样例', () => {
  it('★ resolved=true ⇒ 打「已解决」标签', async () => {
    listMock.mockResolvedValue(listResp({ anomalies: [rec({ resolved: true })] }))
    const w = await mountView()
    expect(w.findAll('.ra__done').length).toBe(1)
    expect(w.text()).toContain('已处理')
  })

  it('★ 未解决 ⇒ 无标签（证明不是恒真）', async () => {
    const w = await mountView()
    expect(w.findAll('.ra__done')).toHaveLength(0)
  })

  it('★ error_sample 原样显示（上游错误体，不含请求正文）', async () => {
    const w = await mountView()
    expect(w.find('.ra__sample').text()).toContain('Unrecognized request argument supplied')
  })
})

describe('★★★ 判据 7：两个端点各自独立失败', () => {
  it('★★★ counts 失败 ⇒ 列表照常显示', async () => {
    countsMock.mockRejectedValueOnce(new Error('count boom'))
    const w = await mountView()
    expect(w.text()).toContain('count boom')
    expect(w.text()).toContain('gpt-4o')
  })

  it('★★★ list 失败 ⇒ 计数照常显示', async () => {
    listMock.mockRejectedValueOnce(new Error('list boom'))
    const w = await mountView()
    expect(w.text()).toContain('list boom')
    expect(w.text()).not.toContain('没有查到请求侧异常')
    expect(w.text()).toContain('今日新增')
  })
})

describe('筛选与分页', () => {
  it('★★★ 勾「只看未解决」⇒ 带 unresolvedOnly=true 重查', async () => {
    const w = await mountView()
    await w.find('input[type="checkbox"]').setValue(true)
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(listMock.mock.calls[1]![0]).toMatchObject({ unresolvedOnly: true })
  })

  it('★★ 填三个筛选项 ⇒ 一并带上并 trim', async () => {
    const w = await mountView()
    const inputs = w.findAll('input[type="text"], input:not([type])')
    await inputs[0]!.setValue(' 2026-10-07 ')
    await inputs[1]!.setValue(' OpenAI ')
    await inputs[2]!.setValue(' gpt-4o ')
    await w.find('form').trigger('submit')
    await flushPromises()
    expect(listMock.mock.calls[1]![0]).toMatchObject({ day: '2026-10-07', provider: 'OpenAI', model: 'gpt-4o' })
  })

  it('★★ 「清空筛选」真的清空全部条件', async () => {
    const w = await mountView()
    const inputs = w.findAll('input[type="text"], input:not([type])')
    await inputs[0]!.setValue('2026-10-07')
    await inputs[1]!.setValue('openai')
    await w.find('form').trigger('submit')
    await flushPromises()
    await w.findAll('.ra__btn').find((b) => b.text() === '清空筛选')!.trigger('click')
    await flushPromises()
    for (const i of inputs) expect((i.element as HTMLInputElement).value).toBe('')
    expect(listMock.mock.calls[2]![0]).toMatchObject({ day: undefined, provider: undefined, model: undefined })
  })

  it('★★★ 切 limit ⇒ offset 归零', async () => {
    const w = await mountView()
    await w.findAll('.ra__chip').find((c) => c.text() === '前 100')!.trigger('click')
    await flushPromises()
    expect(listMock.mock.calls[1]![0]).toMatchObject({ limit: 100, offset: 0 })
  })

  it('★ count 超当前页 ⇒ 「下一页」可用', async () => {
    listMock.mockResolvedValue(listResp({ count: 120 }))
    const w = await mountView()
    const next = w.findAll('.ra__btn').find((b) => b.text() === '下一页')!
    expect(next.attributes('disabled')).toBeUndefined()
    await next.trigger('click')
    await flushPromises()
    expect(listMock.mock.calls[1]![0]).toMatchObject({ offset: 50 })
  })

  it('★★ 空结果 ⇒ 显示「没有查到请求侧异常」', async () => {
    listMock.mockResolvedValue(listResp({ anomalies: [], count: 0 }))
    const w = await mountView()
    expect(w.text()).toContain('没有查到请求侧异常')
  })
})
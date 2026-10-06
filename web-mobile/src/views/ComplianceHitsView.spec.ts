import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ComplianceHitsView from './ComplianceHitsView.vue'
import { fetchComplianceStats, fetchComplianceRecords, fetchComplianceReviewQueue } from '@/api/outputCompliance'
import { setLocale, locale } from '@/i18n'

/**
 * ComplianceHitsView 的不变量（2026-10-07）。
 *
 * 1. ★★★★★ `total_checks` 恒等于 `total_issues` ⇒ 必须标「不是检查次数」；
 * 2. ★★★★★ `jailbreak_hits` / `avg_latency_ms` 恒为 0 ⇒ 必须标「不是统计结果」；
 * 3. ★★★★ stats 只给三类计数，差额（internal_ip/bias）要显式说明；
 * 4. ★★★★ stats 单路失败静默回 0 ⇒ 页面必须带「0 可能是查询失败」的口径；
 * 5. ★★★★★ `content_preview` 只在 redacted=true 时有内容；未脱敏要明说「后端不给」；
 * 6. ★★★★★ 500 **绝不能**渲染成「队列为空 / 没有命中」；
 * 7. ★★★ review-queue 没有 total ⇒ 只能近似说「可能还有更多」。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/outputCompliance', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/outputCompliance')>()
  return {
    ...actual,
    fetchComplianceStats: vi.fn(),
    fetchComplianceRecords: vi.fn(),
    fetchComplianceReviewQueue: vi.fn(),
  }
})

const statsMock = fetchComplianceStats as unknown as ReturnType<typeof vi.fn>
const recMock = fetchComplianceRecords as unknown as ReturnType<typeof vi.fn>
const qMock = fetchComplianceReviewQueue as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(ComplianceHitsView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function statsBody(over: Record<string, unknown> = {}) {
  return {
    total_issues: 10,
    blocked: 4,
    pending_reviews: 2,
    total_checks: 10, // ★ 后端把它写成同一个值
    pii_hits: 2,
    secret_hits: 1,
    toxicity_hits: 0,
    jailbreak_hits: 0,
    avg_latency_ms: 0,
    last_updated: '2026-10-07T09:00:00Z',
    ...over,
  }
}

function rec(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    session_id: 'sess-1',
    check_type: 'pii',
    hit_type: 'email',
    severity: 8,
    redacted: true,
    content_preview: '[email] -> [REDACTED]',
    created_at: '2026-10-07T09:00:00Z',
    ...over,
  }
}

function recResp(over: Record<string, unknown> = {}) {
  return { records: [rec()], total: 1, limit: 50, offset: 0, ...over }
}

function queueItem(over: Record<string, unknown> = {}) {
  return {
    id: 9,
    audit_id: 5,
    request_id: 'req-9',
    session_key: 'sess-9',
    issue_type: 'secret',
    issue_subtype: 'api_key',
    severity: 9,
    status: 'pending',
    created_at: '2026-10-07 09:00:00+08',
    ...over,
  }
}

function queueResp(over: Record<string, unknown> = {}) {
  return { items: [queueItem()], status: 'pending', limit: 20, offset: 0, ...over }
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  statsMock.mockResolvedValue(statsBody())
  recMock.mockResolvedValue(recResp())
  qMock.mockResolvedValue(queueResp())
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

describe('入口与三个端点', () => {
  it('★★★ 挂载即请求 stats / records / review-queue', async () => {
    await mountView()
    expect(statsMock).toHaveBeenCalledTimes(1)
    expect(recMock).toHaveBeenCalledTimes(1)
    expect(qMock).toHaveBeenCalledTimes(1)
    expect(recMock.mock.calls[0]![0]).toMatchObject({ limit: 50, offset: 0 })
    expect(qMock.mock.calls[0]![0]).toMatchObject({ status: 'pending', limit: 20, offset: 0 })
  })

  it('★★ records 与 queue 默认条数不同（50 / 20），各自发各自的', async () => {
    const w = await mountView()
    expect(recMock.mock.calls[0]![0]).toMatchObject({ limit: 50 })
    expect(qMock.mock.calls[0]![0]).toMatchObject({ limit: 20 })
    // 队列没有 total ⇒ 页面上只能说「第 1-1 条」，不会出现「共 N 条」
    expect(w.text()).toContain('第 1-1 条')
    expect(w.text()).toContain('共 1 条') // 那是 records 的分页（有 total）
  })
})

describe('★★★★★ 判据 1/2：stats 里三个假数', () => {
  it('★★★★★ 明说「检查次数 = 命中总数」且别拿它算命中率', async () => {
    const w = await mountView()
    expect(w.text()).toContain('「检查次数」其实等于「命中总数」')
    expect(w.text()).toContain('别拿它算命中率')
  })

  it('★★★★★ 明说「越狱命中恒为 0 = 没有检测器，不是没有问题」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('「越狱命中」恒为 0')
    expect(w.text()).toContain('不是没有越狱问题')
    expect(w.text()).toContain('「平均耗时」也恒为 0')
  })

  it('★★★ 明说「0 可能是那一路查询失败」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('也可能是那一路查询失败被静默按 0 算了')
  })

  it('★★ 「这两个数不是统计结果」必须在页面上', async () => {
    const w = await mountView()
    expect(w.text()).toContain('这两个数不是统计结果')
  })
})

describe('★★★★ 判据 3：stats 只统计三类', () => {
  it('★★★★ 差额存在 ⇒ 明说是「内网地址」和「偏见」', async () => {
    // total=10，三类合计=3 ⇒ 差 7
    const w = await mountView()
    expect(w.text()).toContain('命中总数里有 7 条属于「内网地址」和「偏见」')
    expect(w.text()).toContain('没给单独的数字')
  })

  it('★ 差额为 0 ⇒ 不出这条说明（证明不是恒真）', async () => {
    statsMock.mockResolvedValue(statsBody({ total_issues: 3, pii_hits: 2, secret_hits: 1, toxicity_hits: 0 }))
    const w = await mountView()
    expect(w.text()).not.toContain('没给单独的数字')
  })
})

describe('★★★★★ 判据 5：preview 的安全约束', () => {
  it('★★★★★ 明说「预览只对已脱敏的行显示」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('预览只对引擎已经脱敏过的行显示')
    expect(w.text()).toContain('避免这里变成展示隐私与密钥原文的通道')
  })

  it('★★★ redacted=true ⇒ 显示预览原文', async () => {
    const w = await mountView()
    expect(w.find('.ch__preview').text()).toBe('[email] -> [REDACTED]')
  })

  it('★★★★ redacted=false ⇒ 不显示内容，且明说「是后端的安全约束」', async () => {
    recMock.mockResolvedValue(recResp({ records: [rec({ redacted: false, content_preview: '' })] }))
    const w = await mountView()
    expect(w.findAll('.ch__preview')).toHaveLength(0)
    expect(w.text()).toContain('这一行没有脱敏，所以不显示内容')
    expect(w.text()).toContain('这是后端的安全约束，不是不小心丢了')
  })

  it('★★★★★ 后端万一给了未脱敏行的内容，也**不许**显示（不依赖后端守规矩）', async () => {
    // ★ 这是本族最要紧的安全不变量：判据**不能**只依赖「后端保证未脱敏时
    //   content_preview 必为空串」这个前提 —— 一旦后端回归，页面就是
    //   一条把 PII/密钥原文摊平的通道。
    //   所以这里**故意**喂一个 redacted=false 但带内容的行，页面必须照样不显示。
    recMock.mockResolvedValue(
      recResp({ records: [rec({ redacted: false, content_preview: 'sk-live-ABCD1234@example.com' })] }),
    )
    const w = await mountView()
    expect(w.findAll('.ch__preview')).toHaveLength(0)
    expect(w.text()).not.toContain('sk-live-ABCD1234')
    expect(w.text()).toContain('这一行没有脱敏，所以不显示内容')
  })

  it('★ redacted=true 但 preview 为空 ⇒ 显示另一种说明', async () => {
    recMock.mockResolvedValue(recResp({ records: [rec({ redacted: true, content_preview: '' })] }))
    const w = await mountView()
    expect(w.text()).toContain('已脱敏，但没有可显示的预览文本')
  })
})

describe('★★★★★ 判据 6：500 绝不能退化成空态', () => {
  it('★★★★★ records 500 ⇒ 显示错误 + 成因提示，**不**显示「没有查到命中记录」', async () => {
    recMock.mockRejectedValueOnce(new Error('Failed to list compliance records'))
    const w = await mountView()
    expect(w.text()).toContain('Failed to list compliance records')
    expect(w.text()).not.toContain('没有查到命中记录')
    expect(w.text()).toContain('这通常不是「没有数据」')
  })

  it('★★★★★ review-queue 500 ⇒ 显示错误 + 成因，**不**显示「没有复核项」', async () => {
    qMock.mockRejectedValueOnce(new Error('Failed to scan review item'))
    const w = await mountView()
    expect(w.text()).toContain('Failed to scan review item')
    expect(w.text()).not.toContain('没有状态为')
    expect(w.text()).toContain('这通常不是「没有数据」')
  })

  it('★★ stats 失败不影响另外两块', async () => {
    statsMock.mockRejectedValueOnce(new Error('Failed to query stats'))
    const w = await mountView()
    expect(w.text()).toContain('Failed to query stats')
    expect(w.text()).toContain('[email] -> [REDACTED]')
    expect(w.text()).toContain('req-9')
  })

  it('★★ records 失败也不影响队列', async () => {
    recMock.mockRejectedValueOnce(new Error('boom'))
    const w = await mountView()
    expect(w.text()).toContain('req-9')
  })
})

describe('★★★ 判据 7：队列没有 total', () => {
  it('★★★ 明说「不返回总条数、只能推测」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('这个队列的接口不返回总条数')
    expect(w.text()).toContain('下面是近似值，不是精确总数')
  })

  it('★★ 本页排满 ⇒ 说「可能还有」', async () => {
    qMock.mockResolvedValue(queueResp({ items: Array.from({ length: 20 }, (_, i) => queueItem({ id: i + 1 })) }))
    const w = await mountView()
    expect(w.text()).toContain('这页排满了，后面可能还有')
  })

  it('★ 本页没排满 ⇒ 不说「可能还有」', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('后面可能还有')
  })
})

describe('筛选与分页', () => {
  it('★★★ 点命中类别 chip ⇒ 带 check_type 重查；再点取消', async () => {
    const w = await mountView()
    const chip = w.findAll('.ch__chip').find((c) => c.text() === '隐私信息')!
    await chip.trigger('click')
    await flushPromises()
    expect(recMock.mock.calls[1]![0]).toMatchObject({ checkType: 'pii' })
    await chip.trigger('click')
    await flushPromises()
    expect(recMock.mock.calls[2]![0]).toMatchObject({ checkType: undefined })
  })

  it('★★ 五个类别 chip 都在（内部地址/偏见虽无 stats 计数但 records 能筛）', async () => {
    const w = await mountView()
    const texts = w.findAll('.ch__chip').slice(0, 5).map((c) => c.text())
    expect(texts).toEqual(['隐私信息', '有害内容', '密钥', '内网地址', '偏见'])
  })

  it('★★★ 切复核状态 ⇒ 带 status 重查', async () => {
    const w = await mountView()
    await w.findAll('.ch__chip').find((c) => c.text() === '已通过')!.trigger('click')
    await flushPromises()
    expect(qMock.mock.calls[1]![0]).toMatchObject({ status: 'approved' })
  })

  it('★ 填子类型提交 ⇒ 带 hit_type 重查', async () => {
    const w = await mountView()
    await w.find('input').setValue('  email  ')
    await w.findAll('form')[0]!.trigger('submit')
    await flushPromises()
    expect(recMock.mock.calls[1]![0]).toMatchObject({ hitType: 'email' })
  })

  it('★★ 「清空筛选」清掉类别与子类型', async () => {
    const w = await mountView()
    await w.findAll('.ch__chip').find((c) => c.text() === '密钥')!.trigger('click')
    await w.find('input').setValue('api_key')
    await flushPromises()
    await w.findAll('.ch__btn').find((b) => b.text() === '清空筛选')!.trigger('click')
    await flushPromises()
    expect((w.find('input').element as HTMLInputElement).value).toBe('')
    expect(recMock.mock.calls.at(-1)![0]).toMatchObject({ checkType: undefined, hitType: undefined })
  })

  it('★★★ records 有 total ⇒ 分页信息是精确的', async () => {
    recMock.mockResolvedValue(recResp({ total: 120 }))
    const w = await mountView()
    expect(w.text()).toContain('第 1-1 条，共 120 条')
    const next = w.findAll('.ch__btn').find((b) => b.text() === '下一页')!
    expect(next.attributes('disabled')).toBeUndefined()
    await next.trigger('click')
    await flushPromises()
    expect(recMock.mock.calls[1]![0]).toMatchObject({ offset: 50 })
  })

  it('★★ 空 records ⇒ 空态文案（真的空）', async () => {
    recMock.mockResolvedValue(recResp({ records: [], total: 0 }))
    const w = await mountView()
    expect(w.text()).toContain('没有查到命中记录')
  })

  it('★★ 空 queue ⇒ 空态文案带当前状态', async () => {
    qMock.mockResolvedValue(queueResp({ items: [] }))
    const w = await mountView()
    expect(w.text()).toContain('没有状态为「待复核」的复核项')
  })
})

describe('队列可空列的降级', () => {
  it('★★★ session_key 缺失 ⇒ 显示「无」而不是空白', async () => {
    qMock.mockResolvedValue(queueResp({ items: [queueItem({ session_key: undefined })] }))
    const w = await mountView()
    expect(w.text()).toContain('会话 无')
  })

  it('★★ reviewer 缺失 ⇒ 不渲染「复核人」行', async () => {
    qMock.mockResolvedValue(queueResp({ items: [queueItem({ reviewer: undefined, review_comment: undefined })] }))
    const w = await mountView()
    expect(w.text()).not.toContain('复核人')
  })

  it('★★ reviewed_at 缺失 ⇒ 不渲染复核时间', async () => {
    qMock.mockResolvedValue(queueResp({ items: [queueItem({ reviewed_at: undefined })] }))
    const w = await mountView()
    expect(w.text()).not.toContain('复核于')
  })
})
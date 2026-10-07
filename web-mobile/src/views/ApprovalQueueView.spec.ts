import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ApprovalQueueView from './ApprovalQueueView.vue'
import { fetchApprovals, fetchApprovalStats } from '@/api/approvals'
import { setLocale, locale } from '@/i18n'

/**
 * ApprovalQueueView 的不变量（2026-10-07，第六十五批）。
 *
 * ★ 钉住的五处「不能都渲染成同一个东西」：
 *   ① **`total` 是本页条数**（approval_handler.go:348-350 `total := len(records)`
 *      而 records 已带 Limit/Offset）⇒ UI 显示「本页 N 条」，
 *      翻页**不能**用 `total_pages`（恒为 1）。
 *   ② **`risk_level`/`trigger_type` 空串 = 未检测**，不是「低风险」。
 *   ③ **pending + 无 time_left = 已逾期**，与「倒计时中」处置相反。
 *   ④ **状态筛选缺省是 pending**，不是「全部」⇒ 空列表要说「这一档没有」。
 *   ⑤ **统计端混两套口径**（区间 vs 今天）⇒ 必须分区说明。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/approvals', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/approvals')>()
  return { ...actual, fetchApprovals: vi.fn(), fetchApprovalStats: vi.fn() }
})

const listMock = fetchApprovals as unknown as ReturnType<typeof vi.fn>
const statsMock = fetchApprovalStats as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(ApprovalQueueView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  return w
}

type W = ReturnType<typeof mount>

function sectionOf(w: W, index: number) {
  return w.findAll('.aq__section')[index]!
}
function loadBtn(w: W, n: number) {
  return w.findAll('.aq__load')[n]!
}
async function clickLoad(w: W, n: number): Promise<void> {
  await loadBtn(w, n).trigger('click')
  await flushPromises()
  await flushPromises()
}

/** 按 `<dt>` 精确文本找行（互为前缀的标签会让子串匹配打错行）。 */
function rowByLabel(w: W, index: number, label: string) {
  const row = sectionOf(w, index).findAll('.aq__kv-row').find((r) => r.find('dt').text() === label)
  if (!row) throw new Error(`未找到标签为「${label}」的行`)
  return row
}

/* ── 夹具：逐字照抄后端 ─────────────────────────────────────────────── */

const ITEM_PENDING_LIVE = {
  id: 'a1', session_id: 's1', tenant_id: 'default', request_id: 'r1',
  status: 'pending', detect_result: { decision: 'HIGH', reason: 'risk_rule' },
  risk_level: 'HIGH', trigger_type: 'risk_rule',
  created_at: '2026-10-08T01:00:00Z', expires_at: '2026-10-08T02:00:00Z',
  time_left: '58m',
}

/** ★ 无检测结果 ⇒ risk_level/trigger_type 是**空串**。 */
const ITEM_NO_DETECT = {
  id: 'a2', session_id: 's2', tenant_id: 'default', request_id: 'r2',
  status: 'pending', detect_result: null,
  risk_level: '', trigger_type: '',
  created_at: '2026-10-08T01:00:00Z', expires_at: '2026-10-08T02:00:00Z',
}

const ITEM_APPROVED = {
  ...ITEM_PENDING_LIVE,
  status: 'approved',
  approved_by: 'alice',
  approved_at: '2026-10-08T01:30:00Z',
  reason: '看起来没问题',
}

/** ★★★ 未知 status（后端不校验，非法值直接进 filter）。 */
const ITEM_WEIRD_STATUS = { ...ITEM_PENDING_LIVE, id: 'a3', status: 'weird_state' }

const LIST_NORMAL = {
  items: [ITEM_PENDING_LIVE, ITEM_NO_DETECT, ITEM_APPROVED],
  total: 3,
  page: 1,
  page_size: 50,
  total_pages: 1,
}

const STATS_NORMAL = {
  total: 120, pending: 20, approved: 80, rejected: 15, timeout: 5,
  avg_approval_time_seconds: 342.5,
  by_risk_level: { HIGH: 30, LOW: 90 },
  by_trigger_type: { risk_rule: 40 },
  today_total: 7, today_pending: 2,
}

/** ★ 四个状态加起来 > total。 */
const STATS_DISAGREE = { ...STATS_NORMAL, pending: 50 }

/** ★ 分母为 0 ⇒ avg 是 Go 零值。 */
const STATS_NO_DECISION = {
  ...STATS_NORMAL, approved: 0, rejected: 0, pending: 120, avg_approval_time_seconds: 0,
}

beforeEach(() => {
  setLocale('zh-CN')
  listMock.mockReset()
  statsMock.mockReset()
})

afterEach(() => {
  mountedList.forEach((w) => w.unmount())
  mountedList = []
  setLocale(ORIGIN_LOCALE)
  document.body.innerHTML = ''
})

/* ═══════════════════════════════════════════════════════════════════════
 * ① total 是本页条数
 * ═══════════════════════════════════════════════════════════════════════ */

describe('ApprovalQueueView ① total 是本页条数', () => {
  it('★★★ 显示「本页 N 条」而不是「共 N 条」', async () => {
    listMock.mockResolvedValue(LIST_NORMAL)
    const w = await mountView()
    await clickLoad(w, 1)
    const txt = sectionOf(w, 1).text()
    expect(txt).toContain('本页返回 3 条')
    expect(txt).toContain('不给库里总数')
    // ★ 绝不能出现「共 3 条」这种把本页数当总数的措辞
    expect(txt).not.toContain('共 3 条')
  })

  it('★ total 与 items.length 不一致 ⇒ 报契约异常', async () => {
    listMock.mockResolvedValue({ ...LIST_NORMAL, total: 999 })
    const w = await mountView()
    await clickLoad(w, 1)
    const txt = sectionOf(w, 1).text()
    expect(txt).toContain('契约异常')
    expect(txt).toContain('999')
  })

  it('正常数据不报契约异常', async () => {
    listMock.mockResolvedValue(LIST_NORMAL)
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).text()).not.toContain('契约异常')
  })

  it('★★★ total_pages 恒为 1 时，翻页仍按「本页取满」判定', async () => {
    // total_pages=1 但 items 恰好取满 page_size ⇒ 必须说还有下一页
    const items = new Array(50).fill(ITEM_PENDING_LIVE).map((x, i) => ({ ...x, id: `a${i}` }))
    listMock.mockResolvedValue({ items, total: 50, page: 1, page_size: 50, total_pages: 1 })
    const w = await mountView()
    await clickLoad(w, 1)
    const btns = sectionOf(w, 1).findAll('.aq__pager-btn')
    expect(btns[0]!.attributes('disabled')).toBeDefined()   // 上一页：第一页禁用
    expect(btns[1]!.attributes('disabled')).toBeUndefined() // 下一页：取满 ⇒ 可用
  })

  it('本页没取满 ⇒ 下一页禁用', async () => {
    listMock.mockResolvedValue(LIST_NORMAL)
    const w = await mountView()
    await clickLoad(w, 1)
    const btns = sectionOf(w, 1).findAll('.aq__pager-btn')
    expect(btns[1]!.attributes('disabled')).toBeDefined()
  })

  it('★ 翻页把新 page 真发出去', async () => {
    const items = new Array(50).fill(ITEM_PENDING_LIVE).map((x, i) => ({ ...x, id: `a${i}` }))
    listMock.mockResolvedValue({ items, total: 50, page: 1, page_size: 50, total_pages: 1 })
    const w = await mountView()
    await clickLoad(w, 1)
    await sectionOf(w, 1).findAll('.aq__pager-btn')[1]!.trigger('click')
    await flushPromises()
    await flushPromises()
    expect((listMock.mock.calls[1]![0] as Record<string, unknown>).page).toBe(2)
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ② 空串 = 未检测
 * ═══════════════════════════════════════════════════════════════════════ */

describe('ApprovalQueueView ② 空串 = 未检测', () => {
  it('★★★ risk_level 空串渲染成「未检测」，不是「低风险」', async () => {
    listMock.mockResolvedValue(LIST_NORMAL)
    const w = await mountView()
    await clickLoad(w, 1)
    const sec = sectionOf(w, 1)
    const rows = sec.findAll('.aq__kv-row').filter((r) => r.find('dt').text() === '风险等级')
    expect(rows.length).toBe(3)
    // 第二行（无检测结果）显示「未检测」
    expect(rows[1]!.find('dd').element.textContent).toBe('未检测')
    expect(rows[1]!.find('dd').classes()).toContain('aq__nodata')
    // 第一行显示真实风险等级
    expect(rows[0]!.find('dd').element.textContent).toBe('HIGH')
    expect(rows[0]!.find('dd').classes()).not.toContain('aq__nodata')
    expect(sec.text()).not.toContain('低风险')
  })

  it('★ trigger_type 空串同样是「未检测」', async () => {
    listMock.mockResolvedValue(LIST_NORMAL)
    const w = await mountView()
    await clickLoad(w, 1)
    const rows = sectionOf(w, 1).findAll('.aq__kv-row').filter((r) => r.find('dt').text() === '触发原因')
    expect(rows[1]!.find('dd').element.textContent).toBe('未检测')
    expect(rows[0]!.find('dd').element.textContent).toBe('risk_rule')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ③ 倒计时 / 已逾期 / 已决定 三态互斥
 * ═══════════════════════════════════════════════════════════════════════ */

describe('ApprovalQueueView ③ 三态互斥', () => {
  it('★ pending + 有 time_left ⇒ 显示倒计时', async () => {
    listMock.mockResolvedValue(LIST_NORMAL)
    const w = await mountView()
    await clickLoad(w, 1)
    const row = rowByLabel(w, 1, '当前状态')
    expect(row.text()).toContain('58m')
  })

  it('★★★ pending + 无 time_left ⇒ 显示「已逾期」并带独立 class', async () => {
    listMock.mockResolvedValue(LIST_NORMAL)
    const w = await mountView()
    await clickLoad(w, 1)
    const rows = sectionOf(w, 1).findAll('.aq__kv-row').filter((r) => r.find('dt').text() === '当前状态')
    // 第二行是无检测结果的 pending，没有 time_left ⇒ 已逾期
    expect(rows[1]!.find('dd').element.textContent).toBe('已逾期')
    expect(rows[1]!.find('dd').classes()).toContain('aq__overdue')
  })

  it('★★ 有已逾期的待审批 ⇒ 单独提醒「还在挡着会话」', async () => {
    listMock.mockResolvedValue(LIST_NORMAL)
    const w = await mountView()
    await clickLoad(w, 1)
    const txt = sectionOf(w, 1).text()
    expect(txt).toContain('已逾期')
    expect(txt).toContain('挡着会话')
  })

  it('★ 无逾期时不出现该提醒', async () => {
    listMock.mockResolvedValue({ ...LIST_NORMAL, items: [ITEM_PENDING_LIVE, ITEM_APPROVED] })
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).text()).not.toContain('挡着会话')
  })

  it('★ 已审批 ⇒ 显示处理时间而不是倒计时', async () => {
    listMock.mockResolvedValue({ ...LIST_NORMAL, items: [ITEM_APPROVED] })
    const w = await mountView()
    await clickLoad(w, 1)
    const row = rowByLabel(w, 1, '当前状态')
    expect(row.text()).toContain('2026-10-08T01:30:00Z')
    expect(row.text()).not.toContain('58m')
  })

  it('★ 已审批但仍带 time_left ⇒ 不得说倒计时（判别样本）', async () => {
    // ★ 这是「三态互斥」的关键判别样本：两个状态字段同时存在时，
    //   倒计时判据必须以 status 为准（否则一条已处理的记录会显示倒计时）。
    listMock.mockResolvedValue({ ...LIST_NORMAL, items: [ITEM_APPROVED] })
    const w = await mountView()
    await clickLoad(w, 1)
    const row = rowByLabel(w, 1, '当前状态')
    // ★★★ 原先这里只断言「不是 overdue」—— 变异让 itemState 落到 'other'
    //   （渲染成「—」）时照样通过，等于这条判别样本**自己也漏**。
    //   ⇒ 必须正面断言：显示的是**处理时间**，且**不含倒计时字样**。
    expect(row.find('dd').element.textContent).toBe('2026-10-08T01:30:00Z')
    expect(row.find('dd').element.textContent).not.toContain('58m')
    expect(row.find('dd').classes()).not.toContain('aq__overdue')
    expect(row.find('dd').classes()).not.toContain('aq__nodata')
  })

  it('★ 未知 status 要说明而不是原样透传', async () => {
    listMock.mockResolvedValue({ ...LIST_NORMAL, items: [ITEM_WEIRD_STATUS] })
    const w = await mountView()
    await clickLoad(w, 1)
    const sec = sectionOf(w, 1)
    expect(sec.text()).toContain('未知状态')
    expect(sec.text()).toContain('weird_state')
    expect(sec.find('.aq__badge').classes()).toContain('aq__badge--unknown')
  })

  it('正常 status 不加 unknown class', async () => {
    listMock.mockResolvedValue({ ...LIST_NORMAL, items: [ITEM_PENDING_LIVE] })
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).find('.aq__badge').classes()).not.toContain('aq__badge--unknown')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ④ 状态筛选缺省 pending + 空列表措辞
 * ═══════════════════════════════════════════════════════════════════════ */

describe('ApprovalQueueView ④ 筛选与空列表', () => {
  it('★★ 缺省就是 pending（不是「全部」）', async () => {
    listMock.mockResolvedValue(LIST_NORMAL)
    const w = await mountView()
    await clickLoad(w, 1)
    const q = listMock.mock.calls[0]![0] as Record<string, unknown>
    expect(q.status).toBe('pending')
  })

  it('★ 空列表 ⇒ 说「这一档没有」而不是「一条都没有」', async () => {
    listMock.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 50, total_pages: 1 })
    const w = await mountView()
    await clickLoad(w, 1)
    const txt = sectionOf(w, 1).text()
    expect(txt).toContain('这一档当前没有记录')
    expect(txt).toContain('不是「一条都没有」')
  })

  it('★ 切到「已通过」后筛选真的发出去', async () => {
    listMock.mockResolvedValue(LIST_NORMAL)
    const w = await mountView()
    await sectionOf(w, 1).find('select')!.setValue('approved')
    await clickLoad(w, 1)
    expect((listMock.mock.calls[0]![0] as Record<string, unknown>).status).toBe('approved')
  })

  it('★ page_size 越界 ⇒ 回显后端实际生效值（回落 50）', async () => {
    listMock.mockResolvedValue(LIST_NORMAL)
    const w = await mountView()
    const sizeInput = sectionOf(w, 1).findAll('.aq__input')[1]!
    await sizeInput.setValue('999')
    await flushPromises()
    expect(sectionOf(w, 1).text()).toContain('每页 50 条')
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * ⑤ 统计端两套口径
 * ═══════════════════════════════════════════════════════════════════════ */

describe('ApprovalQueueView ⑤ 统计口径', () => {
  it('★★★ 必须写出「今天口径不受时间范围影响」', async () => {
    statsMock.mockResolvedValue(STATS_NORMAL)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('不受时间范围影响')
  })

  it('★ avg 分母为 0 ⇒ 说「无样本」而不是 0 秒', async () => {
    statsMock.mockResolvedValue(STATS_NO_DECISION)
    const w = await mountView()
    await clickLoad(w, 0)
    const row = rowByLabel(w, 0, '平均审批耗时')
    expect(row.find('dd').element.textContent).toBe('无样本（还没有已通过/已拒绝的记录）')
    expect(row.find('dd').classes()).toContain('aq__nodata')
  })

  it('有样本 ⇒ 显示真实耗时', async () => {
    statsMock.mockResolvedValue(STATS_NORMAL)
    const w = await mountView()
    await clickLoad(w, 0)
    const row = rowByLabel(w, 0, '平均审批耗时')
    expect(row.find('dd').element.textContent).toContain('342.5')
    expect(row.find('dd').element.textContent).toContain('秒')
  })

  it('★ 计数器矛盾 ⇒ 报警', async () => {
    statsMock.mockResolvedValue(STATS_DISAGREE)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).toContain('计数器对不上')
  })

  it('正常数据不报矛盾', async () => {
    statsMock.mockResolvedValue(STATS_NORMAL)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 0).text()).not.toContain('计数器对不上')
  })

  it('★ 风险分布渲染成 chip', async () => {
    statsMock.mockResolvedValue(STATS_NORMAL)
    const w = await mountView()
    await clickLoad(w, 0)
    const chips = sectionOf(w, 0).findAll('.aq__chip').map((c) => c.text())
    expect(chips.some((c) => c.includes('HIGH'))).toBe(true)
    expect(chips.some((c) => c.includes('risk_rule'))).toBe(true)
  })
})

/* ═══════════════════════════════════════════════════════════════════════
 * 加载与失败路径
 * ═══════════════════════════════════════════════════════════════════════ */

describe('ApprovalQueueView 加载与失败路径', () => {
  it('★ 失败 ⇒ 清掉旧数据', async () => {
    listMock.mockResolvedValueOnce(LIST_NORMAL)
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).findAll('.aq__item').length).toBe(3)

    listMock.mockRejectedValueOnce(new Error('boom'))
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).text()).toContain('boom')
    expect(sectionOf(w, 1).findAll('.aq__item').length).toBe(0)
  })

  it('加载成功后按钮切到「重新加载」', async () => {
    listMock.mockResolvedValue(LIST_NORMAL)
    const w = await mountView()
    expect(loadBtn(w, 1).text()).toBe('加载')
    await clickLoad(w, 1)
    expect(loadBtn(w, 1).text()).toBe('重新加载')
  })

  it('403 ⇒ 无权文案', async () => {
    listMock.mockRejectedValue(Object.assign(new Error('Forbidden'), { status: 403 }))
    const w = await mountView()
    await clickLoad(w, 1)
    expect(sectionOf(w, 1).text()).toContain('无权查看审批队列')
  })

  it('★ 两段互不串扰', async () => {
    statsMock.mockResolvedValue(STATS_NORMAL)
    const w = await mountView()
    await clickLoad(w, 0)
    expect(sectionOf(w, 1).findAll('.aq__item').length).toBe(0)
  })
})
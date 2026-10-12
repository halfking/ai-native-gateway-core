import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import ProbeQueueView from './ProbeQueueView.vue'
import { setLocale } from '@/i18n'

/**
 * 凭据探测三态队列回归护栏（2026-10-08，第一百零五批）。
 *
 * 缺陷背景（读 `api/probeTriStateTasks.ts` 文件头 + 复核
 * `admin/probe_dashboard.go:1757-1885` 得出）：
 *
 *  1. ★★★★★★ **`http_status` / `latency_ms` 的 `0` 是真值。**
 *     字段是 `*int` + omitempty，而 omitempty 对**指针**只在 nil 时省略
 *     ⇒ 数据库里的 `0` 会**原样出现**（`http_status: 0`）。
 *     ⇒ 用 `if (t.http_status)` 判「有没有测出状态码」会把 `0` 说成「没测到」。
 *     ★ 这是 `null ≠ 0` 家族的**指针变体**：不是「null 要与 0 区分」，
 *       而是「键不存在」与「键存在但值为 0」必须渲染成两个不同的东西。
 *
 *  2. ★★★★ **`status` 不是数据库的 status。** 六个 DB 值被压成三个
 *     （`ready`→`pending` / `running`→`in_flight` / 其余四值→`completed`+`outcome`），
 *     且原始值**只在 completed 行以 `outcome` 保留** ⇒
 *     pending / in_flight 行的原值被丢掉且不可恢复。
 *     ⇒ 界面上不得出现 `ready` / `running` 这种说法。
 *
 *  3. ★★★ **`outcome` ⇔ `status === 'completed'`**（`:1819` + omitempty），
 *     **`next_retry_at_ms` ⇔ `status === 'pending'`**（`:1822-1824`）——
 *     两条都**互斥且客户端可自验**。不一致是契约漂移，必须说出来而不是静默降级。
 *
 *  4. ★★★ **`count` 是本页长度，不是总数**（`:1879` 的 `len(tasks)`）⇒
 *     页满时只能说「可能还有下一页」，把 `count` 说成总数就是凭空造事实。
 *
 *  5. ★★ **SQL 的过滤按 DB 原值**（`q.status='ready'` / `='running'` /
 *     `IN ('success','failed','expired','cancelled')`），
 *     压缩发生在 Go 里 ⇒ **每行的 `status` 恒等于请求的那条腿**。
 *     响应还回显 `status` ⇒ 回显与行级各是一道可验的关口。
 *
 *  6. ★★ 供应商三键**全缺** = 未知（SQL 给 `''` 再被 omitempty 吃掉），
 *     **不是空串、不是 null**。
 */

const fFetch = vi.fn()

vi.mock('@/api/probeTriStateTasks', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/probeTriStateTasks')>()
  return { ...actual, fetchProbeTriStateTasks: (...a: unknown[]) => fFetch(...a) }
})

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

// ── 夹具：逐条照抄 probe_dashboard.go 的 json 形状 ────────────────────────────

/** pending 腿：有退避时间、供应商三键全缺（SQL 给 '' 再被 omitempty 吃掉）。 */
const TASK_PENDING = {
  id: 501,
  dedup_key: 'dq-pending-1',
  credential_id: 12,
  raw_model: 'gpt-4o',
  command: 'chat_completion',
  source: 'periodic',
  origin: 'scheduled',
  status: 'pending',
  attempt: 1,
  max_attempts: 3,
  priority: 10,
  created_at: '2026-10-08T01:00:00Z',
  updated_at: '2026-10-08T01:00:00Z',
  next_retry_at_ms: 1791500000000,
}

/** in_flight 腿：★ 没有 next_retry_at_ms（按契约只有 pending 腿有）。 */
const TASK_INFLIGHT = {
  id: 502,
  dedup_key: 'dq-flight-1',
  credential_id: 13,
  raw_model: 'claude-opus-5',
  command: 'chat_completion',
  source: 'admin',
  origin: 'manual',
  status: 'in_flight',
  attempt: 2,
  max_attempts: 3,
  priority: 20,
  created_at: '2026-10-08T01:30:00Z',
  updated_at: '2026-10-08T01:31:00Z',
}

/** ★★★ completed + `http_status: 0` + `latency_ms: 0` —— 本批最要紧的那一格。 */
const TASK_COMPLETED_ZERO = {
  id: 503,
  dedup_key: 'dq-done-1',
  credential_id: 14,
  raw_model: 'qwen-max',
  command: 'chat_completion',
  source: 'request_failure',
  origin: 'error',
  status: 'completed',
  attempt: 1,
  max_attempts: 3,
  priority: 5,
  created_at: '2026-10-08T00:00:00Z',
  updated_at: '2026-10-08T02:00:00Z',
  outcome: 'failed',
  reason_code: 'rate_limited',
  http_status: 0,
  latency_ms: 0,
  finished_at: '2026-10-08T02:00:00Z',
}

/** completed 但**两个键都不存在**（`sql.NullInt32` 无效 ⇒ 不写指针）。 */
/**
 * ★★★ 这个夹具**必须真的没有** `http_status` / `latency_ms` 两个键。
 * 第一版我给它填了 `http_status: 200, latency_ms: 1234` 却仍叫它 `NO_NUM`，
 * 于是「键不存在」那一格**从来没被测过**——
 * 名字与内容不符的夹具比没有夹具更坏：它让「两个方向都测了」这句话变成假的。
 */
const TASK_COMPLETED_NO_NUM = {
  id: 504,
  dedup_key: 'dq-done-2',
  credential_id: 15,
  raw_model: 'glm-5.3',
  command: 'chat_completion',
  source: 'periodic',
  origin: 'scheduled',
  status: 'completed',
  attempt: 1,
  max_attempts: 3,
  priority: 1,
  created_at: '2026-10-08T00:00:00Z',
  updated_at: '2026-10-08T02:00:00Z',
  outcome: 'success',
  provider_id: 7,
  provider_name: 'OpenAI',
  provider_code: 'openai',
  finished_at: '2026-10-08T02:00:00Z',
}

/** ★ 同一腿的**另一格**：两个键都在，且都不是 0。 */
const TASK_COMPLETED_WITH_NUM = {
  ...TASK_COMPLETED_NO_NUM,
  id: 506,
  http_status: 200,
  latency_ms: 1234,
}

/** ★ 重试用尽且未进终态。 */
const TASK_EXHAUSTED = { ...TASK_PENDING, id: 505, attempt: 3, max_attempts: 3 }

const resp = (status: string, tasks: unknown[]) => ({ status, tasks, count: tasks.length })

/**
 * ★★ 桩必须**按请求的腿**回数。
 * 视图挂载时固定请求 `pending` 腿，而响应回显的 `status` 必须与之相同 ——
 * 若桩固定回 `completed`，视图里那道 `echoMismatch` 守卫就会（正确地）拦下全部数据，
 * 于是十条断言全部红在一个**与被测点无关**的原因上。
 * ⇒ 第一版正是这么写的：11 条红全是判据自己的桩错了，不是视图坏了。
 */
const LEG_FIXTURES: Record<string, unknown[]> = {
  pending: [TASK_PENDING],
  in_flight: [TASK_INFLIGHT],
  completed: [TASK_COMPLETED_ZERO],
}

function serveLegs(over: Partial<Record<string, unknown[]>> = {}): void {
  const m = { ...LEG_FIXTURES, ...over }
  fFetch.mockImplementation((status: string) => Promise.resolve(resp(status, m[status] ?? [])))
}

beforeEach(() => {
  setLocale('zh-CN')
  fFetch.mockReset()
  serveLegs()
})

/**
 * ★ 挂载后默认停在 `pending` 腿。
 *   测 completed / in_flight 的用例**必须显式切腿** ——
 *   只把桩改成那条腿的数据而不点，界面仍然显示 pending 腿的内容，
 *   断言会红在一个与被测点无关的原因上（第一版就是这么错的）。
 */
async function mountView(legLabel?: string) {
  const w = mount(ProbeQueueView, { attachTo: document.body })
  await flushPromises()
  await flushPromises()
  if (legLabel) await clickLeg(w, legLabel)
  return w
}

/** 切腿（点分段按钮）。 */
async function clickLeg(w: ReturnType<typeof mount>, label: string) {
  const btn = w.findAll('.pq__leg').find((b) => b.text() === label)!
  await btn.trigger('click')
  await flushPromises()
  await flushPromises()
}

// ═══════════════════════════════════════════════════════════════════════════
describe('invariant 1 · http_status / latency 的 0 是真值', () => {
  it('★★★ http_status = 0 时显示 0', async () => {
    serveLegs({ completed: [TASK_COMPLETED_ZERO] })
    const w = await mountView('已完成')
    const txt = w.text()
    expect(txt).toContain('HTTP 状态码 0')
  })

  it('★★★★ http_status = 0 时**绝不**说「未测出状态码」', async () => {
    serveLegs({ completed: [TASK_COMPLETED_ZERO] })
    const w = await mountView('已完成')
    // ★ 判别方向：两个键都不存在的夹具（TASK_COMPLETED_NO_NUM）才该出现这句。
    expect(w.text()).not.toContain('未测出状态码')
  })

  it('★★ 键真的不存在时显示「未测出状态码」（区分格方向相反）', async () => {
    serveLegs({ completed: [TASK_COMPLETED_NO_NUM] })
    const w = await mountView('已完成')
    const txt = w.text()
    expect(txt).toContain('未测出状态码')
    expect(txt).toContain('未记录耗时')
    // ★ 判别方向：另一格（有值）才该出现 200。
    expect(txt).not.toContain('HTTP 状态码 200')
  })

  it('★★★ latency_ms = 0 时显示 0 ms', async () => {
    serveLegs({ completed: [TASK_COMPLETED_ZERO] })
    const w = await mountView('已完成')
    expect(w.text()).toContain('耗时 0 ms')
  })

  it('★★ latency 键不存在时显示「未记录耗时」', async () => {
    // ★ 混合格：两个键**一个有值一个没有** —— 两条校验必须彼此独立，
    //   否则「有 http_status 就当两者都有」的实现会整条糊过去。
    serveLegs({ completed: [TASK_COMPLETED_WITH_NUM, TASK_COMPLETED_NO_NUM] })
    const w = await mountView('已完成')
    const txt = w.text()
    expect(txt).toContain('HTTP 状态码 200')
    expect(txt).toContain('未记录耗时')
  })

  it('★★ 两个键都存在时两句「未…」都不出现', async () => {
    serveLegs({ completed: [TASK_COMPLETED_WITH_NUM] })
    const w = await mountView('已完成')
    expect(w.text()).not.toContain('未测出状态码')
    expect(w.text()).not.toContain('未记录耗时')
    expect(w.text()).toContain('耗时 1234 ms')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('invariant 2 · status 是压缩后的三值，DB 原值不可出现', () => {
  it('★★ 界面上不出现 DB 原值 ready / running', async () => {
    serveLegs({ pending: [TASK_PENDING] })
    const w = await mountView()
    const txt = w.text()
    expect(txt).not.toContain('ready')
    expect(txt).not.toContain('running')
  })

  it('★ 徽章显示的是压缩后的三值', async () => {
    serveLegs({ pending: [TASK_PENDING] })
    const w = await mountView()
    expect(w.text()).toContain('pending')
  })

  it('★ completed 行的 outcome 以 outcome 的名字呈现（不是被压掉的 DB status）', async () => {
    serveLegs({ completed: [TASK_COMPLETED_ZERO] })
    const w = await mountView('已完成')
    expect(w.text()).toContain('失败')
    expect(w.text()).not.toContain('success')
  })

  it('★ 四个 outcome 各自的文案互不相同', async () => {
    const mk = (o: string, id: number) => ({
      ...TASK_COMPLETED_NO_NUM,
      id,
      outcome: o,
      provider_id: undefined,
      provider_name: undefined,
      provider_code: undefined,
    })
    serveLegs({ completed: [mk('success', 1), mk('failed', 2), mk('expired', 3), mk('cancelled', 4)] })
    const w = await mountView('已完成')
    const txt = w.text()
    for (const s of ['成功', '失败', '已过期', '已取消']) expect(txt).toContain(s)
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('invariant 3 · outcome ⇔ completed 与 next_retry ⇔ pending', () => {
  it('★ pending 行渲染退避时间', async () => {
    const w = await mountView()
    expect(w.text()).toContain('下次重试')
    expect(w.text()).toContain('2026-10-08')
  })

  it('★★ in_flight 行**不**渲染退避时间', async () => {
    serveLegs({ in_flight: [TASK_INFLIGHT] })
    const w = await mountView('在途')
    expect(w.text()).not.toContain('下次重试')
  })

  it('★★ in_flight 行**不得**出现 outcome（互斥的另一向）', async () => {
    serveLegs({ in_flight: [TASK_INFLIGHT] })
    const w = await mountView('在途')
    expect(w.text()).not.toContain('结果')
  })

  it('★★★ completed 行缺 outcome 时给出契约漂移告警，而不是静默说「无结果」', async () => {
    const bad = { ...TASK_COMPLETED_NO_NUM }
    delete (bad as Record<string, unknown>)['outcome']
    serveLegs({ completed: [bad] })
    const w = await mountView('已完成')
    expect(w.text()).toContain('outcome 只应出现在 completed 腿')
  })

  it('★★★ pending 行带 outcome 时同样告警（反向那一格）', async () => {
    serveLegs({ pending: [{ ...TASK_PENDING, outcome: 'failed' }] })
    const w = await mountView()
    expect(w.text()).toContain('outcome 只应出现在 completed 腿')
  })

  it('★★★ in_flight 行带 next_retry_at_ms 时告警（存在性 ⇔ pending 的反向）', async () => {
    serveLegs({ in_flight: [{ ...TASK_INFLIGHT, next_retry_at_ms: 1791500000000 }] })
    const w = await mountView('在途')
    expect(w.text()).toContain('next_retry_at_ms 只应出现在 pending 腿')
  })

  it('★ pending 行缺 next_retry_at_ms 时给「无退避时间」而不是崩掉', async () => {
    const bad = { ...TASK_PENDING }
    delete (bad as Record<string, unknown>)['next_retry_at_ms']
    serveLegs({ pending: [bad] })
    const w = await mountView()
    expect(w.text()).toContain('无退避时间')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('invariant 4 · count 是本页长度不是总数', () => {
  it('★ count 只作为「本页 N 条」呈现', async () => {
    const w = await mountView()
    expect(w.text()).toContain('本页 1 条')
  })

  it('★★★ 页满时披露「可能还有下一页」', async () => {
    const many = Array.from({ length: 50 }, (_, i) => ({ ...TASK_PENDING, id: 900 + i }))
    fFetch.mockResolvedValue(resp('pending', many))
    const w = await mountView()
    const txt = w.text()
    expect(txt).toContain('本页取满')
    expect(txt).toContain('可能还有下一页')
  })

  it('★★ 页满披露必须说清 count 不是总数', async () => {
    const many = Array.from({ length: 50 }, (_, i) => ({ ...TASK_PENDING, id: 900 + i }))
    fFetch.mockResolvedValue(resp('pending', many))
    const w = await mountView()
    expect(w.text()).toContain('count 只是本页长度，不是总数')
  })

  it('★★ 页未满时**不**出现「可能还有下一页」（区分格方向相反）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('可能还有下一页')
  })

  it('★ 换每页条数后页满判定跟着变（100 条上限时 50 条不算满）', async () => {
    const many = Array.from({ length: 50 }, (_, i) => ({ ...TASK_PENDING, id: 900 + i }))
    fFetch.mockResolvedValue(resp('pending', many))
    const w = await mountView()
    const btn = w.findAll('.pq__leg').find((b) => b.text() === '100')!
    await btn.trigger('click')
    await flushPromises()
    await flushPromises()
    expect(w.text()).not.toContain('本页取满')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('invariant 5 · 回显与行级 status 各是一道关口', () => {
  it('★★ 回显与请求的腿不一致时给告警', async () => {
    // ★ 故意让回显与请求的腿不符 ⇒ 必须绕过 serveLegs，直接钉一个响应。
    fFetch.mockResolvedValue(resp('completed', [TASK_PENDING]))
    const w = await mountView()
    expect(w.text()).toContain('响应回显的 status 与请求的段不一致')
  })

  it('★★★ 回显不对时**不把别的腿的数据当这一腿渲染**', async () => {
    // ★★★ 第一版这条是**恒真判据**，两个独立缺陷叠在一起：
    //   ① 用 serveLegs 按腿供数 ⇒ 请求 pending 就回 pending，**回显根本不相符**，
    //      被守卫的分支从未被触发；
    //   ② 断言的 `dq-pending-1` 是 dedup_key，**视图根本不渲染它** ——
    //      `not.toContain(X)` 在 X 从来不会被渲染时**恒为真**。
    //   ⇒ 加了守卫也绿、去掉守卫也绿。它证明的不是「守卫有用」，是「我没在测」。
    fFetch.mockResolvedValue(resp('completed', [TASK_PENDING]))
    const w = await mountView()
    const txt = w.text()
    expect(txt).toContain('响应回显的 status 与请求的段不一致')
    expect(txt).not.toContain('gpt-4o')
  })

  it('★★★★ 正控：回显相符时同一个字符串**确实会**被渲染（否则上面那条是空的）', async () => {
    // ★ 负断言只有在「被否的东西本来会出现」时才有意义。
    //   这一条就是那个「本来会出现」的证明。
    serveLegs({ pending: [TASK_PENDING] })
    const w = await mountView()
    const txt = w.text()
    expect(txt).toContain('gpt-4o')
    expect(txt).not.toContain('响应回显的 status')
  })

  it('★★ 回显对但行级 status 不对时也给告警', async () => {
    serveLegs({ pending: [TASK_INFLIGHT] })
    const w = await mountView()
    expect(w.text()).toContain('有行的 status 与当前段不一致')
  })

  it('★ 两者都对时不出现任何漂移告警', async () => {
    const w = await mountView()
    const txt = w.text()
    expect(txt).not.toContain('响应回显的 status')
    expect(txt).not.toContain('有行的 status')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('invariant 6 · 供应商三键全缺 = 未知', () => {
  it('★★ 三键全缺时显示「未知」', async () => {
    const w = await mountView()
    expect(w.text()).toContain('供应商 未知')
  })

  it('★ 有 provider_name 时显示名字而不是「未知」', async () => {
    serveLegs({ completed: [TASK_COMPLETED_NO_NUM] })
    const w = await mountView('已完成')
    expect(w.text()).toContain('供应商 OpenAI')
    expect(w.text()).not.toContain('供应商 未知')
  })

  it('★★ 只有 provider_code 时用 code 兜底', async () => {
    serveLegs({ pending: [{ ...TASK_PENDING, provider_code: 'anthropic' }] })
    const w = await mountView()
    expect(w.text()).toContain('供应商 anthropic')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('取数纪律 · 三条腿各自独立请求', () => {
  it('★ 挂载只请求一次，且是默认的 pending 腿', async () => {
    await mountView()
    expect(fFetch).toHaveBeenCalledTimes(1)
    expect(fFetch).toHaveBeenCalledWith('pending', 50)
  })

  it('★ 不预取另外两条腿', async () => {
    await mountView()
    const legs = fFetch.mock.calls.map((c) => c[0])
    expect(legs).toEqual(['pending'])
  })

  it('★ 切腿只请求那一条', async () => {
    serveLegs({ in_flight: [TASK_INFLIGHT] })
    const w = await mountView('在途')
    await clickLeg(w, '在途')
    expect(fFetch).toHaveBeenLastCalledWith('in_flight', 50)
  })

  it('★★ limit 只发合法值（后端对越界直接 400，不静默回落）', async () => {
    const w = await mountView()
    for (const label of ['100', '200']) {
      const btn = w.findAll('.pq__leg').find((b) => b.text() === label)!
      await btn.trigger('click')
      await flushPromises()
      await flushPromises()
    }
    const limits = fFetch.mock.calls.map((c) => c[1])
    for (const l of limits) {
      expect(l).toBeGreaterThanOrEqual(1)
      expect(l).toBeLessThanOrEqual(200)
    }
    expect(limits).toContain(200)
  })

  it('★★ 失败时清空本页，绝不保留上一次的成功结果冒充本次', async () => {
    serveLegs({ pending: [TASK_PENDING] })
    const w = await mountView()
    expect(w.text()).toContain('gpt-4o')
    fFetch.mockRejectedValue(new Error('503 database not configured'))
    await clickLeg(w, '待处理')
    const txt = w.text()
    expect(txt).toContain('database not configured')
    expect(txt).not.toContain('gpt-4o')
  })
})

// ═══════════════════════════════════════════════════════════════════════════
describe('渲染细节', () => {
  it('★ 重试用尽且未进终态时告警', async () => {
    serveLegs({ pending: [TASK_EXHAUSTED] })
    const w = await mountView()
    expect(w.text()).toContain('重试已用尽')
  })

  it('★ 未用尽时不出现该告警', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('重试已用尽')
  })

  it('★ origin 与 source 不一致时告警并给出应然值', async () => {
    serveLegs({ pending: [{ ...TASK_PENDING, origin: 'manual' }] })
    const w = await mountView()
    const txt = w.text()
    expect(txt).toContain('origin 与 source 不一致')
    expect(txt).toContain('scheduled')
  })

  it('★ origin 一致时不告警', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('origin 与 source 不一致')
  })

  it('★ 空段给出带段名的空态', async () => {
    serveLegs({ pending: [] })
    const w = await mountView()
    expect(w.text()).toContain('「待处理」段没有任务')
  })

  it('★ 凭据号与模型名都渲染', async () => {
    const w = await mountView()
    const txt = w.text()
    expect(txt).toContain('凭据 12')
    expect(txt).toContain('gpt-4o')
  })
})
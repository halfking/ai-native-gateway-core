import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, getActivePinia, setActivePinia } from 'pinia'
import NodesView from './NodesView.vue'
import type { CredentialMonitorSummary } from '@/api/nodes'

/**
 * 回归护栏：凭据卡的**状态点色调**（2026-10-08）。
 *
 * 缺陷背景（线上 API 实测，不是推测）：
 *   245 上 /api/credentials/monitor-summary?mode=core 的 65 条凭据，
 *   availability_state 的真实分布是
 *       ready 40 / auth_failed 20 / unreachable 2 / suspended 2 / cooling 1
 *   而 healthTone 原先**只分支 down / degraded 两个值**，其余全部落到末尾的
 *   `return 'success'`。实测结果：
 *       25 条走 success 全绿，其中 10 条是明确的 auth_failed 或 suspended
 *   ⇒ **正在鉴权失败的凭据被渲染成「一切正常」的绿点。**
 *
 * 这是「枚举 == 分支」的缺口形态：枚举有 5 个在用成员，分支只认 2 个。
 * 日后再新增任何一个状态，同样是静默全绿。
 *
 * 本组用例把三件事分别钉住，缺一不可：
 *   ① 每个**线上实测出现过**的状态都有明确映射（不是只钉 down/degraded）；
 *   ② 收尾 fail-closed —— 未列进白名单的状态拿不到绿色；
 *      ← 只钉①的话，把收尾换回裸 `return 'success'` 照样全绿。
 *   ③ 缺 broken_model_count 的凭据不因「按 0 处理」而拿到不该有的结论。
 *      （该字段 65 条里缺 25 条）
 */

/** 每个用例注入的凭据列表；由下方 mock 读取。 */
let injected: CredentialMonitorSummary[] = []

vi.mock('@/api/nodes', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/nodes')>()
  return {
    ...actual,
    fetchMonitorSummary: vi.fn(async () => injected.map((c) => ({ ...c }))),
  }
})

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

function cred(over: Partial<CredentialMonitorSummary>): CredentialMonitorSummary {
  return {
    id: 1,
    provider_id: 1,
    provider_name: 'p',
    label: 'cred',
    status: 'ok',
    availability_state: 'ready',
    health_status: 'healthy',
    quota_state: 'ok',
    effective_concurrency: 5,
    concurrency_limit: 5,
    manual_disabled: false,
    consecutive_failures: 0,
    state_reason_code: null,
    state_reason_detail: null,
    health_checked_at: '2026-10-08T00:00:00+08:00',
    total_requests: 0,
    broken_model_count: 0,
    ...over,
  } as CredentialMonitorSummary
}

/** 只取色调类名，避免把「有 status-dot--danger」和「有 danger」混为一谈。 */
function toneOf(cardEl: Element): string {
  const dot = cardEl.querySelector('.status-dot')
  if (!dot) throw new Error('卡片里没有 status-dot')
  const cls = Array.from(dot.classList).find((c) => c.startsWith('status-dot--'))
  if (!cls) throw new Error(`status-dot 上没有 status-dot--* 类：${dot.className}`)
  return cls.replace('status-dot--', '')
}

async function mountView() {
  const pinia = (getActivePinia() as ReturnType<typeof createPinia> | undefined) ?? createPinia()
  if (!getActivePinia()) setActivePinia(pinia)
  const w = mount(NodesView, { attachTo: document.body, global: { plugins: [pinia] } })
  await flushPromises()
  await flushPromises()
  return w
}

// ★ 只在这里做一次包装。第一版让 toneFor 内部再调 cred(over)，于是「删掉某个键
//   再传进来」的用例被默认值悄悄填回 —— 那条断言红着是**测试自己的 bug**，
//   不是产品的。要构造缺键的形态必须走 toneForCredential 直接传成品。
async function toneForCredential(c: CredentialMonitorSummary): Promise<string> {
  injected = [c]
  const w = await mountView()
  const card = w.findAll('.node-card')[0]
  if (!card) throw new Error('没有渲染出 node-card')
  return toneOf(card.element)
}

function toneFor(over: Partial<CredentialMonitorSummary>): Promise<string> {
  return toneForCredential(cred(over))
}

describe('healthTone：线上实测出现的每个状态都要有明确映射', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    injected = []
    vi.clearAllMocks()
  })

  // 245 的 65 条凭据里实际出现过的 5 个 availability_state，一个不漏。
  it('auth_failed ⇒ danger（正在鉴权失败，绝不能是绿点）', async () => {
    expect(await toneFor({ availability_state: 'auth_failed', health_status: 'unreachable' })).toBe('danger')
  })

  it('unreachable ⇒ danger', async () => {
    expect(await toneFor({ availability_state: 'unreachable', health_status: 'unreachable' })).toBe('danger')
  })

  it('cooling ⇒ warning', async () => {
    expect(await toneFor({ availability_state: 'cooling' })).toBe('warning')
  })

  it('suspended ⇒ warning（不是 muted：muted 留给用户主动停用）', async () => {
    expect(await toneFor({ availability_state: 'suspended', health_status: 'healthy' })).toBe('warning')
  })

  it('ready + healthy ⇒ success（正常的仍然必须给绿，别一起压暗）', async () => {
    expect(await toneFor({ availability_state: 'ready', health_status: 'healthy' })).toBe('success')
  })

  it('down ⇒ danger（原有语义未被回归掉）', async () => {
    expect(await toneFor({ availability_state: 'down', health_status: 'down' })).toBe('danger')
  })

  it('manual_disabled ⇒ muted（用户主动停用，优先级最高）', async () => {
    expect(await toneFor({ manual_disabled: true, availability_state: 'auth_failed' })).toBe('muted')
  })

  it('consecutive_failures > 0 ⇒ warning', async () => {
    expect(await toneFor({ consecutive_failures: 3 })).toBe('warning')
  })

  it('broken_model_count > 0 ⇒ warning', async () => {
    expect(await toneFor({ broken_model_count: 2 })).toBe('warning')
  })

  // ★ 判别格：状态干净、但 broken_model_count **不存在**（65 条里缺 25 条）。
  //   缺失不能被当成「0 个 broken」⇒ 至少不得因此拿到 success 之外的结论；
  //   这里钉的是「缺失时仍然 success 是可以的，因为其余字段都健康」，
  //   真正的红线在下一条：缺失**不得**被当成「有 broken」。
  it('缺 broken_model_count 不会凭空造出 warning（缺失 ≠ 有 broken）', async () => {
    const c = cred({ availability_state: 'ready', health_status: 'healthy' })
    delete (c as unknown as Record<string, unknown>).broken_model_count
    expect(await toneForCredential(c)).toBe('success')
  })
})

describe('healthTone：收尾必须 fail-closed', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    injected = []
    vi.clearAllMocks()
  })

  // 这条是本组的关键：枚举里**没出现过的**新状态也不能拿到绿色。
  // 它把「新增一个状态」从静默全绿变成自动降级。
  it('未知 availability_state ⇒ 不得是 success', async () => {
    expect(await toneFor({ availability_state: 'brand_new_state' as never })).not.toBe('success')
  })

  it('health_status=unknown 而状态未列 ⇒ 不得是 success', async () => {
    expect(await toneFor({ availability_state: 'ready', health_status: 'unknown' })).not.toBe('success')
  })

  it('availability_state 整体缺失 ⇒ 不得是 success', async () => {
    const c = cred({ health_status: 'healthy' })
    delete (c as unknown as Record<string, unknown>).availability_state
    expect(await toneForCredential(c)).not.toBe('success')
  })
})
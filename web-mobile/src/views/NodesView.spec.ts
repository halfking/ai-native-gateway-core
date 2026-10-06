import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, getActivePinia, setActivePinia } from 'pinia'
import NodesView from './NodesView.vue'
import type { CredentialMonitorSummary } from '@/api/nodes'
import { useAuthStore } from '@/stores/auth'

/**
 * 回归护栏：凭据卡**不能**把缺失的模型计数渲染成字面量 "undefined"（2026-10-06）。
 *
 * 缺陷背景（模拟器实测 + 线上 API 核对）：
 *   /api/credentials/monitor-summary 的 model_available / model_total 对一部分
 *   凭据是**整个键不存在**（不是 null）。245 上 mode=core 的 65 条里：
 *       58 条是 int，7 条缺失（canary-cred-A / canary-cred-B 等，
 *       状态 auth_failed、14h 未检查）
 *   模板原先无条件 `{{ t('nodes.modelsAvailable', {available, total}) }}`，
 *   于是那 7 张卡在真机上渲染出：
 *       小米大模型 · canary-cred-A
 *       Models undefined/undefined   Concurrency 5
 *
 * 这里钉两条，缺一不可：
 *   ① 有计数 ⇒ 仍渲染 "Models 8/8"（别把正常的也一起藏了）
 *   ② 无计数 ⇒ 整段不出现，且**整页文本里不含 "undefined"**
 *      ——只钉①会让「全都藏起来」这种过度修法蒙混过关。
 */

const WITH_COUNTS = {
  id: 9,
  provider_id: 3,
  provider_name: '小米大模型',
  label: 'xiaomi-token-plan',
  status: 'ok',
  availability_state: 'ready',
  health_status: 'healthy',
  quota_state: 'ok',
  effective_concurrency: 20,
  concurrency_limit: 20,
  manual_disabled: false,
  consecutive_failures: 0,
  state_reason_code: null,
  state_reason_detail: null,
  health_checked_at: '2026-10-06T04:28:36+08:00',
  total_requests: 0,
  model_total: 8,
  model_available: 8,
  broken_model_count: 0,
} satisfies CredentialMonitorSummary

// 现场实测形态：这两个键**根本不存在**（不是 null）。
const WITHOUT_COUNTS = {
  id: 12,
  provider_id: 3,
  provider_name: '小米大模型',
  label: 'canary-cred-A',
  status: 'auth_failed',
  availability_state: 'down',
  health_status: 'down',
  quota_state: 'unknown',
  effective_concurrency: 5,
  concurrency_limit: 5,
  manual_disabled: false,
  consecutive_failures: 3,
  state_reason_code: 'auth_failed',
  state_reason_detail: 'invalid api key',
  health_checked_at: '2026-10-05T15:02:00+08:00',
  total_requests: 0,
  broken_model_count: 0,
} as CredentialMonitorSummary

vi.mock('@/api/nodes', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/nodes')>()
  return {
    ...actual,
    fetchMonitorSummary: vi.fn(async () => [{ ...WITH_COUNTS }, { ...WITHOUT_COUNTS }]),
  }
})

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/credentialsOps', () => ({
  setManualDisabled: vi.fn(async () => ({ success: true, message: 'ok' })),
  clearManualDisabled: vi.fn(async () => ({ success: true, message: 'ok' })),
  submitCredentialProbe: vi.fn(async () => ({ message: 'queued', credential_id: 9, status: 'pending' })),
  forceRecoverCredential: vi.fn(async () => ({
    triggered: true, credential_id: 9, timestamp: 't', message: 'm',
    key_cache_invalidated: true, key_rotator_reset: true,
  })),
  // 2026-10-06：详情 Sheet 打开时会拉「近期路由决策」。不 mock 它的话
  // 模块整个是 undefined ⇒ 这一区静默不渲染，测试全绿却零覆盖。
  fetchCredentialDecisions: vi.fn(async () => []),
}))

/**
 * 设置当前登录角色 —— 操作区按 role 分档渲染（后端 superAdmin 对 tenant_admin 直接 403）。
 * 返回值即被 setActivePinia 选中的那个实例，mountView 会复用它。
 */
function activatePiniaAs(role: string) {
  const pinia = createPinia()
  setActivePinia(pinia)
  const store = useAuthStore()
  store.userInfo = {
    id: 1, tenant_id: 't1', username: 'ops', display_name: 'Ops',
    email: 'ops@x', role, enabled: true,
  }
  return pinia
}

async function mountView() {
  // 2026-10-06：视图里新增了 useAuthStore()，测试必须先建 Pinia 实例，
  // 否则 setup 阶段就抛 "no active pinia"，整份 NodesView.spec 全红。
  // ★ 复用调用方已 setActivePinia 的那个实例（mountView 若自己新建，会把
  //   loginAs 写进去的 role 冲掉 ⇒ 权限分档断言会假绿/假红）。
  const pinia = (getActivePinia() as ReturnType<typeof createPinia> | undefined) ?? createPinia()
  if (!getActivePinia()) setActivePinia(pinia)
  const w = mount(NodesView, { attachTo: document.body, global: { plugins: [pinia] } })
  await flushPromises()
  await flushPromises()
  return w
}

describe('NodesView 凭据卡的模型计数', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('有计数的凭据照常渲染 "Models 8/8"', async () => {
    const w = await mountView()
    const cards = w.findAll('.node-card')
    expect(cards.length).toBe(2)
    expect(cards[0]?.text()).toContain('8/8')
  })

  it('计数缺失的凭据整段不渲染，且全页不出现 "undefined"', async () => {
    const w = await mountView()
    const cards = w.findAll('.node-card')
    expect(cards.length).toBe(2)

    // 缺计数那张：标签仍在，但它不该带 Models 那一段
    const noCounts = cards[1]
    if (!noCounts) throw new Error('node-card[1] 不存在')
    expect(noCounts.text()).toContain('canary-cred-A')
    expect(noCounts.text()).not.toContain('Models')

    // ★ 关键判据：整页文本里不得出现字面量 undefined
    const whole = w.text()
    expect(whole).not.toContain('undefined')
    expect(whole).not.toContain('null')
  })
})

/**
 * 运维操作区（17 §2 desktopOnly 让位轮，2026-10-06）。
 *
 * 钉的是**契约**，不是渲染细节：
 *   ① 权限分档：探测/强恢走 h.superAdmin，tenant_admin 点下去必定 403
 *      （后端 admin/handler.go:880-888 注释明写 tenant_admin 对 /api/admin/** 403）
 *      ⇒ 非 super_admin 时**不该**看到这两个按钮，也不能只靠后端报错来兜。
 *   ② reason 必填：set/clear-manual-disabled 对空串直接 400
 *      （admin/credential_monitor.go:1785-1792）⇒ 提交必须带上非空 reason。
 *   ③ 探测语义：后端 202 异步返回「已提交」不是「探测通过」
 *      （admin/credential_state_handlers.go:30-51）⇒ 文案不能说成通过。
 */
describe('NodesView 运维操作区', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  it('tenant_admin 看不到探测与强制恢复按钮，只保留停用/启用', async () => {
    activatePiniaAs('tenant_admin')
    const w = await mountView()
    await w.findAll('.node-card')[0]?.trigger('click')
    await flushPromises()

    const ops = document.body.querySelector('.nodes__ops')
    if (!ops) throw new Error('操作区未渲染')
    expect(ops.textContent).toContain('Disable')
    expect(ops.textContent).not.toContain('Probe now')
    expect(ops.textContent).not.toContain('Force recover')
  })

  it('super_admin 能看到全部四个动作入口', async () => {
    activatePiniaAs('super_admin')
    const w = await mountView()
    await w.findAll('.node-card')[0]?.trigger('click')
    await flushPromises()

    const ops = document.body.querySelector('.nodes__ops')
    if (!ops) throw new Error('操作区未渲染')
    expect(ops.textContent).toContain('Disable')
    expect(ops.textContent).toContain('Probe now')
    expect(ops.textContent).toContain('Force recover')
  })

  it('停用走 set-manual-disabled 且带非空 reason（空 reason 后端必 400）', async () => {
    const { setManualDisabled } = await import('@/api/credentialsOps')
    activatePiniaAs('super_admin')
    const w = await mountView()
    await w.findAll('.node-card')[0]?.trigger('click')
    await flushPromises()

    // 打开确认框 → 确认
    const disableBtn = Array.from(document.body.querySelectorAll('.nodes__ops .btn')).find((b) => (b.textContent ?? '').includes('Disable'))
    if (!disableBtn) throw new Error('停用按钮不存在')
    disableBtn.dispatchEvent(new Event('click'))
    await flushPromises()

    const confirmBtn = Array.from(document.body.querySelectorAll('.confirm__actions .btn')).find((b) => (b.textContent ?? '').includes('Disable'))
    if (!confirmBtn) throw new Error('确认框确认按钮不存在')
    confirmBtn.dispatchEvent(new Event('click'))
    await flushPromises()

    expect(setManualDisabled).toHaveBeenCalledTimes(1)
    const [credId, disabled, reason] = (setManualDisabled as ReturnType<typeof vi.fn>).mock.calls[0] as [number, boolean, string]
    expect(credId).toBe(9)
    expect(disabled).toBe(true)
    // ★ reason 为空串会被后端 400 拒掉
    expect(typeof reason).toBe('string')
    expect(reason.length).toBeGreaterThan(0)
  })

  it('立即探测的反馈文案是「已提交」而不是「探测通过」（后端 202 异步）', async () => {
    const { submitCredentialProbe } = await import('@/api/credentialsOps')
    activatePiniaAs('super_admin')
    const w = await mountView()
    await w.findAll('.node-card')[0]?.trigger('click')
    await flushPromises()

    const probeBtn = Array.from(document.body.querySelectorAll('.nodes__ops .btn')).find((b) => (b.textContent ?? '').includes('Probe now'))
    if (!probeBtn) throw new Error('探测按钮不存在')
    probeBtn.dispatchEvent(new Event('click'))
    await flushPromises()
    const confirmBtn = Array.from(document.body.querySelectorAll('.confirm__actions .btn')).find((b) => (b.textContent ?? '').includes('Probe now'))
    if (!confirmBtn) throw new Error('确认按钮不存在')
    confirmBtn.dispatchEvent(new Event('click'))
    await flushPromises()

    expect(submitCredentialProbe).toHaveBeenCalledWith(9)
    // 反馈条在 Sheet 内（Teleport 到 body）⇒ 断言查 document，不是 wrapper.text()
    const text = document.body.textContent ?? ''
    expect(text).toContain('Probe submitted')
    expect(text).not.toContain('passed')
  })

  it('403 单独提示权限问题，不退化成泛化错误', async () => {
    const { setManualDisabled } = await import('@/api/credentialsOps')
    const { ApiError } = await import('@/api/client')
    ;(setManualDisabled as ReturnType<typeof vi.fn>).mockRejectedValueOnce(
      new ApiError(403, 'forbidden'),
    )
    activatePiniaAs('super_admin')
    const w = await mountView()
    await w.findAll('.node-card')[0]?.trigger('click')
    await flushPromises()

    const disableBtn = Array.from(document.body.querySelectorAll('.nodes__ops .btn')).find((b) => (b.textContent ?? '').includes('Disable'))
    if (!disableBtn) throw new Error('停用按钮不存在')
    disableBtn.dispatchEvent(new Event('click'))
    await flushPromises()
    const confirmBtn = Array.from(document.body.querySelectorAll('.confirm__actions .btn')).find((b) => (b.textContent ?? '').includes('Disable'))
    if (!confirmBtn) throw new Error('确认按钮不存在')
    confirmBtn.dispatchEvent(new Event('click'))
    await flushPromises()

    expect(document.body.querySelector('.nodes__op-msg--err')?.textContent).toContain('permission')
  })
})

/**
 * 近期路由决策区（2026-10-06）。
 *
 * ★ 这组判据针对一个具体的**假绿**成因：详情 Sheet 打开时会调
 *   fetchCredentialDecisions，而该测试文件对整个 '@/api/credentialsOps' 做了
 *   vi.mock。第一版 mock 里漏了这个函数 ⇒ 它是 undefined ⇒ 调用即抛
 *   ⇒ 决策区永远走错误态、从不渲染内容，而**所有旧测试照样全绿**。
 *   ⇒ 「模块被整体 mock」时，新增导出必须显式登记，否则该区零覆盖。
 *
 * 钉三态，且要求彼此**互斥**（防止把空结果当错误、或反之）：
 *   ① 空数组 → 「最近没有路由记录」（这是真实结论，不是故障）
 *   ② 抛错   → 错误提示，且**不牵连**其余详情区
 *   ③ 有数据 → 渲染模型名，且失败条带出 error_class
 */
describe('NodesView 近期路由决策区', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  async function openDetailWith(mockImpl: () => Promise<unknown>) {
    const { fetchCredentialDecisions } = await import('@/api/credentialsOps')
    ;(fetchCredentialDecisions as ReturnType<typeof vi.fn>).mockImplementationOnce(mockImpl as never)
    const pinia = createPinia()
    setActivePinia(pinia)
    useAuthStore().userInfo = {
      id: 1, tenant_id: 't1', username: 'ops', display_name: 'Ops',
      email: 'ops@x', role: 'super_admin', enabled: true,
    }
    const w = mount(NodesView, { attachTo: document.body, global: { plugins: [pinia] } })
    await flushPromises()
    await flushPromises()
    await w.findAll('.node-card')[0]?.trigger('click')
    await flushPromises()
    await flushPromises()
    return w
  }

  it('空数组显示「无记录」，不退化成错误', async () => {
    await openDetailWith(async () => [])
    const text = document.body.textContent ?? ''
    expect(text).toContain('No routing records')
    expect(text).not.toContain('Invalid')
  })

  it('抛错时显示错误，但状态字段与操作区仍可用（不牵连）', async () => {
    const { ApiError } = await import('@/api/client')
    await openDetailWith(async () => {
      throw new ApiError(403, 'forbidden')
    })
    const text = document.body.textContent ?? ''
    // 决策区报了权限问题
    expect(text).toContain('permission')
    // ★ 但详情其余部分必须还在 —— 这一区失败不得拖垮整个 Sheet
    expect(text).toContain('Availability')
    expect(text).toContain('Operations')
  })

  it('有数据时渲染模型名，失败条目带出 error_class', async () => {
    await openDetailWith(async () => [
      {
        ts: '2026-10-06T13:00:00Z', request_id: 'r1', model: 'claude-sonnet-4-6',
        tier: 1, success: true, latency_ms: 820, error_class: null,
        chosen_provider_id: 3, client_model: null, outbound_model: null, sticky_hit: null,
      },
      {
        ts: '2026-10-06T13:05:00Z', request_id: 'r2', model: 'claude-sonnet-4-6',
        tier: 2, success: false, latency_ms: 5000, error_class: 'upstream_timeout',
        chosen_provider_id: 3, client_model: null, outbound_model: null, sticky_hit: true,
      },
    ])
    const text = document.body.textContent ?? ''
    expect(text).toContain('claude-sonnet-4-6')
    expect(text).toContain('upstream_timeout')
    expect(text).toContain('sticky hit')
    // 两个 request_id 都要在 ⇒ 列表没被截断成一条
    const list = document.body.querySelectorAll('.nodes__decision')
    expect(list.length).toBe(2)
  })
})

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

/**
 * ★ 用 importOriginal 透传真实导出，只替换**发网络**的函数。
 *
 * 原因（2026-10-08 实测的既有陷阱）：本文件对 '@/api/credentialsOps' 做了整模块
 * mock。第一版若照抄旧写法（纯对象字面量），`resetStateProbeIndeterminate` /
 * `resetStateOutcomeAmbiguous` 这两个**纯函数**会是 undefined ⇒ 视图调用即抛
 * ⇒ reset-state 分支永远走错误态，**而其余测试照样全绿**。
 * ⇒ 纯函数必须走 actual，别手抄一份实现（手抄的那份会与源码漂移）。
 */
vi.mock('@/api/credentialsOps', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/credentialsOps')>()
  return {
    ...actual,
    setManualDisabled: vi.fn(async () => ({ success: true, message: 'ok' })),
    clearManualDisabled: vi.fn(async () => ({ success: true, message: 'ok' })),
    submitCredentialProbe: vi.fn(async () => ({ message: 'queued', credential_id: 9, status: 'pending' })),
    forceRecoverCredential: vi.fn(async () => ({
      triggered: true, credential_id: 9, timestamp: 't', message: 'm',
      key_cache_invalidated: true, key_rotator_reset: true,
    })),
    // ★★★ 逐字段照抄 admin/routing_reset.go:146-153 的 map 字面量。
    //   注意：**没有** success / reset_fields（旧 TS 接口凭空写的两个键）。
    resetCredentialState: vi.fn(async () => ({
      message: 'credential state reset',
      credential_id: 9,
      raw_model: '',
      actor: 'ops',
      probe_triggered: false,
      details: {
        credential_id: 9,
        raw_model: '',
        reason: 'mobile',
        endpoint: 'reset-state',
        actor: 'ops',
        db_committed: true,
      },
    })),
    // 2026-10-06：详情 Sheet 打开时会拉「近期路由决策」。不 mock 它的话
    // 模块整个是 undefined ⇒ 这一区静默不渲染，测试全绿却零覆盖。
    fetchCredentialDecisions: vi.fn(async () => []),
  }
})

/**
 * ★★ 路由阻塞诊断模块同样要登记（2026-10-08）。
 *
 * 走 importOriginal 透传真 helper：视图要调用 `routingBlockedTotalsDisagree` /
 * `routingBlockedStateUnavailable` / `routingBlockedReasonAbsent` 等**纯函数**，
 * 整模块 mock 会把它们变成 undefined ⇒ 诊断区永远走错误态而测试全绿。
 * 纯函数绝不手抄一份实现进 mock —— 那份会与源码漂移。
 */
vi.mock('@/api/routingBlocked', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/routingBlocked')>()
  return {
    ...actual,
    // 逐字照抄 admin/diagnostics_routing.go:44-53
    fetchRoutingBlockedDiagnostic: vi.fn(async () => ({
      provider_id: 3,
      provider_name: '小米大模型',
      bindings_total: 2,
      bindings_routable: 1,
      bindings_blocked: 1,
      block_reason_breakdown: { quota_exhausted: 1 },
      credentials: [
        {
          credential_id: 9,
          credential_label: 'tok-a:小米大模型',
          status: 'ok',
          availability_state: 'ready',
          health_status: 'healthy',
          manual_disabled: false,
          lifecycle_status: 'active',
          bindings_total: 2,
          bindings_routable: 1,
          bindings_blocked: 1,
          bindings: [
            {
              credential_id: 9,
              credential_label: 'tok-a',
              raw_model_name: 'gpt-4o',
              is_routable: false,
              unavailable_reason: 'quota_exhausted',
            },
            {
              credential_id: 9,
              credential_label: 'tok-a',
              raw_model_name: 'gpt-4o-mini',
              is_routable: true,
            },
          ],
        },
      ],
    })),
  }
})

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
    // ★ reset-state 注册处 admin/handler.go:946 是 h.superAdmin ⇒ 同属受限档。
    //   只钉探测/强恢的话，把 reset-state 误挪到 admin 档不会被任何测试发现。
    expect(ops.textContent).not.toContain('Reset state')
  })

  it('super_admin 能看到全部五个动作入口', async () => {
    activatePiniaAs('super_admin')
    const w = await mountView()
    await w.findAll('.node-card')[0]?.trigger('click')
    await flushPromises()

    const ops = document.body.querySelector('.nodes__ops')
    if (!ops) throw new Error('操作区未渲染')
    expect(ops.textContent).toContain('Disable')
    expect(ops.textContent).toContain('Probe now')
    expect(ops.textContent).toContain('Force recover')
    expect(ops.textContent).toContain('Reset state')
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
 * reset-state 入口（2026-10-08，admin/routing_reset.go）。
 *
 * 这组判据守的是三件**语义**的事，都不是渲染细节：
 *   ① reason 是**审计留痕**（:69-72 空串直接 400），且默认理由必须对得上动作 ——
 *      用三元链实现时新增动作会静默落到别的动作的理由上，审计因此记错。
 *   ② `probe_triggered` 有三义（:151 `req.TriggerProbe && probeSubmitter != nil`）：
 *      请求了却拿到 false = **未能确定**，不是「没触发」。吞掉它就是撒谎。
 *   ③ ★★ 失败分档：5xx 那一支可能「DB 已改但请求报错」，而响应里**没有**
 *      db_committed / audit_outcome ⇒ 不得说「操作失败」。4xx 则可以确定地报未执行。
 */
describe('NodesView reset-state 入口', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  async function openResetConfirm() {
    activatePiniaAs('super_admin')
    const w = await mountView()
    await w.findAll('.node-card')[0]?.trigger('click')
    await flushPromises()
    const btn = Array.from(document.body.querySelectorAll('.nodes__ops .btn')).find((b) =>
      (b.textContent ?? '').includes('Reset state'),
    )
    if (!btn) throw new Error('reset-state 按钮不存在')
    btn.dispatchEvent(new Event('click'))
    await flushPromises()
    return w
  }

  async function confirmReset() {
    const confirmBtn = Array.from(document.body.querySelectorAll('.confirm__actions .btn')).find((b) =>
      (b.textContent ?? '').includes('Reset state'),
    )
    if (!confirmBtn) throw new Error('确认按钮不存在')
    confirmBtn.dispatchEvent(new Event('click'))
    await flushPromises()
    await flushPromises()
  }

  it('★ reason 输入框与 trigger_probe 勾选位都出现，且默认理由对得上本动作', async () => {
    await openResetConfirm()
    // reason 是审计必填 ⇒ 必须有输入位
    expect(document.body.querySelector('.nodes__reason-input')).not.toBeNull()
    // trigger_probe 勾选位
    const cb = document.body.querySelector('.nodes__probe-checkbox') as HTMLInputElement | null
    if (!cb) throw new Error('trigger_probe 勾选位未渲染')
    expect(cb.checked).toBe(false)

    await confirmReset()
    const { resetCredentialState } = await import('@/api/credentialsOps')
    expect(resetCredentialState).toHaveBeenCalledTimes(1)
    const [credId, reason, rawModel, triggerProbe] = (resetCredentialState as ReturnType<typeof vi.fn>).mock
      .calls[0] as [number, string, string, boolean]
    expect(credId).toBe(9)
    // ★ reason 非空（空串后端 400），且默认理由必须是「状态复位」而不是别的动作
    expect(reason.length).toBeGreaterThan(0)
    expect(reason).toContain('State reset from mobile')
    expect(reason).not.toContain('Force recovered')
    // ★ rawModel 留空 = 整凭据复位（routing_reset.go:37-39）
    expect(rawModel).toBe('')
    expect(triggerProbe).toBe(false)
  })

  it('★ reset-state 的确认按钮必须是危险档（不可撤销的审计写入）', async () => {
    await openResetConfirm()
    // AppConfirm.vue:50 —— danger 决定 btn--danger / btn--primary
    const confirmBtn = Array.from(document.body.querySelectorAll('.confirm__actions .btn')).find((b) =>
      (b.textContent ?? '').includes('Reset state'),
    )
    if (!confirmBtn) throw new Error('确认按钮不存在')
    // ★ 降级成普通档会让一个不可撤销的写操作看起来像「保存」
    expect(confirmBtn.className).toContain('btn--danger')
    expect(confirmBtn.className).not.toContain('btn--primary')
  })

  it('★ 勾了 trigger_probe 才把它透传成 true', async () => {
    await openResetConfirm()
    const cb = document.body.querySelector('.nodes__probe-checkbox') as HTMLInputElement
    cb.checked = true
    cb.dispatchEvent(new Event('change'))
    await flushPromises()
    await confirmReset()

    const { resetCredentialState } = await import('@/api/credentialsOps')
    const call = (resetCredentialState as ReturnType<typeof vi.fn>).mock.calls[0] as unknown[]
    expect(call[3]).toBe(true)
  })

  it('★★★ 请求了探测但回 probe_triggered=false ⇒ 说「未能确定」，不能说「没触发」', async () => {
    const { resetCredentialState } = await import('@/api/credentialsOps')
    // 默认 mock 就返回 probe_triggered: false；这次勾了 trigger_probe
    ;(resetCredentialState as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      message: 'credential state reset', credential_id: 9, raw_model: '', actor: 'ops',
      probe_triggered: false,
      details: { credential_id: 9, raw_model: '', reason: 'x', endpoint: 'reset-state', actor: 'ops' },
    })
    await openResetConfirm()
    const cb = document.body.querySelector('.nodes__probe-checkbox') as HTMLInputElement
    cb.checked = true
    cb.dispatchEvent(new Event('change'))
    await flushPromises()
    await confirmReset()

    const note = document.body.querySelector('.nodes__op-msg--warn')
    expect(note?.textContent).toContain('did not confirm')
    // ★ 两档必须互斥：不能同时说「已提交」
    expect(note?.textContent).not.toContain('Probe submitted')
    // 成功文案仍在 —— 复位本身确实成功了
    expect(document.body.textContent).toContain('State reset submitted')
  })

  it('★ probe_triggered=true ⇒ 只说「已提交」并带上免责句，不裸奔', async () => {
    const { resetCredentialState } = await import('@/api/credentialsOps')
    ;(resetCredentialState as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      message: 'credential state reset', credential_id: 9, raw_model: '', actor: 'ops',
      probe_triggered: true,
      details: { credential_id: 9, raw_model: '', reason: 'x', endpoint: 'reset-state', actor: 'ops' },
    })
    await openResetConfirm()
    await confirmReset()

    const note = document.body.querySelector('.nodes__op-msg--warn')
    expect(note?.textContent).toContain('Probe submitted')
    // ★ 「提交」不等于「通过」⇒ 免责句本身要被钉住
    expect(note?.textContent).toContain('does not mean the probe passed')
    expect(note?.textContent).not.toContain('did not confirm')
  })

  it('★★★★★ 5xx 失败 ⇒ 必须提示「状态可能已改」，不得说成操作失败', async () => {
    const { resetCredentialState } = await import('@/api/credentialsOps')
    const { ApiError } = await import('@/api/client')
    ;(resetCredentialState as ReturnType<typeof vi.fn>).mockRejectedValueOnce(
      new ApiError(500, 'internal error (see server logs)'),
    )
    await openResetConfirm()
    await confirmReset()

    const err = document.body.querySelector('.nodes__op-msg--err')?.textContent ?? ''
    expect(err).toContain('may have been changed')
    // ★ 不得退化成泛化错误：那会让运维以为「什么都没发生」而直接重试
    expect(err).not.toContain('internal error (see server logs)')
  })

  it('★★★★ 4xx 失败 ⇒ 可以确定地报「未执行」，不该吓人说可能已改', async () => {
    const { resetCredentialState } = await import('@/api/credentialsOps')
    const { ApiError } = await import('@/api/client')
    ;(resetCredentialState as ReturnType<typeof vi.fn>).mockRejectedValueOnce(
      new ApiError(400, 'reason is required for audit trail'),
    )
    await openResetConfirm()
    await confirmReset()

    const err = document.body.querySelector('.nodes__op-msg--err')?.textContent ?? ''
    expect(err).toContain('reason is required for audit trail')
    // ★ 400 全部发生在 applyForceEnable 之前（routing_reset.go:56-89）⇒ 无二义
    expect(err).not.toContain('may have been changed')
  })

  it('★★★ 传输层失败（status=0）同样算二义', async () => {
    const { resetCredentialState } = await import('@/api/credentialsOps')
    const { ApiError } = await import('@/api/client')
    // client.ts:172 把断网归一成 ApiError(0, 'network_error')：
    // 请求可能已经落库，只是回程断了 ⇒ 不能说「未执行」
    ;(resetCredentialState as ReturnType<typeof vi.fn>).mockRejectedValueOnce(new ApiError(0, 'network_error'))
    await openResetConfirm()
    await confirmReset()

    const err = document.body.querySelector('.nodes__op-msg--err')?.textContent ?? ''
    expect(err).toContain('may have been changed')
  })

  it('★ 探测/强恢的确认框里**不出现** trigger_probe 勾选位', async () => {
    activatePiniaAs('super_admin')
    const w = await mountView()
    await w.findAll('.node-card')[0]?.trigger('click')
    await flushPromises()
    const btn = Array.from(document.body.querySelectorAll('.nodes__ops .btn')).find((b) =>
      (b.textContent ?? '').includes('Probe now'),
    )
    btn?.dispatchEvent(new Event('click'))
    await flushPromises()
    expect(document.body.querySelector('.nodes__probe-checkbox')).toBeNull()
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

/**
 * 路由阻塞诊断区（2026-10-08，admin/diagnostics_routing.go）。
 *
 * 守三件事：
 *   ① ★ 权限分档：注册处 admin/handler.go:1464 是 `h.superAdmin` ⇒ tenant_admin
 *      不该看到入口（后端会直接 403），不能靠后端报错兜底。
 *   ② ★ 按需加载：最坏返回 500 条绑定，**不能**随详情自动拉 ⇒ 打开详情
 *      不应发出这个请求（否则每次点开卡片都付这个代价）。
 *   ③ ★★ 后端那几个「数字对不上 / 状态整段缺失」的情况必须**显示出来**，
 *      而不是把矛盾的数字原样呈现让用户自己发现。
 */
describe('NodesView 路由阻塞诊断区', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
  })

  async function openDetailAs(role: string) {
    activatePiniaAs(role)
    const w = await mountView()
    await w.findAll('.node-card')[0]?.trigger('click')
    await flushPromises()
    return w
  }

  async function clickOpen() {
    const btn = Array.from(document.body.querySelectorAll('.nodes__rblocked .btn')).find((b) =>
      (b.textContent ?? '').includes('Why routing'),
    )
    if (!btn) throw new Error('诊断入口按钮不存在')
    btn.dispatchEvent(new Event('click'))
    await flushPromises()
    await flushPromises()
  }

  it('★★ 按需加载：打开详情**不**发诊断请求', async () => {
    await openDetailAs('super_admin')
    const { fetchRoutingBlockedDiagnostic } = await import('@/api/routingBlocked')
    expect(fetchRoutingBlockedDiagnostic).not.toHaveBeenCalled()
    // 诊断区存在但还没数据
    expect(document.body.querySelector('.nodes__rblocked')).not.toBeNull()
  })

  it('★★★★ tenant_admin 看不到诊断入口（h.superAdmin 档）', async () => {
    await openDetailAs('tenant_admin')
    const text = document.body.textContent ?? ''
    expect(text).not.toContain('Why routing cannot find it')
  })

  it('★★★★ super_admin 点入口后带 provider_id 拉取并渲染绑定原因', async () => {
    await openDetailAs('super_admin')
    await clickOpen()
    const { fetchRoutingBlockedDiagnostic } = await import('@/api/routingBlocked')
    expect(fetchRoutingBlockedDiagnostic).toHaveBeenCalledWith(3)

    const text = document.body.textContent ?? ''
    expect(text).toContain('tok-a:小米大模型')
    expect(text).toContain('gpt-4o')
    expect(text).toContain('quota_exhausted')
    // 可路由那条显示的是「可路由」而不是原因
    expect(text).toContain('gpt-4o-mini')
    expect(text).toContain('routable')
  })

  /**
   * ★★★★★ 负控：provider_id **必须来自详情对象**，不能是硬编码常量。
   *
   * 上一版判据是 `toHaveBeenCalledWith(3)`，而夹具的 provider_id 恰好也是 3
   * ⇒ 变异 E8 把 `loadRoutingBlocked(pid)` 改成 `loadRoutingBlocked(3)` 时
   * **照样全绿**。那测的是巧合，不是「取自详情」。
   * ⇒ 这里换成 provider_id=7 的卡片：任何硬编码常量都会立刻暴露。
   */
  it('★★★★★ provider_id 取自详情卡片，不是硬编码常量（负控）', async () => {
    const { fetchMonitorSummary } = await import('@/api/nodes')
    ;(fetchMonitorSummary as ReturnType<typeof vi.fn>).mockResolvedValueOnce([
      { ...WITH_COUNTS, provider_id: 7 },
    ])
    const w = await mountView()
    await w.findAll('.node-card')[0]?.trigger('click')
    await flushPromises()
    await clickOpen()

    const { fetchRoutingBlockedDiagnostic } = await import('@/api/routingBlocked')
    expect(fetchRoutingBlockedDiagnostic).toHaveBeenCalledWith(7)
    // ★ 3 是全仓最常见的默认供应商 id，硬编码它会让绝大多数用例照常通过
    expect(fetchRoutingBlockedDiagnostic).not.toHaveBeenCalledWith(3)
  })

  it('★★★★★ 计数矛盾 / 截断 会被显式提示，而不是把矛盾数字原样显示', async () => {
    const { fetchRoutingBlockedDiagnostic } = await import('@/api/routingBlocked')
    ;(fetchRoutingBlockedDiagnostic as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      provider_id: 3,
      provider_name: '小米大模型',
      // 逐字复刻后端钳位 bug：total 钳 500、routable 未钳 ⇒ blocked 为负
      bindings_total: 500,
      bindings_routable: 501,
      bindings_blocked: -1,
      block_reason_breakdown: {},
      credentials: [],
      truncated: true,
    })
    await openDetailAs('super_admin')
    await clickOpen()

    const text = document.body.textContent ?? ''
    expect(text).toContain('truncated')
    expect(text).toContain('self-contradictory counts')
    expect(text).toContain('do not add up')
  })

  it('★★★★★ 状态整段缺失时不得显示「已手动停用」，而要显示状态未知', async () => {
    const { fetchRoutingBlockedDiagnostic } = await import('@/api/routingBlocked')
    ;(fetchRoutingBlockedDiagnostic as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      provider_id: 3,
      provider_name: '小米大模型',
      bindings_total: 1,
      bindings_routable: 0,
      bindings_blocked: 1,
      block_reason_breakdown: { unknown: 1 },
      credentials: [
        {
          credential_id: 9,
          credential_label: 'tok-a',
          // ★★ 后端凭据状态查询失败时的真实形态：五个字段全零值
          status: '',
          availability_state: '',
          health_status: '',
          lifecycle_status: '',
          manual_disabled: false,
          bindings_total: 1,
          bindings_routable: 0,
          bindings_blocked: 1,
          bindings: [
            { credential_id: 9, credential_label: 'tok-a', raw_model_name: 'gpt-4o', is_routable: false },
          ],
        },
      ],
    })
    await openDetailAs('super_admin')
    await clickOpen()

    const text = document.body.textContent ?? ''
    expect(text).toContain('Credential state could not be fetched')
    expect(text).toContain('state unknown')
    // ★ manual_disabled=false 是错值，绝不能渲染成「未停用」这种结论
    expect(text).not.toContain('manually disabled')
    // 键不存在 ⇒ 原因未知，而不是空串
    expect(text).toContain('reason not provided by the server')
  })

  it('★★★ 空串原因与「原因未知」渲染成两种不同的文案', async () => {
    const { fetchRoutingBlockedDiagnostic } = await import('@/api/routingBlocked')
    ;(fetchRoutingBlockedDiagnostic as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
      provider_id: 3,
      provider_name: '小米大模型',
      bindings_total: 2,
      bindings_routable: 0,
      bindings_blocked: 2,
      block_reason_breakdown: { '': 1, quota_exhausted: 1 },
      credentials: [
        {
          credential_id: 9,
          credential_label: 'tok-a',
          status: 'ok',
          availability_state: 'ready',
          health_status: 'healthy',
          lifecycle_status: 'active',
          manual_disabled: false,
          bindings_total: 2,
          bindings_routable: 0,
          bindings_blocked: 2,
          bindings: [
            { credential_id: 9, credential_label: 'tok-a', raw_model_name: 'm-empty', is_routable: false, unavailable_reason: '' },
            { credential_id: 9, credential_label: 'tok-a', raw_model_name: 'm-quota', is_routable: false, unavailable_reason: 'quota_exhausted' },
          ],
        },
      ],
    })
    await openDetailAs('super_admin')
    await clickOpen()

    const text = document.body.textContent ?? ''
    // ★ breakdown 里的空字符串键也要提示 —— 它表示「原因是空串」
    expect(text).toContain('contain an empty entry')
    expect(text).toContain('reason is an empty string')
    expect(text).not.toContain('reason not provided by the server')
  })
})

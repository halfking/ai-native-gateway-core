// wizardStatusUnknownVsNotActivated.silentcatch.test.ts —— 首启向导：状态未知 ≠ 未激活
//
// ## 挡住的是什么
//
// `BootstrapWizardView.loadStatus` 原来是 `catch { status.value = null }`。
// 一个 null 在这个视图里造出**三处**后果，其中两处是页面上的断言：
//
// ① 模板 `status?.center_online ? '在线（可自动注册）' : '离线（不阻塞激活）'`
//    —— 本地 `/api/system/bootstrap/status` 取不到时，页面拿一句**安慰话**
//    覆盖了一个未知的量，而且明说「不阻塞」。
// ② 模板 `status?.message || '可以登录本地后台开始使用。…'`
//    —— 兜底文案在没有数据时断言「已就绪」。
// ③ `onMounted` 的 `if (status.value?.activated)`：未知 ⇒ 当成未激活 ⇒
//    把一台**其实已激活**的机器领进完整激活流程。**这条最重。**
//
// 为什么排这么前：这是**首启向导**，用户对本地服务还没有任何先验知识，
// 页面上任何一句肯定句都会被直接采信。
//
// ## 判据钉住的是什么
//
// ① status 失败 → 说「状态未知」，不说「离线（不阻塞激活）」
// ② status 失败 → 走到第 4 步（结束页）时也不说「已就绪」
// ③ status 失败 → **不进入激活流程**（不弹协议、不进第 1 步）
// ④ status 成功且已激活 → 直接到第 4 步，且仍显示正常文案（正向对照）
// ⑤ status 成功且未激活 → 正常进入流程（正向对照，防「永远停在未知」）
//
// ★ ③ 是本判据最要紧的一条：它守的不是一句话，是**状态机**。
//   只断言文案的人会漏掉它——「未知」被当成「未激活」时，页面上**没有一句**是错的，
//   它只是把人带去了错误的下一步。
//
// 反向对照见文末，均已实跑并断言变异确实发生。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const statusMock = vi.fn()
const fingerprintMock = vi.fn()
const collectClientFingerprintMock = vi.fn()
const maintainLifecycleApiMock = { getInfo: vi.fn() }

// ⚠️ 说明符必须相对**本判据**解析，不是相对被测视图：
// 视图在 src/views/bootstrap/，它写 '../../api/bootstrap'；
// 本判据在 src/views/，写 '../../api/bootstrap' 会指向仓库根下的 api/ ——
// **没人 import 那个模块**，桩静默不生效，真模块照常加载，
// 报错是 `Failed to parse URL from /api/system/bootstrap/status`，
// 看起来像「桩返回了错的东西」，其实**桩压根没挂上**。
vi.mock('../api/bootstrap', () => ({
  bootstrapApi: {
    status: (...a: unknown[]) => statusMock(...a),
    fingerprint: (...a: unknown[]) => fingerprintMock(...a),
  },
  markBootstrapActivated: vi.fn(),
}))
vi.mock('../api/maintainLifecycle', () => ({ maintainLifecycleApi: maintainLifecycleApiMock }))
vi.mock('../utils/deviceFingerprint', () => ({
  collectClientFingerprint: (...a: unknown[]) => collectClientFingerprintMock(...a),
  ensureInstanceId: () => 'inst-test',
  setInstanceId: vi.fn(),
  resolveHardwareHash: async () => 'hash-test',
}))
vi.mock('vue-router', () => ({
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
  useRoute: () => ({ query: {} }),
}))

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  missingWarn: false,
  fallbackWarn: false,
  messages: { 'zh-CN': {} },
})

const ACTIVATED = {
  activated: true,
  instance_id: 'inst-test',
  hardware_hash: 'hash-test',
  center_online: true,
  message: '本机已激活。',
}
const NOT_ACTIVATED = { ...ACTIVATED, activated: false, center_online: false, message: '尚未激活。' }

type WizardVm = {
  step: number
  statusError: string
  loadStatus: () => Promise<void>
  loadFingerprint: () => Promise<void>
  error: string
}

async function renderWizard() {
  const View = (await import('./bootstrap/BootstrapWizardView.vue')).default
  const w = mount(View, {
    global: {
      plugins: [i18n],
      stubs: { OperationAgreementDialog: true, RouterLink: true },
    },
    shallow: false,
  })
  await flushPromises()
  await flushPromises()
  await flushPromises()
  return w
}
const vm = (w: Awaited<ReturnType<typeof renderWizard>>) => w.vm as unknown as WizardVm

describe('BootstrapWizardView：激活状态取不到时不得说「未激活 / 离线 / 已就绪」', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    // 同意协议已勾选：这样「不弹协议」与「不进第 1 步」就是可区分的两件事
    localStorage.setItem('llmgw_op_agreement_activate_2026-07-17', new Date().toISOString())
    fingerprintMock.mockResolvedValue({
      hardware_hash: 'hash-test',
      instance_id: 'inst-test',
      network_summary: '10.0.0.1/8',
      os: 'linux',
      arch: 'amd64',
    })
    collectClientFingerprintMock.mockResolvedValue({ hardware_hash: 'hash-test', network_summary: '10.0.0.1/8' })
    maintainLifecycleApiMock.getInfo.mockResolvedValue({})
    statusMock.mockResolvedValue(NOT_ACTIVATED)
  })

  it('① status 失败 → 明确说「状态未知」，不说「离线（不阻塞激活）」', async () => {
    statusMock.mockRejectedValue(new Error('status 503'))
    const w = await renderWizard()
    expect(vm(w).statusError).toBe('status 503')
    // 直接驱动到展示网络状态的那一步（指纹信息卡在第 1 步）
    ;(vm(w) as unknown as { step: number }).step = 1
    await flushPromises()
    const text = w.text()
    expect(text).toContain('状态未知')
    expect(text).not.toContain('离线（不阻塞激活）')
    expect(text).not.toContain('在线（可自动注册）')
  })

  it('② status 失败 → 结束页不许说「已就绪」（status.message 的兜底文案）', async () => {
    statusMock.mockRejectedValue(new Error('status 503'))
    const w = await renderWizard()
    // ⚠️ 直接把 step 推到 4 去**看那句文案**，因为修复后正常路径根本到不了这里。
    //   这条断言守的是「万一到了结束页，也不许拿兜底文案说已就绪」。
    ;(vm(w) as unknown as { step: number }).step = 4
    await flushPromises()
    const text = w.text()
    expect(text).not.toContain('可以登录本地后台开始使用')
    expect(text).toContain('本机状态未确认')
  })

  it('③ status 失败 → 不得把「未知」当成「未激活」把人领进激活流程（状态机）', async () => {
    statusMock.mockRejectedValue(new Error('status 503'))
    const w = await renderWizard()
    // 关键断言：停在第 0 步，不进第 1 步（不加载指纹 = 不开始激活流程）
    expect(vm(w).step, '状态未知时必须停下，不能当作未激活继续').not.toBe(1)
    expect(vm(w).error).toContain('无法确认本机激活状态')
    // 指纹端点压根没被叫过 —— 没有开始任何激活准备动作
    expect(fingerprintMock, '状态未知时不该开始激活准备流程').not.toHaveBeenCalled()
  })

  it('④ status 成功且已激活 → 直接到结束页（正向对照，防「永远停在未知」）', async () => {
    statusMock.mockResolvedValue(ACTIVATED)
    const w = await renderWizard()
    expect(vm(w).statusError).toBe('')
    expect(vm(w).step).toBe(4)
    expect(w.text()).toContain('本机已激活。')
  })

  it('⑤ status 成功且未激活 → 正常进第 1 步并加载指纹（正向对照）', async () => {
    const w = await renderWizard()
    expect(vm(w).statusError).toBe('')
    expect(vm(w).step).toBe(1)
    expect(fingerprintMock, '未激活时该走正常流程').toHaveBeenCalled()
  })
})

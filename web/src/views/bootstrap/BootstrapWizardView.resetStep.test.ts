// BootstrapWizardView.resetStep.test.ts —— 首启向导：第一次激活链上必须有「重置数据」这一步
//
// ## 这道判据钉的是什么
//
// 2026-11-16 之前的首启向导只有 5 步：用户协议 → 设备指纹 → 激活 License →
// 注册中心 → 完成。**整条链上没有任何「重置数据」的操作**——重置只在帮助手册里
// 以「若这台机器是从旧环境迁过来的」的附带说明存在（而且还要人自己去手册里找、
// 自己登录、自己找到面板）。
//
// ⇒ 判据钉住的事实：
//   ① 步骤条里**有**「重置数据」这一步，且排在「完成」之前；
//   ② 这一步的**默认动作是去重置**（主按钮指向数据初始化面板的深链）；
//   ③ 这一步**可跳过**（全新环境不必被迫走一遍重置）；
//   ④ 已经激活过的机器重进向导时**不被拽去重置**（重置属于「第一次激活」这一步）；
//   ⑤ 未激活的机器照常进第 1 步（正向对照，防「永远停在重置步」）。
//
// ★ 为什么不把重置做成向导内的直接执行：
//   首启向导是**登录前**的公开页（/api/system/bootstrap/* 走 noAuth 中间件），
//   而数据重置的三步端点是 super_admin 档 ⇒ 向导内直接执行就得新开一个
//   **无鉴权的破坏性端点**。本轮不这么做：重置仍在已登录的面板里执行
//   （面板默认已全选全部类别 ⇒ 默认要重置），向导只负责把这一步摆到流程里并
//   给出直达入口。
//
// ## 门自身的「有牙」证明
//
// 文末说明：变异在**源码层**做（把「重置数据」这一步从步骤条与模板里摘掉），
// 重跑本判据必须判红，随后还原。

import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

const statusMock = vi.fn()
const fingerprintMock = vi.fn()
const collectClientFingerprintMock = vi.fn()
const maintainLifecycleApiMock = { getInfo: vi.fn() }

// ⚠️ 说明符相对**本判据**解析：本判据在 src/views/bootstrap/ ⇒
//   '../../api/bootstrap' 指向 src/api/bootstrap，与被测视图同一个模块 ⇒ 桩挂得上。
vi.mock('../../api/bootstrap', () => ({
  bootstrapApi: {
    status: (...a: unknown[]) => statusMock(...a),
    fingerprint: (...a: unknown[]) => fingerprintMock(...a),
  },
  markBootstrapActivated: vi.fn(),
}))
vi.mock('../../api/maintainLifecycle', () => ({ maintainLifecycleApi: maintainLifecycleApiMock }))
vi.mock('../../utils/deviceFingerprint', () => ({
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

type WizardVm = { step: number; statusError: string; loadStatus: () => Promise<void> }

async function renderWizard() {
  const View = (await import('./BootstrapWizardView.vue')).default
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

describe('首启向导：第一次激活链上有「重置数据」这一步', { timeout: 20_000 }, () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(console, 'error').mockImplementation(() => {})
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

  it('① 步骤条第 5 项是「重置数据」，且排在「完成」之前', async () => {
    const w = await renderWizard()
    const items = w.findAll('.wizard-steps__item')
    expect(items.length).toBe(6)
    // 第 5 项 = 重置数据（走 i18n ⇒ 键路径出现在文本里）
    expect(items[4].text()).toContain('dataLifecycle.dataInit.wizard.stepLabel')
    // 第 6 项 = 完成
    expect(items[5].text()).toContain('完成')
  })

  it('② 第 4 步的默认动作是「去重置数据」，指向数据初始化面板的深链', async () => {
    const w = await renderWizard()
    ;(vm(w) as WizardVm).step = 4
    await flushPromises()

    const link = w.find('a[href="/admin/data-lifecycle?tab=data-init"]')
    expect(link.exists(), '重置步骤必须给出直达数据初始化面板的入口').toBe(true)
    expect(link.text()).toContain('dataLifecycle.dataInit.wizard.goReset')
    // 该步的说明与提示都在（默认要重置 ⇒ 用户看得见「默认」是什么）
    expect(w.text()).toContain('dataLifecycle.dataInit.wizard.desc')
    expect(w.text()).toContain('dataLifecycle.dataInit.wizard.hint')
  })

  it('③ 第 4 步可跳过（全新环境不必被迫重置）', async () => {
    const w = await renderWizard()
    ;(vm(w) as WizardVm).step = 4
    await flushPromises()

    const skipBtn = w.findAll('button').find(b => b.text().includes('dataLifecycle.dataInit.wizard.skip'))
    expect(skipBtn, '必须给出「跳过重置」的次动作').toBeTruthy()
    // 点跳过 ⇒ 落到结束页（第 5 步）
    await skipBtn!.trigger('click')
    await flushPromises()
    expect(vm(w).step).toBe(5)
  })

  it('④ 已经激活过的机器重进向导 ⇒ 直接到结束页，不被拽去重置', async () => {
    statusMock.mockResolvedValue(ACTIVATED)
    const w = await renderWizard()
    expect(vm(w).statusError).toBe('')
    expect(vm(w).step).toBe(5)
  })

  it('⑤ 未激活 ⇒ 照常进第 1 步（正向对照，防「永远停在重置步」）', async () => {
    const w = await renderWizard()
    expect(vm(w).statusError).toBe('')
    expect(vm(w).step).toBe(1)
    expect(fingerprintMock).toHaveBeenCalled()
  })
})

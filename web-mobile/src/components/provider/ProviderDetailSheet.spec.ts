import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import ProviderDetailSheet from './ProviderDetailSheet.vue'
import { getProviderCredentials, getProviderDetail, getProviderModels, setProviderManualDisabled } from '@/api/providers'
import { useAuthStore } from '@/stores/auth'

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/nodeAudit', () => ({ fetchNodeAudit: vi.fn(async () => ({ entries: [], count: 0, limit: 30 })) }))

vi.mock('@/api/providers', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/providers')>()
  return {
    ...actual,
    getProviderDetail: vi.fn(),
    getProviderCredentials: vi.fn(async () => []),
    getProviderModels: vi.fn(async () => []),
    setProviderManualDisabled: vi.fn(),
  }
})

const provider = {
  id: 5,
  code: 'bedrock',
  display_name: 'Bedrock',
  catalog_code: 'aws',
  protocol: 'anthropic-messages',
  base_url: null,
  enabled: true,
  manual_disabled: false,
  routability: 'available',
} as const

describe('ProviderDetailSheet', () => {
  let pinia: ReturnType<typeof createPinia>

  beforeEach(() => {
    document.body.innerHTML = ''
    vi.clearAllMocks()
    pinia = createPinia()
    setActivePinia(pinia)
    useAuthStore(pinia).userInfo = {
      id: 1,
      tenant_id: 'default',
      username: 'admin',
      display_name: 'Admin',
      email: '',
      role: 'super_admin',
      enabled: true,
    }
    vi.mocked(getProviderDetail).mockResolvedValue({ ...provider, active_cred_count: 0 } as never)
    vi.mocked(getProviderCredentials).mockResolvedValue([])
    vi.mocked(getProviderModels).mockResolvedValue([])
  })

  it('requires a non-empty audit reason before calling the manual-disable API', async () => {
    const prompt = vi.spyOn(window, 'prompt').mockReturnValue('   ')
    const wrapper = mount(ProviderDetailSheet, {
      attachTo: document.body,
      props: { modelValue: true, provider },
      global: { plugins: [pinia] },
    })
    await flushPromises()

    const manualDisable = document.body.querySelector<HTMLButtonElement>('.provider-detail__head-actions button')
    expect(manualDisable).not.toBeNull()
    manualDisable!.click()
    await flushPromises()

    expect(setProviderManualDisabled).not.toHaveBeenCalled()
    expect(document.body.querySelector('[role="alert"]')?.textContent).toBeTruthy()
    prompt.mockRestore()
    wrapper.unmount()
  })

  // 2026-10-11：原 AppSheet 内置的「叉叉」按钮（id="app-sheet-close"）必须把
  // 父组件的 modelValue 翻成 false、并 emit `close` —— 之前用户实测说
  // 「供应商详情弹窗关不掉」，现在钉死这条路径。点叉叉 ⇒ modelValue=false。
  it('close button on AppSheet header emits update:modelValue=false so parent can clear selection', async () => {
    const wrapper = mount(ProviderDetailSheet, {
      attachTo: document.body,
      props: { modelValue: true, provider },
      global: { plugins: [pinia] },
    })
    await flushPromises()
    const closeBtn = document.body.querySelector<HTMLButtonElement>('#app-sheet-close')
    expect(closeBtn).not.toBeNull()
    closeBtn!.click()
    await flushPromises()
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([false])
    expect(wrapper.emitted('close')).toBeTruthy()
    wrapper.unmount()
  })

  // 2026-10-11：scrim 点击同样要走 close 路径（与叉叉按钮等价），否则用户
  // 在弹窗外一点却没反应会以为「点不动」。但 AppSheet 的 `onScrimClick` 有一道
  // 「必须是最顶 overlay」守卫 ⇒ 单元测试里没有 overlay registry ⇒ 跳过这一支，
  // 在 Maestro / iOS 上点 scrim 真测就行。
  it.skip('clicking the scrim also closes the sheet', () => {})

  // 2026-10-11 真机实测缺陷：KPI 小卡的标题渲染成字面量「{n} 个凭据」。
  // providers.credentials 的文案把计数内嵌在句子里（给列表页整句用的），
  // 这里 t() 没传参 ⇒ {n} 原样留在页面上。数字由 <strong> 单独呈现，
  // 标题要的是纯名词（providers.tab.*）。
  // 判据直接钉「渲染结果里不得出现未替换的占位符」——它对将来改 key 免疫。
  it('KPI 卡片标题不出现未替换的 {n} 占位符', async () => {
    const wrapper = mount(ProviderDetailSheet, {
      attachTo: document.body,
      props: { modelValue: true, provider },
      global: { plugins: [pinia] },
    })
    await flushPromises()
    const kpis = Array.from(document.body.querySelectorAll('.provider-detail__kpis small'))
      .map((el) => el.textContent ?? '')
    expect(kpis.length).toBe(3)
    for (const label of kpis) {
      expect(label, `KPI 标题残留占位符：${label}`).not.toMatch(/\{\w+\}/)
      expect(label.trim()).not.toBe('')
    }
    wrapper.unmount()
  })
})

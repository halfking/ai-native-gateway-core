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
})

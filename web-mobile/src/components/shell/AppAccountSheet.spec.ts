import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { ref } from 'vue'
import AppAccountSheet from './AppAccountSheet.vue'
import { useAuthStore } from '@/stores/auth'
import { fetchMobileNodeDescriptor } from '@/api/mobileNode'
import { setLocale, t } from '@/i18n'
vi.mock('@/api/mobileNode', () => ({ fetchMobileNodeDescriptor: vi.fn() }))
vi.mock('qrcode', () => ({ default: { toCanvas: vi.fn(async () => undefined) } }))
vi.mock('@/hyper/update/useDeploySeqUpdate', () => ({ useDeploySeqUpdate: () => ({ status: ref('idle'), reason: ref(''), checkCount: ref(0), lastCheckedAt: ref(null), localSeq: ref(null), checkNow: vi.fn() }) }))
let wrapper: ReturnType<typeof mount> | undefined
function setup(role: string) {
  const pinia = createPinia(); setActivePinia(pinia)
  useAuthStore().userInfo = { id: 1, username: 'admin', tenant_id: 'default', display_name: 'Admin', role, enabled: true, email: '' }
  wrapper = mount(AppAccountSheet, { props: { modelValue: true }, global: { plugins: [pinia], stubs: { AppSheet: { props: ['modelValue'], template: '<div v-if="modelValue"><slot /></div>' }, AppConfirm: true } } })
  return wrapper
}
beforeEach(() => { vi.resetAllMocks(); setLocale('en-US'); localStorage.setItem('llmgw_mobile_theme', 'light'); vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({ drawImage: vi.fn() } as unknown as CanvasRenderingContext2D) })
afterEach(() => { wrapper?.unmount(); vi.restoreAllMocks(); vi.useRealTimers() })
it('offers node switching even for tenant administrators without QR provisioning', () => {
  const w = setup('tenant_admin')
  expect(w.find('a[href="llmgw://nodes"]').exists()).toBe(true)
  expect(w.text()).not.toContain(t('account.showNodeQr'))
})
it('clears expired QR codes and their fingerprint', async () => {
  vi.useFakeTimers(); vi.setSystemTime(new Date('2026-10-10T00:00:00Z'))
  vi.mocked(fetchMobileNodeDescriptor).mockResolvedValue({ payload: 'signed', expires_at: '2026-10-10T00:00:01Z', signing_key_fingerprint: 'trusted-fingerprint' })
  const w = setup('super_admin')
  await w.find('.btn--secondary').trigger('click'); await flushPromises()
  expect(w.find('canvas').exists()).toBe(true)
  expect(w.text()).toContain('trusted-fingerprint')
  await vi.advanceTimersByTimeAsync(1001)
  expect(w.find('canvas').exists()).toBe(false)
  expect(w.text()).not.toContain('trusted-fingerprint')
  expect(w.text()).toContain(t('account.nodeQrExpired'))
})
it('ignores QR responses after the account sheet closes', async () => {
  let resolve!: (value: Awaited<ReturnType<typeof fetchMobileNodeDescriptor>>) => void
  vi.mocked(fetchMobileNodeDescriptor).mockReturnValue(new Promise((done) => { resolve = done }))
  const w = setup('super_admin')
  await w.find('.btn--secondary').trigger('click')
  await w.setProps({ modelValue: false })
  resolve({ payload: 'signed', expires_at: new Date(Date.now() + 60000).toISOString() })
  await flushPromises(); await w.setProps({ modelValue: true })
  expect(w.find('canvas').exists()).toBe(false)
})

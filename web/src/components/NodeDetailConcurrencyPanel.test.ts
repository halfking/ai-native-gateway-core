import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import NodeDetailConcurrencyPanel from './NodeDetailConcurrencyPanel.vue'
import type { CredentialMonitorSummary } from '../api/credential-monitor'

type FpSlotStats = {
  unlimited: boolean
  slot_limit: number | null
  healthy_slots: number
  occupied_slots: number
  free_slots: number
  details: unknown[]
  message: string
}

const {
  updateCredential,
  getCredentialFpSlotStats,
  setConcurrencyAuto,
} = vi.hoisted(() => ({
  updateCredential: vi.fn(),
  getCredentialFpSlotStats: vi.fn(),
  setConcurrencyAuto: vi.fn(),
}))

vi.mock('../api/providers', () => ({
  updateCredential,
  getCredentialFpSlotStats,
}))
vi.mock('../api/credential-monitor', () => ({
  setConcurrencyAuto,
}))

const i18n = createI18n({ legacy: false, locale: 'zh-CN', messages: { 'zh-CN': {} } })

function makeMonitor(overrides: Partial<CredentialMonitorSummary> = {}): CredentialMonitorSummary {
  return {
    id: 36,
    provider_id: 12763,
    provider_name: 'p',
    label: 'augest',
    status: 'active',
    availability_state: 'ready',
    health_status: 'healthy',
    quota_state: 'ok',
    concurrency_limit: 100,
    concurrency_limit_auto: 8,
    effective_concurrency: 100,
    manual_disabled: false,
    consecutive_failures: 0,
    availability_recover_at: null,
    state_reason_code: null,
    state_reason_detail: null,
    health_checked_at: null,
    total_requests: 0,
    model_total: 0,
    model_available: 0,
    broken_model_count: 0,
    ...overrides,
  }
}

function fpStats(overrides: Partial<FpSlotStats> = {}): FpSlotStats {
  return {
    unlimited: false,
    slot_limit: 25,
    healthy_slots: 25,
    occupied_slots: 0,
    free_slots: 25,
    details: [],
    message: '',
    ...overrides,
  }
}

function mountPanel(monitor = makeMonitor(), providerId = 12763) {
  return mount(NodeDetailConcurrencyPanel, {
    props: {
      credentialId: 36,
      providerId,
      monitor,
      monitorLoading: false,
      canEdit: true,
    },
    global: {
      plugins: [i18n],
      stubs: { FpSlotVisualizer: true, Teleport: true },
    },
  })
}

beforeEach(() => {
  vi.clearAllMocks()
  updateCredential.mockResolvedValue({ success: true })
  setConcurrencyAuto.mockResolvedValue({ success: true })
  getCredentialFpSlotStats.mockResolvedValue(fpStats())
})

describe('NodeDetailConcurrencyPanel unified editor', () => {
  it('shows manual / auto / effective concurrency from monitor', () => {
    const w = mountPanel()
    expect(w.text()).toContain('手动并发')
    expect(w.text()).toContain('自动并发')
    expect(w.text()).toContain('生效并发')
    expect(w.text()).toContain('100') // manual
    expect(w.text()).toContain('8') // auto
  })

  it('merges the two old buttons into a single "调整并发与槽位" button', () => {
    const w = mountPanel()
    // old button labels must be gone
    expect(w.text()).not.toContain('调整自动并发')
    expect(w.text()).not.toContain('修改槽位上限')
    const btn = w.findAll('button').find((b) => b.text().includes('调整并发与槽位'))
    expect(btn).toBeTruthy()
  })

  it('opens the unified dialog and seeds drafts from monitor + fp stats', async () => {
    const w = mountPanel()
    await flushPromises()
    await w.findAll('button').find((b) => b.text().includes('调整并发与槽位'))!.trigger('click')
    await flushPromises()
    expect(w.find('.nd-dialog').exists()).toBe(true)
    // manual=100, auto=8, fp=25 (from fpStats.slot_limit)
    const inputs = w.findAll('input[type="number"]')
    expect(inputs.map((i) => (i.element as HTMLInputElement).value)).toEqual(['100', '8', '25'])
  })

  it('saves manual + fp via updateCredential and skips setConcurrencyAuto when auto unchanged', async () => {
    const w = mountPanel()
    await flushPromises()
    await w.findAll('button').find((b) => b.text().includes('调整并发与槽位'))!.trigger('click')
    await flushPromises()
    // change manual 100 -> 50, fp 25 -> 10, leave auto at 8
    const inputs = w.findAll('input[type="number"]')
    await inputs[0].setValue(50)
    await inputs[2].setValue(10)
    await w.find('input[placeholder="请输入原因"]').setValue('调低并发')
    await w.findAll('button').find((b) => b.text().includes('确认'))!.trigger('click')
    await flushPromises()
    expect(updateCredential).toHaveBeenCalledTimes(1)
    expect(updateCredential).toHaveBeenCalledWith(12763, 36, { concurrency_limit: 50, fp_slot_limit: 10 })
    expect(setConcurrencyAuto).not.toHaveBeenCalled()
    expect(w.emitted('saved')).toBeTruthy()
  })

  it('also calls setConcurrencyAuto when auto concurrency changes', async () => {
    const w = mountPanel()
    await flushPromises()
    await w.findAll('button').find((b) => b.text().includes('调整并发与槽位'))!.trigger('click')
    await flushPromises()
    const inputs = w.findAll('input[type="number"]')
    await inputs[1].setValue(12) // auto 8 -> 12
    await w.find('input[placeholder="请输入原因"]').setValue('调高自动并发')
    await w.findAll('button').find((b) => b.text().includes('确认'))!.trigger('click')
    await flushPromises()
    expect(setConcurrencyAuto).toHaveBeenCalledTimes(1)
    expect(setConcurrencyAuto).toHaveBeenCalledWith(36, 12, '调高自动并发')
    expect(updateCredential).not.toHaveBeenCalled()
  })

  it('rejects empty reason', async () => {
    const w = mountPanel()
    await flushPromises()
    await w.findAll('button').find((b) => b.text().includes('调整并发与槽位'))!.trigger('click')
    await flushPromises()
    await w.findAll('button').find((b) => b.text().includes('确认'))!.trigger('click')
    await flushPromises()
    expect(updateCredential).not.toHaveBeenCalled()
    expect(setConcurrencyAuto).not.toHaveBeenCalled()
    expect(w.emitted('error')).toBeTruthy()
  })

  it('rejects fp slot exceeding manual concurrency', async () => {
    const w = mountPanel()
    await flushPromises()
    await w.findAll('button').find((b) => b.text().includes('调整并发与槽位'))!.trigger('click')
    await flushPromises()
    const inputs = w.findAll('input[type="number"]')
    await inputs[0].setValue(5) // manual
    await inputs[2].setValue(20) // fp > manual
    await w.find('input[placeholder="请输入原因"]').setValue('违规值')
    await w.findAll('button').find((b) => b.text().includes('确认'))!.trigger('click')
    await flushPromises()
    expect(updateCredential).not.toHaveBeenCalled()
    expect(w.emitted('error')).toBeTruthy()
  })

  it('rejects auto concurrency < 1', async () => {
    const w = mountPanel()
    await flushPromises()
    await w.findAll('button').find((b) => b.text().includes('调整并发与槽位'))!.trigger('click')
    await flushPromises()
    const inputs = w.findAll('input[type="number"]')
    await inputs[1].setValue(0) // auto < 1
    await w.find('input[placeholder="请输入原因"]').setValue('自动并发过低')
    await w.findAll('button').find((b) => b.text().includes('确认'))!.trigger('click')
    await flushPromises()
    expect(setConcurrencyAuto).not.toHaveBeenCalled()
    expect(w.emitted('error')).toBeTruthy()
  })
})

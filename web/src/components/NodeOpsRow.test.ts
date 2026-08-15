// NodeOpsRow — 2026-08-15 OBS-FE2（26号 §2/§6、25号 §4）组件守卫：
//   1) 测试 → POST /api/admin/providers/{id}/test-now（Bearer 头）。
//   2) 强制启用 → emergencyRepair(force_enable, raw_model='')（整凭据修复）。
//   3) 手工禁用下拉三动作：force_disable（需 confirm）/ clear_circuit /
//      reset_errors。
//   4) BE4 投影字段（disable_kind/fp_disabled_until/system_recover_at）
//      缺省不渲染。
// api/routing 以 vi.mock 替身；fetch 以 stubGlobal 替身（NodeStatusMatrix
// 测试同款模式）。

import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import NodeOpsRow from './NodeOpsRow.vue'
import type { LiveNodeStatus } from '../composables/liveStreamStore'
import { emergencyRepair } from '../api/routing'

vi.mock('../api/routing', () => ({
  emergencyRepair: vi.fn().mockResolvedValue({ message: 'ok' }),
}))
vi.mock('../store', () => ({ authBearer: () => 'test-token' }))

const emergencyRepairMock = vi.mocked(emergencyRepair)

function baseNode(overrides: Partial<LiveNodeStatus> = {}): LiveNodeStatus {
  return {
    credential_id: 9,
    provider_id: 4,
    provider_code: 'anthropic',
    manual_disabled: false,
    circuit_state: 'closed',
    ...overrides,
  }
}

function mountRow(node: LiveNodeStatus) {
  return mount(NodeOpsRow, { props: { node }, attachTo: document.body })
}

beforeEach(() => {
  emergencyRepairMock.mockClear()
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
    ok: true,
    json: async () => ({ status: 'ok', latency_ms: 231 }),
  }))
  vi.stubGlobal('confirm', vi.fn(() => true))
})

afterEach(() => {
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe('NodeOpsRow (OBS-FE2)', () => {
  it('tests the node via POST /api/admin/providers/{id}/test-now with bearer auth', async () => {
    const wrapper = mountRow(baseNode())
    await wrapper.find('.nor-btn:not(.nor-btn--primary)').trigger('click')

    expect(fetch).toHaveBeenCalledWith('/api/admin/providers/4/test-now', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer test-token' },
    })
    await vi.waitFor(() => {
      expect(wrapper.text()).toContain('231ms')
    })
    wrapper.unmount()
  })

  it('force-enables via emergencyRepair with whole-credential scope', async () => {
    const wrapper = mountRow(baseNode())
    await wrapper.find('.nor-btn--primary').trigger('click')

    await vi.waitFor(() => {
      expect(emergencyRepairMock).toHaveBeenCalledWith({
        credential_id: 9,
        raw_model: '',
        action: 'force_enable',
        reason: 'force_enable from queue perspective node ops row',
      })
    })
    expect(wrapper.text()).toContain('已强制启用')
    wrapper.unmount()
  })

  it('offers the three dropdown actions and maps them to emergency repairs', async () => {
    const wrapper = mountRow(baseNode())
    await wrapper.find('.nor-menu > button').trigger('click')

    const items = wrapper.findAll('.nor-menu-item')
    expect(items.map(i => i.text())).toEqual(['手工禁用', '清除熔断', '重置错误'])

    // 手工禁用（confirm 已放行）→ force_disable
    await items[0].trigger('click')
    await vi.waitFor(() => {
      expect(emergencyRepairMock).toHaveBeenCalledWith(
        expect.objectContaining({ credential_id: 9, action: 'force_disable' }),
      )
    })

    emergencyRepairMock.mockClear()
    await wrapper.find('.nor-menu > button').trigger('click')
    const items2 = wrapper.findAll('.nor-menu-item')
    await items2[1].trigger('click')
    await vi.waitFor(() => {
      expect(emergencyRepairMock).toHaveBeenCalledWith(
        expect.objectContaining({ action: 'clear_circuit' }),
      )
    })
    wrapper.unmount()
  })

  it('skips force_disable when the confirm dialog is rejected', async () => {
    vi.mocked(globalThis.confirm).mockReturnValueOnce(false)
    const wrapper = mountRow(baseNode())
    await wrapper.find('.nor-menu > button').trigger('click')
    await wrapper.findAll('.nor-menu-item')[0].trigger('click')

    expect(emergencyRepairMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('renders BE4 disable projections only when reported', () => {
    // 时间按本地时区渲染（展示口径），期望值从同一 Date 推导。
    const localHM = (iso: string) => {
      const t = new Date(iso)
      const pad = (n: number) => String(n).padStart(2, '0')
      return `${pad(t.getHours())}:${pad(t.getMinutes())}:${pad(t.getSeconds())}`
    }

    const plain = mountRow(baseNode())
    expect(plain.find('.nor-disable-kind').exists()).toBe(false)
    expect(plain.find('.nor-recover').exists()).toBe(false)
    plain.unmount()

    const disabled = mountRow(baseNode({
      manual_disabled: true,
      disable_kind: 'manual',
      fp_disabled_until: '2026-08-15T12:30:00Z',
    }))
    expect(disabled.text()).toContain('手工禁用')
    expect(disabled.text()).toContain(`禁用至 ${localHM('2026-08-15T12:30:00Z')}`)
    disabled.unmount()

    const system = mountRow(baseNode({
      disable_kind: 'system',
      system_recover_at: '2026-08-15T12:05:30Z',
    }))
    expect(system.text()).toContain('系统降级')
    expect(system.text()).toContain(`恢复 ${localHM('2026-08-15T12:05:30Z')}`)
    system.unmount()
  })
})

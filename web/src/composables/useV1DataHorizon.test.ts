// useV1DataHorizon.test.ts — 告示三态的判据（审计 §9.73.7）。
//
// 重点是第三态：**取失败不能被当成「未停更」**。
// 那是这一族最危险的写法：停写期间告示端点恰好挂了，页面照常展示
// 停写前的数字，而没有任何东西在提示——与 silently_frozen 同一失效形态。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import {
  useV1DataHorizon,
  refreshV1DataHorizon,
  __resetV1DataHorizonForTests,
} from './useV1DataHorizon'

vi.mock('../api/v1DataHorizon', () => ({
  getV1DataHorizon: vi.fn(),
}))

import { getV1DataHorizon } from '../api/v1DataHorizon'
const mockGet = vi.mocked(getV1DataHorizon)

const noticeFixture = {
  frozen: true as const,
  unknown: false as const,
  source: 'request_logs',
  gate_key: 'storage.request_logs_write_enabled',
  effect: '本页数据只反映停写之前的流量。',
  silence: '接口没报错不代表数据是新的。',
  affects: ['silently_frozen'],
}

describe('useV1DataHorizon', () => {
  beforeEach(() => {
    __resetV1DataHorizonForTests()
    mockGet.mockReset()
  })

  it('初始为 unknown，且此时不显示横幅', async () => {
    const { state, shouldShowBanner } = useV1DataHorizon()
    expect(state.value).toBe('unknown')
    // 页面刚起来那几毫秒闪一条「数据已停更」比不闪更糟。
    expect(shouldShowBanner()).toBe(false)
  })

  it('键为 null ⇒ live，不显示横幅', async () => {
    mockGet.mockResolvedValue({ v1_data_horizon: null, '//': '' })
    await refreshV1DataHorizon()
    const { state, shouldShowBanner } = useV1DataHorizon()
    expect(state.value).toBe('live')
    expect(shouldShowBanner()).toBe(false)
  })

  it('有告示 ⇒ frozen，显示横幅', async () => {
    mockGet.mockResolvedValue({ v1_data_horizon: noticeFixture, '//': '' })
    await refreshV1DataHorizon()
    const { state, notice, shouldShowBanner } = useV1DataHorizon()
    expect(state.value).toBe('frozen')
    expect(shouldShowBanner()).toBe(true)
    expect(notice.value?.gate_key).toBe('storage.request_logs_write_enabled')
  })

  it('★ 取失败 ⇒ failed 且**仍然显示横幅**（不得当成 live）', async () => {
    mockGet.mockRejectedValue(new Error('network down'))
    await refreshV1DataHorizon()
    const { state, notice, shouldShowBanner } = useV1DataHorizon()
    expect(state.value).toBe('failed')
    expect(shouldShowBanner()).toBe(true)
    expect(notice.value).toBeNull()
  })

  it('★ 响应里根本没有 v1_data_horizon 键 ⇒ 按 frozen 处理，不得当成 live', async () => {
    // 后端哪天把键整个省掉时，前端不能因此变成「未停更」。
    mockGet.mockResolvedValue({ '//': '' } as never)
    await refreshV1DataHorizon()
    const { state, shouldShowBanner } = useV1DataHorizon()
    expect(state.value).toBe('frozen')
    expect(shouldShowBanner()).toBe(true)
  })

  it('★ 并发调用只发一次请求（App.vue 与横幅组件都会调）', async () => {
    let resolveFn: (v: unknown) => void = () => {}
    mockGet.mockReturnValue(new Promise((res) => { resolveFn = res }) as never)
    const a = refreshV1DataHorizon()
    const b = refreshV1DataHorizon()
    resolveFn({ v1_data_horizon: noticeFixture, '//': '' })
    await Promise.all([a, b])
    expect(mockGet).toHaveBeenCalledTimes(1)
  })

  it('★ unknown 态（后端读到回落值）⇒ unconfirmed，绝不是 live', async () => {
    // §9.79.8：写门读点 GetPlatformBool 的三个回落点全部返回 fallback(true)，
    // 「读到 true」既可能是 DB 真值也可能根本没读到。后端用 source 区分后
    // 报成 unknown。前端若归 live，就等于把「不知道」显示成「数据是新的」。
    mockGet.mockResolvedValue({
      v1_data_horizon: {
        ...noticeFixture,
        frozen: false,
        unknown: true,
        effect: '无法确认 v1 读源族的停写状态：该 gate 键在配置里没有显式取值。',
      },
      '//': '',
    } as never)
    await refreshV1DataHorizon()
    const { state, shouldShowBanner, notice } = useV1DataHorizon()
    expect(state.value).toBe('unconfirmed')
    expect(shouldShowBanner()).toBe(true)
    // 文案必须透出「不知道」，而不是后端那句「已停更」的 effect。
    expect(notice.value?.effect).toContain('无法确认')
  })

  it('★ unknown 态不得被显示成「已停更」——判据要先判 unknown 再判 frozen', async () => {
    // unknown 态的 frozen 是 false。若判据写成 `notice !== null ⇒ frozen`，
    // 它会显示成「已停更」——那是反向的另一种编造。
    mockGet.mockResolvedValue({
      v1_data_horizon: { ...noticeFixture, frozen: false, unknown: true },
      '//': '',
    } as never)
    await refreshV1DataHorizon()
    expect(useV1DataHorizon().state.value).toBe('unconfirmed')
  })

  it('★ unknown 判据必须真的存在：删掉它会让 unknown 态「巧合地」判对', async () => {
    // 变异 F4 实录：删掉 `if (n.unknown)` 那一段，**10 个用例全绿**。
    // 原因：兜底那行 `state.value = n.frozen ? 'frozen' : 'failed'`
    // 在 unknown 态（frozen=false）下恰好也落到 'failed' ⇒ 巧合正确。
    //
    // ⇒ 需要一条**只有 unknown 判据能给**的行为。
    // 这里用「unknown 态必须透出 unknown 标记」：删掉判据的实现里
    // state 与 notice 的组合会与 frozen 态无法区分（见下一条用例的注释）。
    // 本条钉住：unknown 态下 notice 保留原样（不被清成 null）。
    mockGet.mockResolvedValue({
      v1_data_horizon: { ...noticeFixture, frozen: false, unknown: true, effect: 'UNSETTLED' },
      '//': '',
    } as never)
    await refreshV1DataHorizon()
    const { notice, state } = useV1DataHorizon()
    expect(state.value).toBe('unconfirmed')
    expect(notice.value).not.toBeNull()
    expect(notice.value?.unknown).toBe(true)
  })

  it('★ unknown 态与 frozen 态在对外状态上必须不同（防「巧合落到同一分支」）', async () => {
    // 若某个实现让两者落到同一分支（例如都变成 frozen，或都变成 failed 且
    // 靠文案区分），横幅就分不清「已停更」和「不知道」——而 §9.79.8 的全部
    // 意义就是这两者必须可区分。这里把「可区分」写成可执行断言：
    // frozen 态的 notice.unknown === false，unknown 态 === true，
    // 且两者 state 不同。
    mockGet.mockResolvedValue({
      v1_data_horizon: { ...noticeFixture, frozen: true, unknown: false },
      '//': '',
    } as never)
    await refreshV1DataHorizon()
    const f = useV1DataHorizon()
    const frozenState = f.state.value
    const frozenUnknown = f.notice.value?.unknown
    expect(frozenState).toBe('frozen')
    expect(frozenUnknown).toBe(false)

    __resetV1DataHorizonForTests()
    mockGet.mockResolvedValue({
      v1_data_horizon: { ...noticeFixture, frozen: false, unknown: true },
      '//': '',
    } as never)
    await refreshV1DataHorizon()
    const u = useV1DataHorizon()
    expect(u.state.value).not.toBe(frozenState)
    expect(u.notice.value?.unknown).toBe(true)
  })

  it('★ 显式 unknown:false 的 frozen:true ⇒ 仍是 frozen（第三态没有污染既有两态）', async () => {
    mockGet.mockResolvedValue({ v1_data_horizon: noticeFixture, '//': '' } as never)
    await refreshV1DataHorizon()
    expect(useV1DataHorizon().state.value).toBe('frozen')
  })

  it('失败的这次不缓存：修好之后 refresh 能拿到 frozen', async () => {
    mockGet.mockRejectedValueOnce(new Error('boom'))
    await refreshV1DataHorizon()
    expect(useV1DataHorizon().state.value).toBe('failed')

    mockGet.mockResolvedValueOnce({ v1_data_horizon: noticeFixture, '//': '' })
    await refreshV1DataHorizon()
    expect(useV1DataHorizon().state.value).toBe('frozen')
  })
})

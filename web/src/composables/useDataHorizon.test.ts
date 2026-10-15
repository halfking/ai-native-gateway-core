// useDataHorizon.test.ts — 告示三态的判据（2026-10-15 r1033 重写）。
//
// 重点是第三态：**取失败不能被当成「健康」**。
// 那是这一族最危险的写法：断流期间告示端点恰好挂了，页面照常展示
// 滞后数字，而没有任何东西在提示——与断流本身同一失效形态。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import {
  useDataHorizon,
  refreshDataHorizon,
  __resetDataHorizonForTests,
} from './useDataHorizon'

vi.mock('../api/dataHorizon', () => ({
  getDataHorizon: vi.fn(),
}))

import { getDataHorizon } from '../api/dataHorizon'
const mockGet = vi.mocked(getDataHorizon)

const staleFixture = {
  stale: true as const,
  unknown: false as const,
  source: 'session_turns_hot',
  effect: '网关仍在接收请求，但 session_turns 未记录到新数据。',
  silence: '这类断流没有任何错误信号。',
  hint: '排查方向：session 镜像写入路径。',
}

describe('useDataHorizon', () => {
  beforeEach(() => {
    __resetDataHorizonForTests()
    mockGet.mockReset()
  })

  it('初始为 unknown，且此时不显示横幅', async () => {
    const { state, shouldShowBanner } = useDataHorizon()
    expect(state.value).toBe('unknown')
    // 页面刚起来那几毫秒闪一条「数据断流」比不闪更糟。
    expect(shouldShowBanner()).toBe(false)
  })

  it('键为 null ⇒ healthy（含网关空闲），不显示横幅', async () => {
    mockGet.mockResolvedValue({ data_horizon: null, '//': '' })
    await refreshDataHorizon()
    const { state, shouldShowBanner } = useDataHorizon()
    expect(state.value).toBe('healthy')
    expect(shouldShowBanner()).toBe(false)
  })

  it('有告示 ⇒ stale，显示横幅', async () => {
    mockGet.mockResolvedValue({ data_horizon: staleFixture, '//': '' })
    await refreshDataHorizon()
    const { state, notice, shouldShowBanner } = useDataHorizon()
    expect(state.value).toBe('stale')
    expect(shouldShowBanner()).toBe(true)
    expect(notice.value?.source).toBe('session_turns_hot')
  })

  it('★ 取失败 ⇒ failed 且**仍然显示横幅**（不得当成 healthy）', async () => {
    mockGet.mockRejectedValue(new Error('network down'))
    await refreshDataHorizon()
    const { state, notice, shouldShowBanner } = useDataHorizon()
    expect(state.value).toBe('failed')
    expect(shouldShowBanner()).toBe(true)
    expect(notice.value).toBeNull()
  })

  it('★ 响应里根本没有 data_horizon 键 ⇒ 按 stale 处理，不得当成 healthy', async () => {
    // 后端哪天把键整个省掉时，前端不能因此变成「健康」。
    mockGet.mockResolvedValue({ '//': '' } as never)
    await refreshDataHorizon()
    const { state, shouldShowBanner } = useDataHorizon()
    expect(state.value).toBe('stale')
    expect(shouldShowBanner()).toBe(true)
  })

  it('★ 并发调用只发一次请求（App.vue 与横幅组件都会调）', async () => {
    let resolveFn: (v: unknown) => void = () => {}
    mockGet.mockReturnValue(new Promise((res) => { resolveFn = res }) as never)
    const a = refreshDataHorizon()
    const b = refreshDataHorizon()
    resolveFn({ data_horizon: staleFixture, '//': '' })
    await Promise.all([a, b])
    expect(mockGet).toHaveBeenCalledTimes(1)
  })

  it('★ 后端 unknown 态（读不到 session 数据）⇒ failed，绝不是 healthy', async () => {
    // 「不知道」≠「数据是新的」。后端把读不到报成 unknown:true + stale:false，
    // 前端若归 healthy，就等于把「不知道」显示成「数据是新的」。
    mockGet.mockResolvedValue({
      data_horizon: {
        ...staleFixture,
        stale: false,
        unknown: true,
        effect: '无法确认流量数据状态：session_turns 读取失败。',
      },
      '//': '',
    } as never)
    await refreshDataHorizon()
    const { state, shouldShowBanner, notice } = useDataHorizon()
    expect(state.value).toBe('failed')
    expect(shouldShowBanner()).toBe(true)
    // notice 保留原样（不被清成 null）：unknown 判据不是冗余分支。
    expect(notice.value?.unknown).toBe(true)
  })

  it('★ unknown 态不得被显示成「确证断流」——判据要先判 unknown 再判 stale', async () => {
    // unknown 态的 stale 是 false。若判据写成 `notice !== null ⇒ stale`，
    // 它会显示成「确证断流」——那是反向的另一种编造。
    mockGet.mockResolvedValue({
      data_horizon: { ...staleFixture, stale: false, unknown: true },
      '//': '',
    } as never)
    await refreshDataHorizon()
    const { state, shouldShowBanner } = useDataHorizon()
    expect(state.value).toBe('failed')
    // 两态都显示横幅（安全方向相同），但状态必须可区分——
    // 这是横幅文案分流的依据（stale=警示色+后端三句；failed=危险色+failedHint）。
    expect(shouldShowBanner()).toBe(true)
  })

  it('★ unknown 态与 stale 态在对外状态上必须不同（防「巧合落到同一分支」', async () => {
    mockGet.mockResolvedValue({ data_horizon: staleFixture, '//': '' } as never)
    await refreshDataHorizon()
    const s = useDataHorizon()
    expect(s.state.value).toBe('stale')
    // ⚠ 先把字符串快照下来再刷新第二轮——state 是单例 ref，
    // 直接拿 s.state.value 比较会在第二轮刷新后变成「自己跟自己比」。
    const staleState = s.state.value
    const staleUnknown = s.notice.value?.unknown
    expect(staleUnknown).toBe(false)

    __resetDataHorizonForTests()
    mockGet.mockResolvedValue({
      data_horizon: { ...staleFixture, stale: false, unknown: true },
      '//': '',
    } as never)
    await refreshDataHorizon()
    const u = useDataHorizon()
    expect(u.state.value).not.toBe(staleState)
    expect(u.notice.value?.unknown).toBe(true)
  })

  it('失败的这次不缓存：修好之后 refresh 能拿到 stale', async () => {
    mockGet.mockRejectedValueOnce(new Error('boom'))
    await refreshDataHorizon()
    expect(useDataHorizon().state.value).toBe('failed')

    mockGet.mockResolvedValueOnce({ data_horizon: staleFixture, '//': '' })
    await refreshDataHorizon()
    expect(useDataHorizon().state.value).toBe('stale')
  })
})

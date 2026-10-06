import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import SystemMonitorView from './SystemMonitorView.vue'
import { fetchSystemMonitorStats, fetchRecentProbeRuns } from '@/api/systemMonitor'
import { setLocale, locale } from '@/i18n'

/**
 * SystemMonitorView 的八条不变量（2026-10-06）。
 *
 * 1. ★★★ 两个端点**独立**取：stats 失败不清空明细，明细失败不清空 stats；
 * 2. ★★★ 近 1 小时四项**全 0** ⇒ 必须提示「可能查询失败」（与「零次」无法区分）；
 * 3. ★★★ 三项之和**不是**总数：`expired` 不被任何一项统计；
 * 4. ★★ 队列与运行**同时为 0** ⇒ 提示可能是「监控器未接线」；
 * 5. ★★ 并发**恰好等于兜底值 5** ⇒ 提示该值可能来自读配置失败；
 * 6. ★ `limit` 回显 ⇒ 撞上限是精确信号；
 * 7. ★★ 清单**只含已结束的运行** ⇒ 说明常驻；
 * 8. ★★ `http_status` / `latency_ms` 键缺失 ⇒ 显示「—」，不是 0。
 */

vi.mock('@/hyper', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/hyper')>()
  return { ...actual, useHyperPage: () => {} }
})

vi.mock('@/api/systemMonitor', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/systemMonitor')>()
  return { ...actual, fetchSystemMonitorStats: vi.fn(), fetchRecentProbeRuns: vi.fn() }
})

const statsMock = fetchSystemMonitorStats as unknown as ReturnType<typeof vi.fn>
const runsMock = fetchRecentProbeRuns as unknown as ReturnType<typeof vi.fn>

const ORIGIN_LOCALE = locale.value
let mountedList: Array<{ unmount(): void }> = []

async function mountView(): Promise<ReturnType<typeof mount>> {
  const pinia = createPinia()
  setActivePinia(pinia)
  const w = mount(SystemMonitorView, { attachTo: document.body, global: { plugins: [pinia] } })
  mountedList.push(w)
  await flushPromises()
  await flushPromises()
  return w
}

function stats(over: Record<string, unknown> = {}) {
  return {
    queue_size: 3,
    running_size: 1,
    in_fallback: false,
    monitor_concurrency: 8,
    completed_total_1h: 10,
    failed_total_1h: 2,
    skipped_total_1h: 1,
    total_tokens_1h: 5000,
    snapshot_at: '2026-10-07T10:00:00Z',
    ...over,
  }
}

function run(over: Record<string, unknown> = {}) {
  return {
    id: 1,
    task_id: 9,
    task_type: 'chat',
    automaticity: 'mandatory',
    credential_id: 7,
    raw_model: 'gpt-4o',
    source: 'system_monitor',
    worker_id: 'w1',
    status: 'success',
    attempt: 1,
    http_status: 200,
    latency_ms: 820,
    err_code: '',
    skip_reason: '',
    started_at: '2026-10-07T09:59:00Z',
    finished_at: '2026-10-07T10:00:00Z',
    recent_request_id: 'req-1',
    ...over,
  }
}

function runsResp(over: Record<string, unknown> = {}) {
  return { total: 1, limit: 50, runs: [run()], ...over }
}

beforeEach(() => {
  setLocale('zh-CN')
  vi.clearAllMocks()
  statsMock.mockResolvedValue(stats())
  runsMock.mockResolvedValue(runsResp())
  document.body.innerHTML = ''
})
afterEach(() => {
  for (const w of mountedList) {
    try {
      w.unmount()
    } catch {
      /* ignore */
    }
  }
  mountedList = []
  document.body.innerHTML = ''
  setLocale(ORIGIN_LOCALE as 'zh-CN' | 'en-US')
})

describe('入口', () => {
  it('挂载即请求两个端点，limit=50', async () => {
    await mountView()
    expect(statsMock).toHaveBeenCalledTimes(1)
    expect(runsMock).toHaveBeenCalledTimes(1)
    expect(runsMock.mock.calls[0]![0]).toEqual({ limit: 50 })
  })

  it('★ 切 limit chip ⇒ 带新值重查', async () => {
    const w = await mountView()
    await w.findAll('button').find((b) => b.text() === '前 100')!.trigger('click')
    await flushPromises()
    expect(runsMock.mock.calls[1]![0]).toEqual({ limit: 100 })
  })
})

describe('★★★ 判据 1：两端点独立', () => {
  it('★★ stats 失败 ⇒ 明细**照样显示**', async () => {
    statsMock.mockRejectedValueOnce(new Error('stats boom'))
    const w = await mountView()
    expect(w.text()).toContain('stats boom')
    expect(w.text()).toContain('gpt-4o')
  })

  it('★★ 明细失败 ⇒ stats **照样显示**', async () => {
    runsMock.mockRejectedValueOnce(new Error('runs boom'))
    const w = await mountView()
    expect(w.text()).toContain('runs boom')
    expect(w.text()).toContain('快照时间')
  })

  it('★ 一个失败时另一个的错误不串台', async () => {
    statsMock.mockRejectedValueOnce(new Error('S-ERR'))
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('S-ERR')
    expect(text).not.toContain('S-ERR 出现在明细')
  })
})

describe('★★★ 判据 2/3：四项全 0 与「不是总数」', () => {
  it('★★ 四项全 0 ⇒ 提示「可能查询失败」', async () => {
    statsMock.mockResolvedValue(
      stats({ completed_total_1h: 0, failed_total_1h: 0, skipped_total_1h: 0, total_tokens_1h: 0 }),
    )
    const w = await mountView()
    expect(w.text()).toContain('可能是这一小时的查询失败')
  })

  it('★ 有任一非 0 ⇒ 不提示（证明不是恒真）', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('可能是这一小时的查询失败')
  })

  it('★★★ 常驻说明「三项合计不是总数 + expired 不被统计」', async () => {
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('以上三项合计 13 次')
    expect(text).toContain('这不是一小时的总运行数')
    expect(text).toContain('expired')
  })

  it('★★ expired 行 ⇒ 单独标出未被统计', async () => {
    runsMock.mockResolvedValue(runsResp({ total: 1, runs: [run({ status: 'expired' })] }))
    const w = await mountView()
    expect(w.text()).toContain('不会计入上面「完成 / 失败 / 跳过」中的任何一项')
  })

  it('★ 非 expired 行 ⇒ 不出现该标注', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('不会计入上面')
  })
})

describe('★★ 判据 4/5：没接线与兜底并发', () => {
  it('★★ 队列与运行同时 0 ⇒ 提示可能未接线', async () => {
    statsMock.mockResolvedValue(stats({ queue_size: 0, running_size: 0 }))
    const w = await mountView()
    expect(w.text()).toContain('可能是「监控器未接线」')
  })

  it('★ 有一个非 0 ⇒ 不提示', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('可能是「监控器未接线」')
  })

  it('★★ 并发 == 5 ⇒ 提示该值可能来自读配置失败', async () => {
    statsMock.mockResolvedValue(stats({ monitor_concurrency: 5 }))
    const w = await mountView()
    expect(w.text()).toContain('恰好等于兜底值 5')
  })

  it('★ 并发 != 5 ⇒ 不提示', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('恰好等于兜底值')
  })

  it('★ in_fallback ⇒ 显示降级提示', async () => {
    statsMock.mockResolvedValue(stats({ in_fallback: true }))
    const w = await mountView()
    expect(w.text()).toContain('正在降级运行')
  })

  it('★ in_fallback=false ⇒ 不提示降级', async () => {
    const w = await mountView()
    expect(w.text()).not.toContain('正在降级运行')
  })
})

describe('★ 判据 6/7：截断与「只含已结束」', () => {
  it('★ total === 回显 limit ⇒ 提示截断', async () => {
    runsMock.mockResolvedValue({ total: 50, limit: 50, runs: [run()] })
    const w = await mountView()
    expect(w.text()).toContain('后面还有更多记录')
  })

  it('★ total < limit ⇒ 显示正常计数', async () => {
    const w = await mountView()
    expect(w.text()).toContain('共 1 条记录')
    expect(w.text()).not.toContain('后面还有更多记录')
  })

  it('★★ 「只含已结束的运行」说明常驻（空列表时也在）', async () => {
    runsMock.mockResolvedValue({ total: 0, limit: 50, runs: [] })
    const w = await mountView()
    expect(w.text()).toContain('进行中的探测不会出现在这里')
  })
})

describe('★ 判据 8：可空键与用时', () => {
  it('★★ http_status / latency_ms 键缺失 ⇒ 「—」', async () => {
    runsMock.mockResolvedValue(runsResp({ runs: [run({ http_status: undefined, latency_ms: undefined })] }))
    const w = await mountView()
    const cells = w.findAll('.sm__kv-item').map((e) => e.text())
    expect(cells.some((c) => c.includes('HTTP') && c.includes('—'))).toBe(true)
    expect(cells.some((c) => c.includes('延时') && c.includes('—'))).toBe(true)
    expect(cells.some((c) => c.includes('延时') && c.includes('0ms'))).toBe(false)
  })

  it('★ 有值 ⇒ 正常显示', async () => {
    const w = await mountView()
    expect(w.text()).toContain('200')
    expect(w.text()).toContain('820ms')
  })

  it('★ 用时按起止时间差算（60 秒 ⇒ 60.0s）', async () => {
    runsMock.mockResolvedValue(
      runsResp({ runs: [run({ started_at: '2026-10-07T09:59:00Z', finished_at: '2026-10-07T10:00:00Z' })] }),
    )
    const w = await mountView()
    expect(w.text()).toContain('60.0s')
  })

  it('★ 亚秒用时 ⇒ 显示 ms', async () => {
    runsMock.mockResolvedValue(
      runsResp({ runs: [run({ started_at: '2026-10-07T09:59:59.900Z', finished_at: '2026-10-07T10:00:00Z' })] }),
    )
    const w = await mountView()
    expect(w.text()).toContain('100ms')
  })

  it('★ 错误码与跳过原因按需显示', async () => {
    runsMock.mockResolvedValue(runsResp({ runs: [run({ err_code: 'ETIMEDOUT', skip_reason: '' })] }))
    const a = await mountView()
    expect(a.text()).toContain('ETIMEDOUT')
    runsMock.mockResolvedValue(runsResp({ runs: [run({ err_code: '', skip_reason: 'disabled' })] }))
    const b = await mountView()
    expect(b.text()).toContain('disabled')
  })
})

describe('图例与空态', () => {
  it('★★★ 图例常驻且三条口径一次说清', async () => {
    const w = await mountView()
    const text = w.text()
    expect(text).toContain('这一页刻意保留的三条口径说明')
    expect(text).toContain('「没接线」「读不到配置」的产物')
    expect(text).toContain('不含 expired')
    expect(text).toContain('不是数据库里的总记录数')
  })

  it('★ 空列表时不显示图例', async () => {
    runsMock.mockResolvedValue({ total: 0, limit: 50, runs: [] })
    const w = await mountView()
    expect(w.find('.sm__legend').exists()).toBe(false)
  })

  it('空列表 ⇒ 空态文案', async () => {
    runsMock.mockResolvedValue({ total: 0, limit: 50, runs: [] })
    const w = await mountView()
    expect(w.text()).toContain('还没有已结束的探针运行')
  })
})
import { describe, it, expect, vi, afterEach } from 'vitest'
import { fetchMonitorSummary, unwrapMonitorSummary, type CredentialMonitorSummary } from '@/api/nodes'

/**
 * /api/credentials/monitor-summary 信封解包（2026-10-06）
 *
 * 后端 admin/credential_monitor.go handleMonitorSummary 返回
 *   { credentials: [...], count, meta }
 * 而 web-mobile 的 fetchMonitorSummary 原先声明 Promise<CredentialMonitorSummary[]>
 * 并把**整包**当数组返回 ⇒ NodesView 的 `cache.filter(...)` 在对象上调用
 * ⇒ TypeError ⇒ 页面进错误态。**接口一旦成功就必然渲染失败**，
 * 只是被后端 15s 超时 500 挡住了没暴露。
 *
 * 这些用例 stub fetch 走**真实入口** fetchMonitorSummary，不直接测辅助函数 ——
 * 只测辅助函数的写法在「fetchMe 回退成整包返回」时不会转红（恒绿假证据）。
 */

const CRED: CredentialMonitorSummary = {
  id: 7,
  provider_id: 3,
  provider_name: 'Volcano',
  label: 'sk-volc-001',
  status: 'ok',
  availability_state: 'available',
  health_status: 'healthy',
  quota_state: 'ok',
  effective_concurrency: 2,
  concurrency_limit: 5,
  manual_disabled: false,
  consecutive_failures: 0,
  state_reason_code: null,
  state_reason_detail: null,
  health_checked_at: null,
  total_requests: 120,
  model_total: 4,
  model_available: 4,
  broken_model_count: 0,
}

/** 与线上实测一致的载荷：顶层 dict，键 count/credentials/meta。 */
function envelope() {
  return {
    count: 2,
    credentials: [CRED, { ...CRED, id: 8, label: 'sk-volc-002' }],
    meta: {
      cache_hit: false,
      generated_at: '2026-10-06T05:22:14+08:00',
      expires_at: '2026-10-06T05:22:44+08:00',
      server_duration_ms: 186,
      ttl_seconds: 30,
    },
  }
}

function stubFetchOk(body: unknown) {
  // req() 走 r.text() 再 JSON.parse，不是 r.json() —— 手法抄 client.spec.ts:20
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => ({ ok: true, status: 200, text: async () => JSON.stringify(body) })),
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('unwrapMonitorSummary —— monitor-summary 信封', () => {
  it('取出 credentials 数组', () => {
    const got = unwrapMonitorSummary(envelope() as never)
    expect(Array.isArray(got)).toBe(true)
    expect(got).toHaveLength(2)
    expect(got[0]?.label).toBe('sk-volc-001')
  })

  it('空 credentials 也返空数组而不是崩', () => {
    expect(unwrapMonitorSummary({ count: 0, credentials: [] } as never)).toEqual([])
  })

  it('形状不符（裸数组）抛错，不静默返空', () => {
    // 静默返 [] 会让「接口没数据」和「解包失败」长得一样 —— 那才是更难查的缺陷
    expect(() => unwrapMonitorSummary([CRED] as never)).toThrow(/形状不符/)
  })

  it('形状不符（缺 credentials 键）抛错', () => {
    expect(() => unwrapMonitorSummary({ count: 1 } as never)).toThrow(/形状不符/)
  })
})

describe('fetchMonitorSummary —— 走真实入口', () => {
  it('返回的是 credentials 数组而不是信封对象（这条守的就是原缺陷）', async () => {
    stubFetchOk(envelope())
    const got = await fetchMonitorSummary()
    expect(Array.isArray(got)).toBe(true)
    expect(got).toHaveLength(2)
    expect(got[1]?.id).toBe(8)
  })

  it('调用方能直接 .filter() —— NodesView 的真实用法', async () => {
    stubFetchOk(envelope())
    const cache = await fetchMonitorSummary()
    // NodesView.filtered() 就是这么用的；在对象上调用会 TypeError
    const filtered = cache.filter((c) => c.provider_name.toLowerCase().includes('volc'))
    expect(filtered).toHaveLength(2)
  })

  it('形状违约时 promise reject，让视图走错误态而不是空列表', async () => {
    stubFetchOk([CRED]) // 后端若退化成裸数组
    await expect(fetchMonitorSummary()).rejects.toThrow(/形状不符/)
  })

  it('列表请求必须带 mode=core', async () => {
    // 2026-10-06 实测：不带 mode 的全量列表 15.18s/15.19s 两次都撞满 15s 超时 → 500；
    // mode=core 冷查 0.90s。桌面 web 的列表（useCredentialLabels.ts:150）同款选择。
    const fetchMock = vi.fn(async (_url: string) => ({
      ok: true,
      status: 200,
      text: async () => JSON.stringify(envelope()),
    }))
    vi.stubGlobal('fetch', fetchMock)
    await fetchMonitorSummary()
    const url = String(fetchMock.mock.calls[0]?.[0] ?? '')
    expect(url).toContain('/api/credentials/monitor-summary')
    expect(url).toContain('mode=core')
  })
})

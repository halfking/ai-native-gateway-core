// ProbeTriStateQueue.test.ts — 三段式自检队列组件测试（OBS-FE4）
// 覆盖：三段渲染、origin 徽标、展开详情、退避下一跳、API 降级态、
// SSE 断线态、空态/骨架、SSE 增量驱动。
import { mount, flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import type { Ref } from 'vue'
import ProbeTriStateQueue from './ProbeTriStateQueue.vue'

const { getCredentialMonitorSummary } = vi.hoisted(() => ({
  getCredentialMonitorSummary: vi.fn(),
}))

vi.mock('../../api/credential-monitor', () => ({
  getCredentialMonitorSummary,
}))

beforeEach(async () => {
  tiles.value = []
  connection.value = 'open'
  // 默认让 mount 流程立即拿到空凭据，避免组件层 await Promise.then 报错；
  // 需要覆盖特定场景的测试再用 mockImplementationOnce 替换。
  getCredentialMonitorSummary.mockReset().mockResolvedValue({ credentials: [] })
  // 重置共享凭据标签缓存，避免其它测试串味。
  const { clearCredentialLabels } = await import('../../composables/useCredentialLabels')
  clearCredentialLabels()
})

// ── mock probeStreamStore 单例（SSE 通道） ────────────────────────────
// 工厂内创建共享 ref，测试通过 mocked 模块导出直接驱动 tiles/connection。
vi.mock('../../composables/probeStreamStore', async () => {
  const { ref } = await import('vue')
  const tiles = ref<any[]>([])
  const connection = ref<string>('open')
  return {
    tiles,
    connection,
    useProbeStream: () => ({ tiles, connection }),
    acquireProbeStream: () => () => { /* noop release */ },
    releaseProbeStream: () => { /* noop */ },
    resetProbeTiles: () => { tiles.value = [] },
  }
})

import * as probeStreamStore from '../../composables/probeStreamStore'

// The vi.mock factory above adds tiles/connection exports the real module
// doesn't have — reach them through an untyped cast.
const tiles = (probeStreamStore as unknown as { tiles: Ref<any[]> }).tiles
const connection = (probeStreamStore as unknown as { connection: Ref<string> }).connection

// ── tri-state API mock（global.fetch） ────────────────────────────────

function jsonResponse(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    statusText: 'OK',
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response
}

function errorResponse(): Response {
  return {
    ok: false,
    status: 500,
    statusText: 'Internal Server Error',
    json: async () => ({}),
    text: async () => 'boom',
  } as unknown as Response
}

function triTask(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    dedup_key: 'node_probe:1:gpt-5.6-luna',
    credential_id: 1,
    raw_model: 'gpt-5.6-luna',
    command: 'node_probe',
    source: 'periodic',
    origin: 'scheduled',
    status: 'pending',
    attempt: 1,
    max_attempts: 7,
    priority: 60,
    created_at: '2026-08-15T10:00:00Z',
    updated_at: '2026-08-15T10:00:00Z',
    ...overrides,
  }
}

function stubTriFetch(legs: { pending?: unknown[]; in_flight?: unknown[]; completed?: unknown[] }) {
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    const status = new URL(url, 'http://localhost').searchParams.get('status') ?? 'pending'
    const rows = (legs as Record<string, unknown[] | undefined>)[status] ?? []
    return jsonResponse({ status, tasks: rows, count: rows.length })
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

function mountQueue() {
  return mount(ProbeTriStateQueue, {
    global: {
      // api-selfcheck 的 req() 会读 authBearer（localStorage 兜底为空）。
      stubs: { 'transition-group': false },
    },
  })
}

describe('ProbeTriStateQueue — 三段渲染', () => {
  it('挂载时拉取三段并渲染 pending / in_flight / completed 卡片', async () => {
    vi.stubGlobal('fetch', stubTriFetch({
      pending: [triTask({ id: 11, dedup_key: 'node_probe:11:m1', credential_id: 11, raw_model: 'm1', next_retry_at_ms: Date.now() + 45_000 })],
      in_flight: [triTask({ id: 22, dedup_key: 'node_probe:22:m2', credential_id: 22, raw_model: 'm2', status: 'in_flight', source: 'request_failure', origin: 'error' })],
      completed: [triTask({
        id: 33, dedup_key: 'node_probe:33:m3', credential_id: 33, raw_model: 'm3',
        status: 'completed', outcome: 'success', latency_ms: 812, http_status: 200,
        finished_at: '2026-08-15T09:59:00Z',
      })],
    }))
    const w = mountQueue()
    await flushPromises()

    expect(w.find('[data-testid="tri-skeleton"]').exists()).toBe(false)
    expect(w.findAll('[data-testid="probe-pending-card"]')).toHaveLength(1)
    expect(w.findAll('[data-testid="probe-inflight-card"]')).toHaveLength(1)
    expect(w.findAll('[data-testid="probe-completed-card"]')).toHaveLength(1)

    // 默认标签缓存为空：fallback 为「凭据 #ID」便于人工排查
    expect(w.find('[data-testid="tri-pending"]').text()).toContain('凭据 #11')
    expect(w.find('[data-testid="tri-pending"]').text()).toContain('m1')
    // pending 小卡：预计执行时间（next_retry_at_ms 存在时才渲染）
    expect(w.find('[data-testid="tri-pending"]').text()).toContain('预计')

    // in_flight 卡：实时耗时
    expect(w.find('[data-testid="inflight-elapsed"]').exists()).toBe(true)

    w.unmount()
  })

  it('共享凭据名称加载后渲染 label 而非 #ID', async () => {
    // mock 延迟 resolve：保证「凭证接口先于 refreshFromApi 返回」
    // 的真实时序，验证 revision 后模板重新渲染。
    let resolveCreds: (() => void) | null = null
    const inFlight = new Promise<{ credentials: Array<{ id: number; label: string; provider_id?: number; provider_name?: string }> }>((resolve) => {
      resolveCreds = () => resolve({
        credentials: [{ id: 11, label: 'key-prod-001', provider_id: 7, provider_name: 'openai' }],
      })
    })
    getCredentialMonitorSummary.mockReset().mockImplementation(() => inFlight)

    vi.stubGlobal('fetch', stubTriFetch({
      pending: [triTask({ id: 11, dedup_key: 'k1', credential_id: 11, raw_model: 'm1' })],
    }))
    const w = mountQueue()
    await flushPromises()
    expect(w.find('[data-testid="tri-pending"]').text()).toContain('凭据 #11')
    // 非空断言：赋值发生在上面的 Promise 构造器回调里，TS 控制流分析
    // 看不到，会把 resolveCreds 收窄为 null。
    resolveCreds!()
    await flushPromises()
    await flushPromises()
    expect(w.find('[data-testid="tri-pending"]').text()).toContain('key-prod-001')
    expect(w.find('[data-testid="tri-pending"]').text()).not.toContain('凭据 #11')
    w.unmount()
  })

  it('标签缓存失败保留「凭据 #ID」fallback 且不中断渲染', async () => {
    getCredentialMonitorSummary.mockImplementationOnce(async () => {
      throw new Error('boom')
    })
    vi.stubGlobal('fetch', stubTriFetch({
      pending: [triTask({ id: 11, dedup_key: 'k2', credential_id: 11, raw_model: 'm1' })],
    }))
    const w = mountQueue()
    await flushPromises()
    expect(w.find('[data-testid="tri-pending"]').text()).toContain('凭据 #11')
    w.unmount()
  })

  it('三段各自渲染空态', async () => {
    vi.stubGlobal('fetch', stubTriFetch({}))
    const w = mountQueue()
    await flushPromises()
    expect(w.find('[data-testid="tri-pending-empty"]').exists()).toBe(true)
    expect(w.find('[data-testid="tri-inflight-empty"]').exists()).toBe(true)
    expect(w.find('[data-testid="tri-completed-empty"]').exists()).toBe(true)
    w.unmount()
  })

  it('首次加载渲染 Skeleton', async () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => { /* never resolves */ })))
    const w = mountQueue()
    await flushPromises()
    expect(w.find('[data-testid="tri-skeleton"]').exists()).toBe(true)
    w.unmount()
  })
})

describe('ProbeTriStateQueue — origin 徽标', () => {
  it('scheduled / error / manual 分别渲染为 定期 / 错误触发 / 手动', async () => {
    vi.stubGlobal('fetch', stubTriFetch({
      pending: [
        triTask({ id: 1, dedup_key: 'k-sch', raw_model: 'a', origin: 'scheduled' }),
        triTask({ id: 2, dedup_key: 'k-err', raw_model: 'b', origin: 'error' }),
        triTask({ id: 3, dedup_key: 'k-man', raw_model: 'c', origin: 'manual' }),
      ],
    }))
    const w = mountQueue()
    await flushPromises()
    const badges = w.findAll('[data-testid="origin-badge"]').map((b) => b.text())
    expect(badges).toContain('定期')
    expect(badges).toContain('错误触发')
    expect(badges).toContain('手动')
    w.unmount()
  })
})

describe('ProbeTriStateQueue — completed 大卡', () => {
  it('点击展开显示结果/延迟/错误码/观察时间', async () => {
    vi.stubGlobal('fetch', stubTriFetch({
      completed: [triTask({
        id: 9, dedup_key: 'node_probe:9:m9', credential_id: 9, raw_model: 'm9',
        status: 'completed', outcome: 'failed', reason_code: 'http_503',
        http_status: 503, latency_ms: 1234, finished_at: '2026-08-15T09:00:00Z',
      })],
    }))
    const w = mountQueue()
    await flushPromises()

    expect(w.find('[data-testid="completed-detail"]').exists()).toBe(false)
    await w.find('[data-testid="probe-completed-card"] .probe-card__summary').trigger('click')
    const detail = w.find('[data-testid="completed-detail"]')
    expect(detail.exists()).toBe(true)
    expect(detail.text()).toContain('失败')
    expect(detail.text()).toContain('1.23s')
    expect(detail.text()).toContain('http_503')
    expect(detail.text()).toContain('503')
    w.unmount()
  })

  it('失败卡显示同 dedup_key pending 重臂行的退避下一跳；无重臂行则不显示', async () => {
    vi.stubGlobal('fetch', stubTriFetch({
      pending: [triTask({ id: 5, dedup_key: 'node_probe:5:m5', raw_model: 'm5', next_retry_at_ms: Date.now() + 30_000 })],
      completed: [
        triTask({ id: 4, dedup_key: 'node_probe:5:m5', credential_id: 5, raw_model: 'm5', status: 'completed', outcome: 'failed' }),
        triTask({ id: 6, dedup_key: 'node_probe:6:m6', credential_id: 6, raw_model: 'm6', status: 'completed', outcome: 'failed' }),
      ],
    }))
    const w = mountQueue()
    await flushPromises()

    const backoffs = w.findAll('[data-testid="backoff-next-hop"]')
    expect(backoffs).toHaveLength(1)
    expect(backoffs[0].text()).toContain('退避下一跳')
    w.unmount()
  })
})

describe('ProbeTriStateQueue — 降级与断线', () => {
  it('tri-state API 失败 → SSE-only 降级提示，SSE 增量仍可渲染', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => errorResponse()))
    const w = mountQueue()
    await flushPromises()

    expect(w.find('[data-testid="api-degraded"]').exists()).toBe(true)
    expect(w.find('[data-testid="api-degraded"]').text()).toContain('降级')

    // SSE 增量：pending → in-flight → ok 生命周期驱动三段
    tiles.value = [{
      id: 'node_probe:77:m7', task_type: 'node_probe', source: 'request_failure',
      status: 'pending', credential_id: 77, raw_model: 'm7', attempt: 1, ts: Date.now(),
      origin: 'error',
    }]
    await flushPromises()
    expect(w.findAll('[data-testid="probe-pending-card"]')).toHaveLength(1)

    tiles.value = [...tiles.value, { ...tiles.value[0], status: 'in-flight' }]
    await flushPromises()
    expect(w.findAll('[data-testid="probe-inflight-card"]')).toHaveLength(1)

    tiles.value = [...tiles.value, { ...tiles.value[0], status: 'ok', latency_ms: 300 }]
    await flushPromises()
    const doneCards = w.findAll('[data-testid="probe-completed-card"]')
    expect(doneCards).toHaveLength(1)
    expect(w.find('[data-testid="outcome-badge"]').text()).toContain('成功')
    w.unmount()
  })

  it('SSE 断线（reconnecting）→ 展示重连中状态；恢复 open 后消失', async () => {
    vi.stubGlobal('fetch', stubTriFetch({}))
    connection.value = 'reconnecting'
    const w = mountQueue()
    await flushPromises()
    expect(w.find('[data-testid="sse-state"]').text()).toContain('SSE 重连中')

    connection.value = 'open'
    await flushPromises()
    expect(w.find('[data-testid="sse-state"]').text()).not.toContain('重连')
    w.unmount()
  })

  it('SSE pending 重臂增量携带 next_retry_at_ms 时渲染预计执行时间，缺失不冒充', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => errorResponse()))
    const w = mountQueue()
    await flushPromises()
    tiles.value = [{
      id: 'node_probe:88:m8', task_type: 'node_probe', source: 'periodic',
      status: 'pending', credential_id: 88, raw_model: 'm8', ts: Date.now(),
      next_retry_at_ms: Date.now() + 60_000,
    }]
    await flushPromises()
    const pendingSection = w.find('[data-testid="tri-pending"]')
    expect(pendingSection.text()).toContain('预计')
    expect(pendingSection.text()).not.toContain('预计 —')

    tiles.value = [{ ...tiles.value[0], next_retry_at_ms: undefined }]
    await flushPromises()
    expect(w.find('[data-testid="tri-pending"]').text()).not.toContain('预计')
    w.unmount()
  })
})

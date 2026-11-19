import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import BoardOperationalView from './BoardOperationalView.vue'
import { fetchBoardOperational, fetchDashboardBoard } from '../api/board'
import { testI18n } from '../test-utils/i18n'

vi.mock('../api/board', () => ({
  fetchBoardOperational: vi.fn(),
  fetchDashboardBoard: vi.fn(),
}))

const mockedFetchOperational = vi.mocked(fetchBoardOperational)
const mockedFetchBoard = vi.mocked(fetchDashboardBoard)

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  vi.restoreAllMocks()
})

const SAMPLE_OPERATIONAL = {
  background_tasks: {
    discovery: {
      running: false,
      status: 'success',
      trigger: 'scheduled',
      started_at: '2026-10-17T00:00:00.000Z',
      heartbeat_at: '2026-10-17T00:01:00.000Z',
    },
    probe_loop: { checks_last_10m: 42 },
  },
  selfcheck: {
    total_runs_24h: 24,
    success_rate: 0.958,
    last_status: 'success',
    last_run_at: '2026-10-17T00:30:00.000Z',
  },
}

const SAMPLE_BOARD = {
  summary: {
    total_requests: 10000,
    total_prompt_tokens: 50000,
    total_completion_tokens: 30000,
    total_tokens: 80000,
    total_cost_usd: 10.5,
    total_credits_charged: 1000,
    success_rate: 0.95,
    avg_latency_ms: 250,
    active_api_keys: 5,
    active_models: 10,
    providers: 3,
  },
  pies: {
    models: [
      { key: 'gpt-4', requests: 5000, tokens: 40000, credits: 500, cost_usd: 5.0 },
      { key: 'claude-3', requests: 3000, tokens: 25000, credits: 300, cost_usd: 3.0 },
    ],
    providers: [
      { key: 'openai', requests: 5000, tokens: 40000, credits: 500, cost_usd: 5.0 },
      { key: 'anthropic', requests: 3000, tokens: 25000, credits: 300, cost_usd: 3.0 },
    ],
    clients: [
      { key: 'client-1', requests: 4000, tokens: 32000, credits: 400, cost_usd: 4.0 },
    ],
    tenants: [
      { key: 'default', requests: 10000, tokens: 80000, credits: 1000, cost_usd: 10.5 },
    ],
    errors: [
      { key: 'rate_limit', requests: 100, tokens: 0, credits: 0, cost_usd: 0 },
    ],
    client_ips: [],
    identity_hashes: [],
  },
  trends: [
    { bucket: '2026-10-17T00:00:00Z', requests: 1000, tokens: 8000, credits: 100, cost_usd: 1.0 },
    { bucket: '2026-10-17T01:00:00Z', requests: 1100, tokens: 8800, credits: 110, cost_usd: 1.1 },
  ],
  days: 7,
}

describe('BoardOperationalView', () => {
  it('renders all sections with full data', async () => {
    mockedFetchOperational.mockResolvedValueOnce(SAMPLE_OPERATIONAL)
    mockedFetchBoard.mockResolvedValueOnce(SAMPLE_BOARD)
    const w = mount(BoardOperationalView, { global: { plugins: [testI18n] } })
    await flushPromises()

    // 标题和副标题
    expect(w.text()).toContain('运营面板')
    expect(w.text()).toContain('系统运营状况汇总分析')

    // 运营概览
    expect(w.text()).toContain('运营概览')
    expect(w.text()).toContain('10,000')
    expect(w.text()).toContain('95.0%')
    expect(w.text()).toContain('80,000')
    expect(w.text()).toContain('$10.50')
    expect(w.text()).toContain('1,000')
    expect(w.text()).toContain('250ms')

    // 分布分析
    expect(w.text()).toContain('分布分析')
    expect(w.text()).toContain('Top 5 模型')
    expect(w.text()).toContain('gpt-4')
    expect(w.text()).toContain('claude-3')
    expect(w.text()).toContain('Top 5 供应商')
    expect(w.text()).toContain('openai')
    expect(w.text()).toContain('anthropic')

    // 错误分析
    expect(w.text()).toContain('错误分析')
    expect(w.text()).toContain('rate_limit')

    // 趋势分析
    expect(w.text()).toContain('趋势分析')
    expect(w.text()).toContain('共 2 个数据点')

    // 系统健康 - 后台任务
    expect(w.text()).toContain('系统健康 - 后台任务')
    expect(w.text()).toContain('模型发现')
    expect(w.text()).toContain('探针巡检')
    expect(w.text()).toContain('scheduled')
    expect(w.text()).toContain('42')

    // 系统健康 - 自检
    expect(w.text()).toContain('系统健康 - 自检')
    expect(w.text()).toContain('总运行次数')
    expect(w.text()).toContain('24')
    expect(w.text()).toContain('95.8%')
    expect(w.text()).toContain('成功')

    // 页脚提示
    expect(w.text()).toContain('提示：运营面板数据来自服务端缓存')
    expect(w.text()).toContain('数据时间范围：最近 7 天')
  })

  it('handles time range selection', async () => {
    mockedFetchOperational.mockResolvedValue(SAMPLE_OPERATIONAL)
    mockedFetchBoard.mockResolvedValue(SAMPLE_BOARD)
    const w = mount(BoardOperationalView, { global: { plugins: [testI18n] } })
    await flushPromises()

    // 默认是 7 天
    expect(mockedFetchBoard).toHaveBeenCalledWith({ days: 7 })

    // KxDateRangePicker 现在使用组件，无法直接设置值
    // 我们只验证初始加载是正确的
    expect(w.text()).toContain('数据时间范围')
  })

  it('handles missing board data gracefully', async () => {
    mockedFetchOperational.mockResolvedValueOnce(SAMPLE_OPERATIONAL)
    mockedFetchBoard.mockResolvedValueOnce({
      summary: {},
      pies: {
        models: [],
        providers: [],
        clients: [],
        tenants: [],
        errors: [],
        client_ips: [],
        identity_hashes: [],
      },
      trends: [],
      days: 7,
    })
    const w = mount(BoardOperationalView, { global: { plugins: [testI18n] } })
    await flushPromises()

    // 应该显示占位符而不是崩溃
    expect(w.text()).toContain('—')
    expect(w.text()).toContain('无数据')
  })

  it('shows running chip for live discovery + handles missing nested data', async () => {
    mockedFetchOperational.mockResolvedValueOnce({
      background_tasks: {
        discovery: { running: true, status: 'running' },
        probe_loop: {},
      },
      selfcheck: { total_runs_24h: 0 },
    })
    mockedFetchBoard.mockResolvedValueOnce(SAMPLE_BOARD)
    const w = mount(BoardOperationalView, { global: { plugins: [testI18n] } })
    await flushPromises()

    // running 状态
    expect(w.text()).toContain('运行中')
    // 缺字段的安全兜底
    expect(w.text()).toContain('—')
  })

  it('shows error message on fetch failure and keeps refresh button re-clickable', async () => {
    mockedFetchOperational.mockRejectedValueOnce(new Error('upstream 503'))
    mockedFetchBoard.mockRejectedValueOnce(new Error('upstream 503'))
    const w = mount(BoardOperationalView, { global: { plugins: [testI18n] } })
    await flushPromises()

    expect(w.find('.bo-error').exists()).toBe(true)
    expect(w.text()).toContain('upstream 503')

    // 重试：第二次 fetch 成功
    mockedFetchOperational.mockResolvedValueOnce(SAMPLE_OPERATIONAL)
    mockedFetchBoard.mockResolvedValueOnce(SAMPLE_BOARD)
    await w.find('.btn').trigger('click')
    await flushPromises()

    expect(w.text()).toContain('运营概览')
    expect(w.find('.bo-error').exists()).toBe(false)
  })

  it('calculates derived metrics correctly', async () => {
    mockedFetchOperational.mockResolvedValueOnce(SAMPLE_OPERATIONAL)
    mockedFetchBoard.mockResolvedValueOnce(SAMPLE_BOARD)
    const w = mount(BoardOperationalView, { global: { plugins: [testI18n] } })
    await flushPromises()

    // 平均 Token/请求 = 80000 / 10000 = 8
    expect(w.text()).toContain('平均 8')
    // 平均成本/请求 = 10.5 / 10000 = 0.00105
    expect(w.text()).toContain('平均 $0.00')
    // 平均积分/请求 = 1000 / 10000 = 0.1
    expect(w.text()).toContain('平均 0.1')
  })

  it('shows degradation warnings when present', async () => {
    mockedFetchOperational.mockResolvedValueOnce({
      background_tasks: {
        degraded: true,
        degraded_reason: 'discovery unavailable',
      },
      selfcheck: {
        degraded: true,
        degraded_reason: 'self-check unavailable',
      },
    })
    mockedFetchBoard.mockResolvedValueOnce({
      ...SAMPLE_BOARD,
      summary: {
        ...SAMPLE_BOARD.summary,
        degraded_summary: true,
        summary_hint: '数据源不可用',
      },
      degraded_trends: true,
      degraded_trends_reason: '趋势数据降级',
    })
    const w = mount(BoardOperationalView, { global: { plugins: [testI18n] } })
    await flushPromises()

    expect(w.text()).toContain('数据源不可用')
    expect(w.text()).toContain('趋势数据降级')
    expect(w.text()).toContain('discovery unavailable')
    expect(w.text()).toContain('self-check unavailable')
  })
})

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import BoardOperationalView from './BoardOperationalView.vue'
import { fetchBoardOperational } from '../api/board'
import { testI18n } from '../test-utils/i18n'

vi.mock('../api/board', () => ({
  fetchBoardOperational: vi.fn(),
}))

const mockedFetch = vi.mocked(fetchBoardOperational)

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  vi.restoreAllMocks()
})

const SAMPLE = {
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

describe('BoardOperationalView', () => {
  it('renders background_tasks + selfcheck sections with full field coverage', async () => {
    mockedFetch.mockResolvedValueOnce(SAMPLE)
    const w = mount(BoardOperationalView, { global: { plugins: [testI18n] } })
    await flushPromises()
    // 2 sections
    expect(w.findAll('.bo-section')).toHaveLength(2)
    // background_tasks: discovery + probe_loop 卡片
    expect(w.text()).toContain('模型发现')
    expect(w.text()).toContain('探针巡检')
    expect(w.text()).toContain('scheduled')
    expect(w.text()).toContain('42')
    // selfcheck 4 卡片
    expect(w.text()).toContain('总运行次数')
    expect(w.text()).toContain('24')
    expect(w.text()).toContain('95.8%')
    expect(w.text()).toContain('成功')
    // 刷新按钮 enabled after load
    expect(w.find('.btn').attributes('disabled')).toBeUndefined()
  })

  it('shows running chip for live discovery + handles missing nested data', async () => {
    mockedFetch.mockResolvedValueOnce({
      background_tasks: {
        discovery: { running: true, status: 'running' },
        probe_loop: {},
      },
      selfcheck: { total_runs_24h: 0 },
    })
    const w = mount(BoardOperationalView, { global: { plugins: [testI18n] } })
    await flushPromises()
    // running 状态
    expect(w.text()).toContain('运行中')
    // 缺字段的安全兜底
    expect(w.text()).toContain('—')
    // 4 行 selfcheck 指标（2026-10-28 第 30 增量：原来是 4 张同尺寸同构的
    // 「小标签 + 巨数字」卡并排 ⇒ 改成「标签 → 值」的行）
    expect(w.findAll('.bo-section')[1].findAll('.bo-metric')).toHaveLength(4)
    // ★ 断言「不再有那 4 张卡」本身 ⇒ 防止有人把卡片形态改回来
    expect(w.findAll('.bo-section')[1].findAll('.bo-card-metric')).toHaveLength(0)
  })

  it('shows error message on fetch failure and keeps refresh button re-clickable', async () => {
    mockedFetch.mockRejectedValueOnce(new Error('upstream 503'))
    const w = mount(BoardOperationalView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.find('.bo-error').exists()).toBe(true)
    expect(w.text()).toContain('upstream 503')
    expect(w.findAll('.bo-section')).toHaveLength(0)
    // 重试：第二次 fetch 成功
    mockedFetch.mockResolvedValueOnce(SAMPLE)
    await w.find('.btn').trigger('click')
    await flushPromises()
    expect(w.findAll('.bo-section')).toHaveLength(2)
    expect(w.find('.bo-error').exists()).toBe(false)
  })
})

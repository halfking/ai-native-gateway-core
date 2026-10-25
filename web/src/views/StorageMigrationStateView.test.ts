import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import StorageMigrationStateView from './StorageMigrationStateView.vue'
import { getStorageMigrationState } from '../api/tuning'
import { testI18n } from '../test-utils/i18n'

vi.mock('../api/tuning', () => ({
  getStorageMigrationState: vi.fn(),
}))

const mockedGet = vi.mocked(getStorageMigrationState)

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  vi.restoreAllMocks()
})

const RUNNING = {
  run_id: 'run-001',
  status: 'running' as const,
  from_dir: '/old/path',
  to_dir: '/new/path',
  started_at: '2026-10-17T10:00:00.000Z',
  heartbeat_at: '2026-10-17T10:01:00.000Z',
  files_total: 100,
  files_copied: 45,
  bytes_total: 1048576,
  bytes_copied: 524288,
  files_deleted: 0,
  old_dir_purged: false,
}

const LATEST = {
  run_id: 'run-000',
  status: 'succeeded' as const,
  from_dir: '/old/path',
  to_dir: '/new/path',
  started_at: '2026-10-16T08:00:00.000Z',
  finished_at: '2026-10-16T08:30:00.000Z',
  files_total: 50,
  files_copied: 50,
  bytes_total: 524288,
  bytes_copied: 524288,
  files_deleted: 50,
  old_dir_purged: true,
}

describe('StorageMigrationStateView', () => {
  it('renders running migration with progress + latest history when both present', async () => {
    mockedGet.mockResolvedValueOnce({ running: RUNNING, latest: LATEST })
    const w = mount(StorageMigrationStateView, { global: { plugins: [testI18n] } })
    await flushPromises()
    // 2 段：当前运行 + 历史最新
    expect(w.findAll('.sms-section')).toHaveLength(2)
    // running section
    const runningSection = w.find('.sms-section-running')
    expect(runningSection.exists()).toBe(true)
    expect(runningSection.text()).toContain('运行中')
    expect(runningSection.text()).toContain('run-001')
    expect(runningSection.text()).toContain('45 / 100（45%）')
    // progress bar width = 45%
    const bar = runningSection.find('.sms-progress-bar')
    expect(bar.attributes('style')).toContain('width: 45%')
    // latest section
    expect(w.text()).toContain('成功')
    expect(w.text()).toContain('run-000')
    expect(w.text()).toContain('旧目录已清是')
  })

  it('shows only latest section when no running migration', async () => {
    mockedGet.mockResolvedValueOnce({ running: null, latest: LATEST })
    const w = mount(StorageMigrationStateView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.find('.sms-section-running').exists()).toBe(false)
    expect(w.findAll('.sms-section')).toHaveLength(1)
    expect(w.text()).toContain('run-000')
  })

  it('shows empty state when both running and latest are null', async () => {
    mockedGet.mockResolvedValueOnce({ running: null, latest: null })
    const w = mount(StorageMigrationStateView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.findAll('.sms-section')).toHaveLength(0)
    expect(w.find('.sms-empty').exists()).toBe(true)
    expect(w.text()).toContain('暂无迁移记录')
  })

  it('shows error message on fetch failure and refresh works after recovery', async () => {
    mockedGet.mockRejectedValueOnce(new Error('admin 502'))
    const w = mount(StorageMigrationStateView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.find('.sms-error').exists()).toBe(true)
    expect(w.text()).toContain('admin 502')
    expect(w.findAll('.sms-section')).toHaveLength(0)
    // 重试成功
    mockedGet.mockResolvedValueOnce({ running: RUNNING, latest: LATEST })
    await w.find('.btn').trigger('click')
    await flushPromises()
    expect(w.find('.sms-error').exists()).toBe(false)
    expect(w.findAll('.sms-section')).toHaveLength(2)
  })
})

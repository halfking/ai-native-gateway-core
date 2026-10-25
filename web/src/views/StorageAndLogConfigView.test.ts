import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import StorageAndLogConfigView from './StorageAndLogConfigView.vue'
import { getStorageAndLogSnapshot } from '../api/storageAndLogConfig'
import { testI18n } from '../test-utils/i18n'

vi.mock('../api/storageAndLogConfig', () => ({
  getStorageAndLogSnapshot: vi.fn(),
}))

const mockedGet = vi.mocked(getStorageAndLogSnapshot)

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  vi.restoreAllMocks()
})

const SAMPLE = {
  storage: {
    attachment_dir_override: '',
    ttl_days: 30,
    max_file_size_mb: 20,
    disk_quota_percent: 80,
    auto_cleanup_enabled: true,
    auto_cleanup_threshold: 85,
    effective_dir: '/var/attachments',
    attachment_dir_env: '/var/attachments',
    needs_restart: false,
    current_disk_usage: 1024 * 1024 * 100,
    config_source: 'env',
    download_url_prefix: '/api/attachments/',
  },
  filesystem: {
    attachment_dir: '/var/attachments',
    total_files: 1500,
    total_size_bytes: 1024 * 1024 * 500,
    total_size_human: '500 MB',
    oldest_file_time: '2026-09-01T00:00:00.000Z',
    disk_total_bytes: 1024 * 1024 * 1024 * 100,
    disk_used_bytes: 1024 * 1024 * 1024 * 50,
    disk_avail_bytes: 1024 * 1024 * 1024 * 50,
    disk_usage_percent: 50,
    disk_warning_level: 'safe' as const,
  },
  log: {
    max_size_mb: 100,
    max_backups: 10,
    max_age_days: 7,
    compress: true,
    archive_days: 7,
    delete_days: 30,
    file_path: '/var/log/llmgw.log',
    file_path_override: '',
    file_path_env: '/var/log/llmgw.log',
    enabled: true,
    enabled_source: 'env',
    log_file: '/var/log/llmgw.log',
    log_dir: '/var/log',
    hot_reloadable: true,
    config_source: 'env',
  },
  refreshedAt: '2026-10-17T10:00:00.000Z',
}

describe('StorageAndLogConfigView', () => {
  it('renders 3 sections (storage + filesystem + log) with key field coverage', async () => {
    mockedGet.mockResolvedValueOnce(SAMPLE)
    const w = mount(StorageAndLogConfigView, { global: { plugins: [testI18n] } })
    await flushPromises()
    // 3 sections
    expect(w.findAll('.slc-section')).toHaveLength(3)
    // 存储配置
    expect(w.text()).toContain('DB 覆盖目录')
    expect(w.text()).toContain('（空，用 env）')
    expect(w.text()).toContain('/var/attachments')
    expect(w.text()).toContain('保留 TTL（天）')
    expect(w.text()).toContain('30')
    expect(w.text()).toContain('自动清理')
    expect(w.text()).toContain('开启（85%）')
    // 附件文件系统
    expect(w.text()).toContain('附件目录')
    expect(w.text()).toContain('总文件数')
    expect(w.text()).toContain('1500')
    expect(w.text()).toContain('500 MB')
    expect(w.text()).toContain('使用率')
    expect(w.text()).toContain('安全')
    // 日志配置
    expect(w.text()).toContain('日志文件')
    expect(w.text()).toContain('/var/log/llmgw.log')
    expect(w.text()).toContain('单文件上限（MB）')
    expect(w.text()).toContain('100')
    expect(w.text()).toContain('压缩')
  })

  it('shows error message on fetch failure', async () => {
    mockedGet.mockRejectedValueOnce(new Error('super 403'))
    const w = mount(StorageAndLogConfigView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.find('.slc-error').exists()).toBe(true)
    expect(w.text()).toContain('super 403')
    expect(w.findAll('.slc-section')).toHaveLength(0)
  })

  it('shows disk usage percent with safe / warning / danger color classes', async () => {
    mockedGet.mockResolvedValueOnce({
      ...SAMPLE,
      filesystem: { ...SAMPLE.filesystem, disk_usage_percent: 85, disk_warning_level: 'warning' as const },
    })
    const w = mount(StorageAndLogConfigView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.text()).toContain('85.0%')
    expect(w.text()).toContain('警告')
  })
})

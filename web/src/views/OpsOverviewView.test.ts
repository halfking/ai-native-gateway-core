import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import OpsOverviewView from './OpsOverviewView.vue'
import { getOpsOverview } from '../api/opsOverview'
import { testI18n } from '../test-utils/i18n'

vi.mock('../api/opsOverview', () => ({
  getOpsOverview: vi.fn(),
}))

const mockedGet = vi.mocked(getOpsOverview)

beforeEach(() => {
  vi.clearAllMocks()
})

afterEach(() => {
  vi.restoreAllMocks()
})

const SAMPLE = {
  generated_at: '2026-10-17T08:00:00.000Z',
  center_stats: { total_instances: 10, online_instances: 7, offline_instances: 2, degraded_instances: 1 },
  region_stats: {
    byRegion: {
      cn: { region: 'cn', total_instances: 6, online_instances: 5, offline_instances: 1, degraded_instances: 0, last_heartbeat: '2026-10-17T07:59:50.000Z' },
      us: { region: 'us', total_instances: 4, online_instances: 2, offline_instances: 1, degraded_instances: 1 },
    },
    total: 2,
  },
  deployment_nodes: [
    { node_id: 'n-001', region: 'cn', version: 'v1.2.3', status: 'online', started_at: '2026-10-17T00:00:00.000Z' },
  ],
  data_plane_tables: [
    { name: 'gateway_instances', count: 10 },
    { name: 'licenses', count: 25 },
  ],
  license_total: 25,
  offline_requests: [{ id: 'req-1' }],
  fault_stats: { total: 5, resolved: 4, pending: 1 },
  recent_faults: [
    { id: 1, severity: 'warn', message: 'high latency', status: 'resolved', triggered_at: '2026-10-17T07:00:00.000Z' },
  ],
  recent_upgrades: [
    { id: 1, version_from: 'v1.0.0', version_to: 'v1.2.3', status: 'success', started_at: '2026-10-16T20:00:00.000Z', completed_at: '2026-10-16T20:30:00.000Z' },
  ],
  download_stats: { total_downloads: 100, last_24h: 8 },
  runtime_metrics_summary: { uptime_seconds: 90000, memory_rss_bytes: 100 * 1024 * 1024, goroutines: 50, cpu_percent: 12.5 },
}

describe('OpsOverviewView', () => {
  it('renders 9 sections + 4 center stat cards from sample data', async () => {
    mockedGet.mockResolvedValueOnce(SAMPLE as never)
    const w = mount(OpsOverviewView, { global: { plugins: [testI18n] } })
    await flushPromises()
    // 9 sections: 中心实例 / 区域分布 / 部署节点 / 数据面表规模 / 许可证 / 故障 / 升级 / 下载统计 / 运行时指标
    expect(w.findAll('.oo-section')).toHaveLength(9)
    // 中心实例 4 卡片
    expect(w.text()).toContain('10')   // 总实例
    expect(w.text()).toContain('7')    // 在线
    expect(w.text()).toContain('2')    // 离线
    expect(w.text()).toContain('1')    // 降级
    // 区域分布
    expect(w.text()).toContain('cn')
    expect(w.text()).toContain('us')
    // 升级记录
    expect(w.text()).toContain('v1.0.0')
    expect(w.text()).toContain('v1.2.3')
    // 运行时
    expect(w.text()).toContain('12.5%')
    expect(w.text()).toContain('1天 1小时')  // 90000s = 1d 1h
    // 刷新按钮 enabled
    expect(w.find('.btn').attributes('disabled')).toBeUndefined()
  })

  it('hides optional sections when backend omits data', async () => {
    mockedGet.mockResolvedValueOnce({
      generated_at: '2026-10-17T08:00:00.000Z',
      center_stats: { total_instances: 0, online_instances: 0, offline_instances: 0, degraded_instances: 0 },
    } as never)
    const w = mount(OpsOverviewView, { global: { plugins: [testI18n] } })
    await flushPromises()
    // 缺 region / deployment / data_plane / fault / upgrade / download / runtime 隐藏 → 仅 中心实例 + 许可证 可见 = 2 节
    const sections = w.findAll('.oo-section')
    expect(sections.length).toBe(2)
    // 全 0 不应渲染区域 / 部署 / 升级表
    expect(w.findAll('.oo-table').length).toBe(0)
  })

  it('shows error message on fetch failure and refresh works after recovery', async () => {
    mockedGet.mockRejectedValueOnce(new Error('forbidden'))
    const w = mount(OpsOverviewView, { global: { plugins: [testI18n] } })
    await flushPromises()
    expect(w.find('.oo-error').exists()).toBe(true)
    expect(w.text()).toContain('forbidden')
    expect(w.findAll('.oo-section')).toHaveLength(0)
    // 重试成功
    mockedGet.mockResolvedValueOnce(SAMPLE as never)
    await w.find('.btn').trigger('click')
    await flushPromises()
    expect(w.find('.oo-error').exists()).toBe(false)
    expect(w.findAll('.oo-section')).toHaveLength(9)
  })
})

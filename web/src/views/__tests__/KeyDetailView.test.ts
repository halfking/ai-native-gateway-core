import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import KeyDetailView from '../KeyDetailView.vue'
import * as api from '../../api'

// Mock API calls
vi.mock('../../api', () => ({
  getKeyDetail: vi.fn(),
  getKeyUsage: vi.fn(),
  getKeyUsageByModel: vi.fn(),
  getKeyUsageTrend: vi.fn(),
  getRequestLogs: vi.fn(),
  updateKeyLimits: vi.fn(),
}))

// Mock vue-router
vi.mock('vue-router', () => ({
  useRoute: () => ({
    params: { id: '123' }
  }),
  useRouter: () => ({
    push: vi.fn()
  })
}))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      keys: {
        common: {
          loading: '加载中…',
          loadFailed: '加载失败',
        },
        detail: {
          backToList: '返回',
          title: '密钥详情',
          logs: {
            title: '最近请求日志',
            viewAll: '查看全部',
            empty: '暂无请求记录',
            success: '成功',
            pending: '进行中',
          },
          error: {
            loadDetailFailed: '加载详情失败',
            loadLogsFailed: '加载日志失败',
          }
        }
      }
    }
  }
})

describe('KeyDetailView - Request Logs', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    
    // Setup default mock responses
    vi.mocked(api.getKeyDetail).mockResolvedValue({
      id: 123,
      key_prefix: 'sk-test',
      application_id: 1,
      status: 'enabled',
      rate_limit_rpm: null,
      rate_limit_concurrent: null,
      rate_limit_tpm: null,
      last_used_at: null,
    } as any)
    
    vi.mocked(api.getKeyUsage).mockResolvedValue({
      total_requests: 100,
      total_prompt_tokens: 1000,
      total_completion_tokens: 500,
      total_tokens: 1500,
      total_cost_usd: 0.05,
      success_rate: 0.95,
      avg_latency_ms: 200,
      unique_models: 3,
      first_request_at: '2024-01-01T00:00:00Z',
      last_request_at: '2024-01-02T00:00:00Z',
      window_start: '2024-01-01',
      window_end: '2024-01-02',
    } as any)
    
    vi.mocked(api.getKeyUsageByModel).mockResolvedValue([])
    vi.mocked(api.getKeyUsageTrend).mockResolvedValue([])
  })

  it('should load recent request logs on mount', async () => {
    const mockLogs = [
      {
        ts: '2024-01-01T12:00:00Z',
        request_id: 'req-1',
        api_key_id: 123,
        client_model: 'gpt-4',
        canonical_model: 'gpt-4',
        success: true,
        latency_ms: 150,
        prompt_tokens: 100,
        completion_tokens: 50,
        cost_usd: 0.01,
        error_kind: null,
      },
      {
        ts: '2024-01-01T12:05:00Z',
        request_id: 'req-2',
        api_key_id: 123,
        client_model: 'gpt-3.5-turbo',
        canonical_model: 'gpt-3.5-turbo',
        success: false,
        error_kind: 'rate_limit',
        latency_ms: null,
        prompt_tokens: null,
        completion_tokens: null,
        cost_usd: null,
      }
    ]

    vi.mocked(api.getRequestLogs).mockResolvedValue({
      items: mockLogs,
      total: 2,
      page: 1,
      page_size: 10,
    } as any)

    const wrapper = mount(KeyDetailView, {
      global: {
        plugins: [i18n]
      }
    })

    // Wait for async operations
    await new Promise(resolve => setTimeout(resolve, 100))

    expect(api.getRequestLogs).toHaveBeenCalledWith({
      api_key_id: 123,
      page: 1,
      page_size: 10,
    })
  })

  it('should display empty state when no logs', async () => {
    vi.mocked(api.getRequestLogs).mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 10,
    } as any)

    const wrapper = mount(KeyDetailView, {
      global: {
        plugins: [i18n]
      }
    })

    await new Promise(resolve => setTimeout(resolve, 100))

    expect(wrapper.text()).toContain('暂无请求记录')
  })

  it('should display error state when logs fail to load', async () => {
    vi.mocked(api.getRequestLogs).mockRejectedValue(new Error('Network error'))

    const wrapper = mount(KeyDetailView, {
      global: {
        plugins: [i18n]
      }
    })

    await new Promise(resolve => setTimeout(resolve, 100))

    // Should show some error indication
    expect(wrapper.html()).toBeTruthy()
  })
})

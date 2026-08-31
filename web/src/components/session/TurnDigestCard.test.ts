// TurnDigestCard.test.ts — OBS-FE digest card component tests.
// Covers: empty digest → fallback, full digest → all sections, event color
// mapping (error/warning/info), omitted metrics (cache_hit_rate /
// compression_rate), tool_usage tag list rendering.

import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import type { TurnDigest } from '../../api/sessions_v2'
import TurnDigestCard from './TurnDigestCard.vue'

const i18n = createI18n({
  legacy: false,
  globalInjection: true,
  locale: 'zh-CN',
  fallbackLocale: 'en',
  messages: {
    'zh-CN': {
      turnDigest: {
        title: '轮次摘要',
        turn: '轮次',
        loading: '摘要加载中…',
        close: '关闭',
        fallback: '摘要生成中',
        empty: '本轮暂无摘要',
        view: '查看摘要',
        userInput: '用户输入',
        assistantOutput: '助手输出',
        statusError: '出错',
        statusWarning: '警告',
        statusSuccess: '成功',
        metrics: {
          tokens: 'Tokens',
          cost: '费用',
          latency: '延迟',
          cacheHitRate: '缓存命中率',
          compressionRate: '压缩率',
        },
        events: { header: '关键事件', error: '错误', warning: '警告', info: '信息' },
        toolUsage: '工具使用',
        toolCount: '{n} 次',
        tabs: {
          summary: '摘要', request: '请求', response: '回复', compression: '压缩诊断',
          meta: '元数据', governance: '治理', attachments: '附件',
        },
        noAttachments: '无附件',
        openAttachment: '下载附件',
        openingAttachment: '正在打开…',
      },
    },
  },
})

function mountCard(props: Record<string, unknown> = {}) {
  return mount(TurnDigestCard, {
    props,
    global: {
      plugins: [i18n],
      stubs: {
        'el-tag': { template: '<span class="el-tag"><slot /></span>' },
      },
    },
  })
}

function fullDigest(): TurnDigest {
  return {
    user_input: '用户提问示例',
    assistant_output: '助手回复示例',
    metrics: {
      tokens_used: 1234,
      cost: 0.0123,
      latency_ms: 1500,
      cache_hit_rate: 0.42,
      compression_rate: 0.18,
    },
    events: [
      { type: 'warning', category: 'retry', message: '重试一次' },
      { type: 'error', category: 'timeout', message: '上游超时' },
      { type: 'info', category: 'cache', message: '命中缓存' },
    ],
    tool_usage: { tool_call_count: 3, tools_used: ['web_search', 'calculator'] },
  }
}

beforeEach(() => {
  vi.clearAllMocks()
})

describe('TurnDigestCard', () => {
  it('renders fallback when digest is null and no summary/title', () => {
    const w = mountCard({ digest: null })
    expect(w.find('[data-testid="tdc-fallback"]').exists()).toBe(true)
    expect(w.text()).toContain('本轮暂无摘要')
  })

  it('renders summary fallback when digest is null but summary provided', () => {
    const w = mountCard({ digest: null, summary: '一些旧的摘要文本' })
    expect(w.text()).toContain('一些旧的摘要文本')
  })

  it('renders title fallback when digest is null but only title provided', () => {
    const w = mountCard({ digest: null, title: '对话标题' })
    expect(w.text()).toContain('对话标题')
  })

  it('renders loading state when loading=true and no digest', () => {
    const w = mountCard({ digest: null, loading: true })
    expect(w.text()).toContain('摘要生成中')
  })

  it('renders all sections for a full digest', () => {
    const w = mountCard({ digest: fullDigest(), turnIndex: 5 })
    expect(w.find('[data-testid="tdc-user-input"]').exists()).toBe(true)
    expect(w.find('[data-testid="tdc-assistant-output"]').exists()).toBe(true)
    expect(w.find('[data-testid="tdc-tool-usage"]').exists()).toBe(true)
    expect(w.find('[data-testid="tdc-events"]').exists()).toBe(true)
    expect(w.find('[data-testid="tdc-tokens"]').exists()).toBe(true)
    expect(w.find('[data-testid="tdc-cost"]').exists()).toBe(true)
    expect(w.find('[data-testid="tdc-latency"]').exists()).toBe(true)
    expect(w.find('[data-testid="tdc-cache"]').exists()).toBe(true)
    expect(w.find('[data-testid="tdc-compression"]').exists()).toBe(true)
  })

  it('omits cache_hit_rate and compression_rate rows when absent', () => {
    const digest: TurnDigest = {
      user_input: 'hi',
      assistant_output: 'hello',
      metrics: { tokens_used: 100, cost: 0.01, latency_ms: 200 },
    }
    const w = mountCard({ digest })
    expect(w.find('[data-testid="tdc-tokens"]').exists()).toBe(true)
    expect(w.find('[data-testid="tdc-cost"]').exists()).toBe(true)
    expect(w.find('[data-testid="tdc-latency"]').exists()).toBe(true)
    expect(w.find('[data-testid="tdc-cache"]').exists()).toBe(false)
    expect(w.find('[data-testid="tdc-compression"]').exists()).toBe(false)
  })

  it('renders error status badge when any event has type=error', () => {
    const digest: TurnDigest = {
      user_input: 'hi',
      assistant_output: 'oops',
      metrics: { tokens_used: 10, cost: 0, latency_ms: 100 },
      events: [{ type: 'error', category: 'timeout', message: 'fail' }],
    }
    const w = mountCard({ digest })
    expect(w.text()).toContain('出错')
  })

  it('does not render user_input / assistant_output sections when empty', () => {
    const digest: TurnDigest = {
      user_input: '',
      assistant_output: '',
      metrics: { tokens_used: 0, cost: 0, latency_ms: 0 },
    }
    const w = mountCard({ digest })
    expect(w.find('[data-testid="tdc-user-input"]').exists()).toBe(false)
    expect(w.find('[data-testid="tdc-assistant-output"]').exists()).toBe(false)
  })

  it('renders every tool in tools_used as a tag', () => {
    const w = mountCard({ digest: fullDigest() })
    const tags = w.findAll('[data-testid="tdc-tool-usage"] .el-tag')
    // tools_used = ['web_search', 'calculator']
    expect(tags.length).toBeGreaterThanOrEqual(2)
    expect(w.text()).toContain('web_search')
    expect(w.text()).toContain('calculator')
    expect(w.text()).toContain('3 次')
  })

  it('formats latency in human-readable form', () => {
    const digest: TurnDigest = {
      user_input: 'x',
      assistant_output: 'y',
      metrics: { tokens_used: 0, cost: 0, latency_ms: 65_000 },
    }
    const w = mountCard({ digest })
    expect(w.text()).toContain('1m')
  })
})

import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import RoutingAttemptsTimeline from './RoutingAttemptsTimeline.vue'

const attempts = [
  {
    seq: 1,
    provider_id: 35,
    provider_name: '火山方舟',
    credential_id: 12,
    raw_model: 'glm-5.2',
    upstream_url: 'https://example.invalid/v1/chat/completions',
    result: 'model_not_found',
    latency_ms: 1523,
    http_status: 404,
    error_message: 'model not found',
  },
  {
    seq: 2,
    provider_id: 18,
    provider_name: 'NVIDIA',
    credential_id: 13,
    raw_model: 'glm-5.2',
    upstream_url: 'https://example.invalid/v1/chat/completions',
    result: 'success',
    latency_ms: 2300,
  },
]

describe('RoutingAttemptsTimeline', () => {
  it('renders the summary and ordered fallback attempts', () => {
    const wrapper = mount(RoutingAttemptsTimeline, {
      props: {
        summary: '候选1: 火山方舟(35) 模型未找到 1.5s → 候选2: NVIDIA(18) 成功 2.3s',
        attempts,
      },
    })

    expect(wrapper.text()).toContain('模型未找到')
    expect(wrapper.text()).toContain('成功 · 2.3s')
    expect(wrapper.text()).toContain('HTTP 404')
    expect(wrapper.text()).toContain('火山方舟')
    expect(wrapper.findAll('.attempt-item')).toHaveLength(2)
  })

  it('renders an empty state when no routing attempts were persisted', () => {
    const wrapper = mount(RoutingAttemptsTimeline, { props: { attempts: null } })

    expect(wrapper.text()).toContain('暂无路由回退尝试记录')
    expect(wrapper.findAll('.attempt-item')).toHaveLength(0)
  })
})

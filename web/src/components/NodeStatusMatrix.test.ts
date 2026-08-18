import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import NodeStatusMatrix from './NodeStatusMatrix.vue'
import { liveStreamState } from '../composables/liveStreamStore'

// 与 QueuePerspectivePanel.test.ts 一致的 i18n 写法：legacy:false + legacy messages 模式。
// 注意：locale 在 vue-i18n v9 内部按 BCP-47 归一化为 "zh"，故 messages 也用 "zh"。
const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    zh: {
      requestJourneys: {
        triggerTitle: '节点状态矩阵',
        viewAll: '查看全景',
        triggerMeta: '{total} 个节点',
        abnormalCount: '异常 {count}',
        empty: '暂无节点数据',
        emptyHint: '等待 node_update 推送',
        modalSubtitle: '全量节点按健康分组的全景；点击卡片查看模型×节点详情与维护',
        groupDanger: '⚠️ 异常节点 ({count})',
        groupWarn: '⚡ 警告节点 ({count})',
        groupOk: '✅ 正常节点 ({count})',
        groupDisabled: '🚫 已禁用节点 ({count})',
        nodeLabel: '节点 {credentialId}',
        circuit: '熔断',
        availability: '可用',
        quota: '配额',
        health: '健康',
        inFlight: '在途 {count}',
        lastLatency: '{ms}ms',
        disabledTag: '已禁用',
        closeLabel: '关闭',
        detailTitle: 'Journey {requestId}',
        detailLoading: '正在加载时间线...',
        detailError: 'Journey 详情暂不可用',
        loading: '正在加载请求 Journey...',
        unavailable: '请求 Journey 数据暂不可用',
        retry: '重试',
        emptyWindow: '当前 FIFO 窗口内无请求',
        totalGroup: '全部请求',
        modelGroup: '模型 {model}',
        nodeGroup: '{model} · 供应商 {provider} · 节点 {node}',
        position: '#{position}',
        requestedModel: '请求模型 {model}',
        resolvedModel: '解析模型 {model}',
        attempt: '第 {number} 次尝试',
        updatedAt: '更新于 {time}',
        openDetail: '查看请求 {requestId} 的 Journey',
        closeDetail: '关闭 Journey 详情',
      },
    },
  },
})

function mountMatrix() {
  return mount(NodeStatusMatrix, {
    attachTo: document.body,
    global: { stubs: { Teleport: true }, plugins: [i18n] },
  })
}

describe('NodeStatusMatrix', () => {
  beforeEach(() => {
    liveStreamState.nodes = [{
      credential_id: 11,
      provider_id: 7,
      provider_code: 'anthropic',
      circuit_state: 'open',
      availability_state: 'ready',
      quota_state: 'ok',
      health_status: 'unreachable',
      manual_disabled: false,
    }]
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({}) }))
    vi.stubGlobal('confirm', vi.fn(() => true))
  })

  afterEach(() => {
    document.body.innerHTML = ''
    vi.unstubAllGlobals()
  })

  it('renders only a lazy trigger by default and opens the panorama on demand', async () => {
    const wrapper = mountMatrix()

    // 默认零渲染：只有触发按钮 + 健康摘要，矩阵分组不挂载
    const trigger = wrapper.get('.nm-trigger')
    expect(trigger.attributes('aria-haspopup')).toBe('dialog')
    expect(trigger.attributes('aria-expanded')).toBe('false')
    expect(wrapper.find('.nm-group').exists()).toBe(false)

    await trigger.trigger('click')
    expect(wrapper.find('.nm-modal').exists()).toBe(true)
    expect(wrapper.find('.nm-group').exists()).toBe(true)

    // 关闭弹窗即销毁内容
    await wrapper.get('.nm-modal-close').trigger('click')
    expect(wrapper.find('.nm-modal').exists()).toBe(false)
  })

  it('groups unhealthy nodes and opens the unified detail drawer', async () => {
    const wrapper = mountMatrix()
    await wrapper.get('.nm-trigger').trigger('click')

    await wrapper.find('.nm-card').trigger('click')
    expect(wrapper.find('.nd-drawer').exists()).toBe(true)
  })

  it('opens unified maintenance controls for a selected node', async () => {
    const wrapper = mountMatrix()
    await wrapper.get('.nm-trigger').trigger('click')
    await wrapper.find('.nm-card').trigger('click')
    await wrapper.get('.nd-tabs button:last-child').trigger('click')
    await flushPromises()

    expect(wrapper.find('.nd-actions .btn-primary').exists()).toBe(true)
  })

  it('closes the panorama via mask click and tears down the detail drawer state', async () => {
    liveStreamState.nodes = [{
      credential_id: 11, provider_id: 7, provider_code: 'anthropic', circuit_state: 'open', availability_state: 'ready', quota_state: 'ok', health_status: 'unreachable', manual_disabled: false,
    }]
    const wrapper = mountMatrix()
    // 打开弹窗并打开详情抽屉
    await wrapper.get('.nm-trigger').trigger('click')
    await wrapper.find('.nm-card').trigger('click')
    expect(wrapper.find('.nd-drawer').exists()).toBe(true)

    // 点遮罩关闭：vue-test-utils 不允许传 target；用直接触发 .self 修饰语义
    const mask = wrapper.get('.nm-modal-mask')
    mask.element.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    // 给 Vue 反应周期一次 flush
    await flushPromises()
    expect(wrapper.find('.nm-modal').exists()).toBe(false)
    expect(wrapper.find('.nd-drawer').exists()).toBe(false)

    // 再次打开 → 抽屉状态已重置，不会携带上一次的 selectedNode
    await wrapper.get('.nm-trigger').trigger('click')
    expect(wrapper.find('.nm-card').exists()).toBe(true)
    expect(wrapper.find('.nd-drawer').exists()).toBe(false)
  })

  it('supports keyboard access and closes the panorama with Escape', async () => {
    const wrapper = mountMatrix()
    await wrapper.get('.nm-trigger').trigger('click')

    const groupHeader = wrapper.get('.nm-group-header')
    expect(groupHeader.element.tagName).toBe('BUTTON')
    expect(groupHeader.attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('.nm-card').element.tagName).toBe('BUTTON')

    await groupHeader.trigger('click')
    expect(wrapper.get('.nm-group-header').attributes('aria-expanded')).toBe('false')

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await flushPromises()
    expect(wrapper.find('.nm-modal').exists()).toBe(false)
    expect(wrapper.get('.nm-trigger').attributes('aria-expanded')).toBe('false')
  })

  it('renders the empty state inside the modal when no node_update data', async () => {
    liveStreamState.nodes = []
    const wrapper = mountMatrix()
    await wrapper.get('.nm-trigger').trigger('click')
    expect(wrapper.find('.nm-card').exists()).toBe(false)
    expect(wrapper.find('.nm-modal').exists()).toBe(true)
  })
})

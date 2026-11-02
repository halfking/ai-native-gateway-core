import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import zhCN from '../../locales/zh-CN'
import ConversationMessagesPanel from './ConversationMessagesPanel.vue'

// ConversationMessagesPanel.test.ts —— 「对话」页空态说真话（2026-11-02）。
//
// ★ 病：正文没落库（body_status=unavailable）与「这条请求没有消息」是两件事，
//   但面板此前一律显示「(无消息)」⇒ 把「录制同意关闭 ⇒ 正文压根没写」
//   读成「这次请求没有对话」＝**错的量具比没量具危险**。
// ★ 本次报障形态：overview 有 token/预览，chat 的 system/user/tool/assistant
//   四个页签全空，而运营看到的提示是「无消息」。

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': zhCN },
})

function mountPanel(props: Record<string, unknown> = {}) {
  return mount(ConversationMessagesPanel, {
    props: {
      body: null,
      responseBody: null,
      ...props,
    },
    global: { plugins: [i18n] },
  })
}

describe('ConversationMessagesPanel 空态（正文未落库 ≠ 无消息）', () => {
  it('① body_status=unavailable ⇒ 显示「正文未落库」而不是「(无消息)」', () => {
    const wrapper = mountPanel({ bodyStatus: 'unavailable' })
    expect(wrapper.find('[data-testid="conversation-body-unavailable"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('(无消息)')
  })

  it('② 录制同意关闭 ⇒ 显示「会话正文录制未开启」并给出成因', () => {
    const wrapper = mountPanel({ bodyStatus: 'unavailable', recordingConsent: false })
    const banner = wrapper.get('[data-testid="conversation-body-unavailable"]')
    expect(banner.text()).toContain('会话正文录制未开启')
    expect(banner.text()).toContain('管理员')
  })

  it('③ 正文未落库时**不**渲染 assistant 回复块（否则会显示「(无回复)」）', () => {
    const wrapper = mountPanel({ bodyStatus: 'unavailable', recordingConsent: false })
    expect(wrapper.find('[data-testid="chat-reply"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('(无回复)')
  })

  it('④ 没有 body_status（旧后端）⇒ 保持原空态「(无消息)」，不误报', () => {
    const wrapper = mountPanel()
    expect(wrapper.find('[data-testid="conversation-body-unavailable"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('(无消息)')
  })

  it('⑤ body_status=available 但正文为空 ⇒ 仍是「(无消息)」（这次是真的没有消息）', () => {
    const wrapper = mountPanel({ bodyStatus: 'available' })
    expect(wrapper.find('[data-testid="conversation-body-unavailable"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('(无消息)')
  })

  it('⑥ 有正文时正常渲染消息，角色页签可切', async () => {
    const wrapper = mountPanel({
      body: [{ role: 'system', content: 'sys' }, { role: 'user', content: 'usr' }],
      responseBody: [{ role: 'assistant', content: 'ast' }],
      bodyStatus: 'available',
    })
    expect(wrapper.text()).toContain('[system]')
    expect(wrapper.text()).toContain('[user]')
    expect(wrapper.find('[data-testid="chat-reply"]').exists()).toBe(true)

    const buttons = wrapper.findAll('.conv-filters button')
    const userBtn = buttons.find((b) => b.text() === 'user')
    expect(userBtn).toBeTruthy()
    await userBtn!.trigger('click')
    expect(wrapper.text()).not.toContain('[system]')
    expect(wrapper.text()).toContain('[user]')
  })
})

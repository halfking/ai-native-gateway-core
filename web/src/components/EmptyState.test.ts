// EmptyState/AppSpinner 共享组件测试：渲染文案、slot 优先级、inline 模式。

import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import EmptyState from './EmptyState.vue'
import AppSpinner from './AppSpinner.vue'

describe('EmptyState', () => {
  it('renders the text prop', () => {
    const w = mount(EmptyState, { props: { text: '暂无数据' } })
    expect(w.text()).toBe('暂无数据')
    expect(w.find('.app-empty-state').exists()).toBe(true)
  })

  it('prefers the default slot over the text prop', () => {
    const w = mount(EmptyState, {
      props: { text: 'fallback' },
      slots: { default: '<p>rich content</p>' },
    })
    expect(w.html()).toContain('rich content')
    expect(w.html()).not.toContain('fallback')
  })

  it('applies the padding prop for visual parity with legacy .empty-state', () => {
    const w = mount(EmptyState, { props: { padding: '24px' } })
    expect(w.find('.app-empty-state').attributes('style')).toContain('padding: 24px')
  })
})

describe('AppSpinner', () => {
  it('renders the disc with an optional label', () => {
    const w = mount(AppSpinner, { props: { label: '加载中…' } })
    expect(w.find('.app-spinner__disc').exists()).toBe(true)
    expect(w.text()).toContain('加载中…')
    expect(w.attributes('role')).toBe('status')
  })

  it('renders disc only when label is empty', () => {
    const w = mount(AppSpinner)
    expect(w.find('.app-spinner__disc').exists()).toBe(true)
    expect(w.find('.app-spinner__label').exists()).toBe(false)
  })

  it('inline mode drops the block padding', () => {
    const w = mount(AppSpinner, { props: { inline: true } })
    expect(w.find('.app-spinner--inline').exists()).toBe(true)
  })
})

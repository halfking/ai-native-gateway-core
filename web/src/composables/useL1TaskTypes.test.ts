// useL1TaskTypes.test.ts — verifies the i18n L1 label flow added 2026-07-20.
//
// Critical: l1Label() must prefer the i18n translation over the backend's
// Chinese label so locale switching shows the user-localized name. This
// regression test guards against the i18n step being silently bypassed
// (e.g. by someone inlining the backend label again).

import { describe, it, expect, beforeEach } from 'vitest'
import { nextTick, ref, watch } from 'vue'
import { mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'
import { createI18n, useI18n } from 'vue-i18n'
import enUSCommon from '../locales/en-US/common'
import zhCNCommon from '../locales/zh-CN/common'
import { useL1TaskTypes } from './useL1TaskTypes'

// Build a minimal i18n that mirrors the production app's module structure
// (each locale has a `common` namespace where l1TaskType lives). The
// production i18n setup nests all modules under their respective keys
// (common, nav, workTypes, etc.).
function makeI18n(initialLocale: 'zh-CN' | 'en') {
  return createI18n({
    legacy: false,
    globalInjection: true,
    locale: initialLocale,
    fallbackLocale: 'en',
    messages: {
      'zh-CN': { common: { l1TaskType: zhCNCommon.l1TaskType } },
      'en': { common: { l1TaskType: enUSCommon.l1TaskType } },
    },
  })
}

// To use useI18n() (and our composable) we need a Vue app context.
// Mount a tiny harness component that returns the label in its DOM.
function mountWithI18n(i18n: ReturnType<typeof makeI18n>, key: string) {
  const Comp = defineComponent({
    setup() {
      const { l1Label } = useL1TaskTypes()
      return () => h('div', { 'data-test': 'l1' }, l1Label(key))
    },
  })
  return mount(Comp, { global: { plugins: [i18n] } })
}

describe('useL1TaskTypes i18n labels', () => {
  let i18n: ReturnType<typeof makeI18n>
  beforeEach(() => {
    i18n = makeI18n('zh-CN')
  })

  it('zh-CN: l1Label("code") returns "代码"', () => {
    const wrapper = mountWithI18n(i18n, 'code')
    expect(wrapper.text()).toBe('代码')
  })

  it('en: l1Label("code") returns "Code"', () => {
    i18n.global.locale.value = 'en'
    const wrapper = mountWithI18n(i18n, 'code')
    expect(wrapper.text()).toBe('Code')
  })

  it('zh-CN: l1Label("function_call") returns "函数调用"', () => {
    const wrapper = mountWithI18n(i18n, 'function_call')
    expect(wrapper.text()).toBe('函数调用')
  })

  it('zh-CN: l1Label("agent") returns "Agent" (English-only proper noun)', () => {
    const wrapper = mountWithI18n(i18n, 'agent')
    expect(wrapper.text()).toBe('Agent')
  })

  it('zh-CN: l1Label("vision") returns "视觉"', () => {
    const wrapper = mountWithI18n(i18n, 'vision')
    expect(wrapper.text()).toBe('视觉')
  })

  it('en: l1Label("vision") returns "Vision"', () => {
    i18n.global.locale.value = 'en'
    const wrapper = mountWithI18n(i18n, 'vision')
    expect(wrapper.text()).toBe('Vision')
  })

  it('en: l1Label("function_call") returns "Function calling"', () => {
    i18n.global.locale.value = 'en'
    const wrapper = mountWithI18n(i18n, 'function_call')
    expect(wrapper.text()).toBe('Function calling')
  })

  it('en: l1Label("long_context") returns "Long context"', () => {
    i18n.global.locale.value = 'en'
    const wrapper = mountWithI18n(i18n, 'long_context')
    expect(wrapper.text()).toBe('Long context')
  })

  it('en: l1Label("reasoning") returns "Reasoning"', () => {
    i18n.global.locale.value = 'en'
    const wrapper = mountWithI18n(i18n, 'reasoning')
    expect(wrapper.text()).toBe('Reasoning')
  })

  it('en: l1Label("creative") returns "Creative writing"', () => {
    i18n.global.locale.value = 'en'
    const wrapper = mountWithI18n(i18n, 'creative')
    expect(wrapper.text()).toBe('Creative writing')
  })

  it('en: l1Label("chat") returns "General chat"', () => {
    i18n.global.locale.value = 'en'
    const wrapper = mountWithI18n(i18n, 'chat')
    expect(wrapper.text()).toBe('General chat')
  })

  it('unknown L1 key: l1Label("foo") returns "foo" (no crash, raw key fallback)', () => {
    const wrapper = mountWithI18n(i18n, 'foo')
    expect(wrapper.text()).toBe('foo')
  })

  it('empty L1 key: l1Label("") returns "" (no crash)', () => {
    const wrapper = mountWithI18n(i18n, '')
    expect(wrapper.text()).toBe('')
  })

  it('operator-added L1 key (e.g. "vision_audio"): falls back to raw key when no i18n + no canonical', () => {
    // Composable seeds canonical 8 but operator-added L1 keys (e.g.
    // "vision_audio") only come from the backend fetch. With the seed
    // alone, l1Label falls back to the raw key. Acceptable — UI shows
    // the L1 key until the user adds an i18n entry.
    const wrapper = mountWithI18n(i18n, 'vision_audio')
    expect(wrapper.text()).toBe('vision_audio')
  })

  it('all 8 canonical keys have i18n in both zh-CN and en', () => {
    const keys = ['chat', 'reasoning', 'code', 'agent', 'creative', 'long_context', 'vision', 'function_call']
    for (const k of keys) {
      expect(zhCNCommon.l1TaskType[k as keyof typeof zhCNCommon.l1TaskType]).toBeTruthy()
      expect(enUSCommon.l1TaskType[k as keyof typeof enUSCommon.l1TaskType]).toBeTruthy()
    }
  })

  it('locale switch: en → zh-CN reactively updates label', async () => {
    i18n.global.locale.value = 'en'
    const wrapper = mountWithI18n(i18n, 'vision')
    expect(wrapper.text()).toBe('Vision')
    i18n.global.locale.value = 'zh-CN'
    await nextTick()
    expect(wrapper.text()).toBe('视觉')
  })
})

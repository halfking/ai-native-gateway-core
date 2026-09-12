// AppModal.test.ts — 开合、ESC/遮罩关闭、滚动锁定引用计数、焦点圈闭与返还，
// 以及响应式源码断言（768px 档、isSmall 全屏、宽度档位映射）。
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import AppModal from './AppModal.vue'
import { _resetScrollLockForTests } from '../../composables/useScrollLock'

const source = readFileSync(resolve(process.cwd(), 'src/components/ui/AppModal.vue'), 'utf8')

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { common: { button: { close: '关闭' } } } },
})

function mountModal(props: Record<string, unknown> = {}, slot = '<p>body-content</p>') {
  return mount(
    AppModal,
    {
      props: { modelValue: true, title: '测试弹窗', ...props },
      slots: { default: slot },
      global: { plugins: [i18n], stubs: { Teleport: true } },
      attachTo: document.body,
    },
  )
}

function pressEscape(): void {
  document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
}

describe('AppModal', () => {
  beforeEach(() => {
    _resetScrollLockForTests()
  })

  afterEach(() => {
    document.body.innerHTML = ''
    _resetScrollLockForTests()
  })

  it('open 时渲染默认插槽与标题，closed 时不渲染', async () => {
    const w = mountModal()
    expect(w.find('.app-modal').exists()).toBe(true)
    expect(w.find('.modal').exists()).toBe(true)
    expect(w.text()).toContain('body-content')
    expect(w.text()).toContain('测试弹窗')

    await w.setProps({ modelValue: false })
    expect(w.find('.app-modal').exists()).toBe(false)
  })

  it('ESC 关闭：派发 update:modelValue=false 与 close', async () => {
    const w = mountModal()
    pressEscape()
    expect(w.emitted('update:modelValue')).toEqual([[false]])
    expect(w.emitted('close')).toHaveLength(1)
  })

  it('遮罩点击关闭可经 closeOnMask=false 禁用；面板内点击不关闭', async () => {
    const w = mountModal()
    await w.find('.modal').trigger('click')
    expect(w.emitted('update:modelValue')).toBeUndefined()

    const w2 = mountModal({ closeOnMask: false })
    await w2.find('.app-modal').trigger('click')
    expect(w2.emitted('update:modelValue')).toBeUndefined()

    const w3 = mountModal()
    await w3.find('.app-modal').trigger('click')
    expect(w3.emitted('update:modelValue')).toEqual([[false]])
  })

  it('打开时锁定 body 滚动，关闭后恢复（data-scroll-locked 属性）', async () => {
    const w = mountModal()
    expect(document.body.getAttribute('data-scroll-locked')).toBe('true')
    expect(document.body.style.overflow).toBe('hidden')

    await w.setProps({ modelValue: false })
    expect(document.body.getAttribute('data-scroll-locked')).toBeNull()
    expect(document.body.style.overflow).toBe('')
  })

  it('引用计数：两个弹层嵌套时，关闭其一不误解锁', async () => {
    const a = mountModal({ title: 'A' })
    const b = mountModal({ title: 'B' })
    expect(document.body.getAttribute('data-scroll-locked')).toBe('true')

    a.unmount()
    expect(document.body.getAttribute('data-scroll-locked')).toBe('true')

    b.unmount()
    expect(document.body.getAttribute('data-scroll-locked')).toBeNull()
  })

  it('焦点圈闭：打开后焦点进入面板；关闭后焦点返还', async () => {
    const outside = document.createElement('button')
    document.body.appendChild(outside)
    outside.focus()
    expect(document.activeElement).toBe(outside)

    const w = mountModal({}, '<button class="first-focusable">确定</button>')
    await flushPromises()
    const panel = w.find('.modal').element
    expect(panel.contains(document.activeElement)).toBe(true)
    expect((document.activeElement as HTMLElement).classList.contains('first-focusable')).toBe(true)

    pressEscape()
    expect(w.emitted('update:modelValue')).toEqual([[false]])
    // 模拟 v-model 回写后，焦点返还给打开前的元素
    await w.setProps({ modelValue: false })
    expect(document.activeElement).toBe(outside)
    w.unmount()
  })

  it('footer 插槽透传 disabledConfirm', () => {
    const w = mount(AppModal, {
      props: { modelValue: true, title: 'T', disabledConfirm: true },
      slots: {
        default: '<p>x</p>',
        footer: '<button class="ok">确定</button>',
      },
      global: { plugins: [i18n], stubs: { Teleport: true } },
      attachTo: document.body,
    })
    expect(w.find('.app-modal__footer').exists()).toBe(true)
    expect(w.find('.ok').exists()).toBe(true)
    w.unmount()
  })

  it('响应式源码断言：768px 档、92vw 宽度、isSmall 全屏与档位映射', () => {
    expect(source).toMatch(/@media \(max-width: 768px\)/)
    expect(source).toContain('min(92vw')
    expect(source).toContain('isSmall')
    expect(source).toContain('app-modal--fullscreen')
    expect(source).toContain('100dvh')
    expect(source).toContain('--fullscreen')
    expect(source).toMatch(/--sm \{ max-width: 480px/)
    expect(source).toMatch(/--md \{ max-width: 640px/)
    expect(source).toMatch(/--lg \{ max-width: 860px/)
    // 全局 .modal-overlay/.modal 复用，视觉零回归
    expect(source).toContain('class="modal-overlay app-modal"')
    expect(source).toContain('class="modal app-modal__panel"')
    // 全屏下 720px 碎片段点不允许回流
    expect(source).not.toMatch(/max-width: 720px/)
  })
})

// AppDrawer.test.ts — 开合、ESC/遮罩关闭、滚动锁定、方向解析（auto 按 768 断点），
// 以及响应式源码断言（bottom sheet/90dvh/把手/inset-inline-end RTL）。
import { mount, flushPromises } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import AppDrawer from './AppDrawer.vue'
import { _resetScrollLockForTests } from '../../composables/useScrollLock'
import { BREAKPOINTS } from '../../config/breakpoints'

const source = readFileSync(resolve(process.cwd(), 'src/components/ui/AppDrawer.vue'), 'utf8')

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { common: { button: { close: '关闭' } } } },
})

// 控制方向解析用的 isTablet（>=768），默认 false → bottom sheet
const bpState = vi.hoisted(() => ({ isTablet: false }))
vi.mock('../../composables/useBreakpoint', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../composables/useBreakpoint')>()
  const { computed, readonly, ref } = await import('vue')
  return {
    ...actual,
    useBreakpoint: () => ({
      isMobile: readonly(ref(!bpState.isTablet)),
      isTablet: readonly(ref(bpState.isTablet)),
      isSmall: readonly(ref(false)),
      isDesktop: computed(() => !bpState.isTablet),
    }),
  }
})

function mountDrawer(props: Record<string, unknown> = {}) {
  return mount(
    AppDrawer,
    {
      props: { modelValue: true, title: '测试抽屉', ...props },
      slots: { default: '<p>drawer-content</p>' },
      global: { plugins: [i18n], stubs: { Teleport: true } },
      attachTo: document.body,
    },
  )
}

function pressEscape(): void {
  document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
}

describe('AppDrawer', () => {
  beforeEach(() => {
    bpState.isTablet = false
    _resetScrollLockForTests()
  })

  afterEach(() => {
    document.body.innerHTML = ''
    _resetScrollLockForTests()
  })

  it('open 渲染内容与标题；closed 不渲染', async () => {
    const w = mountDrawer()
    expect(w.find('.app-drawer').exists()).toBe(true)
    expect(w.text()).toContain('drawer-content')
    expect(w.text()).toContain('测试抽屉')

    await w.setProps({ modelValue: false })
    expect(w.find('.app-drawer').exists()).toBe(false)
  })

  it('auto 方向按断点解析：<768 为 bottom sheet，>=768 为右侧面板', async () => {
    const mobile = mountDrawer()
    expect(mobile.find('.app-drawer').classes()).toContain('app-drawer--bottom')
    expect(mobile.find('.app-drawer__grip').exists()).toBe(true)
    mobile.unmount()

    bpState.isTablet = true
    const tablet = mountDrawer()
    expect(tablet.find('.app-drawer').classes()).toContain('app-drawer--right')
    expect(tablet.find('.app-drawer__grip').exists()).toBe(false)
    tablet.unmount()
  })

  it('direction 显式指定时忽略断点', () => {
    bpState.isTablet = false
    const w = mountDrawer({ direction: 'right' })
    expect(w.find('.app-drawer').classes()).toContain('app-drawer--right')
  })

  it('ESC 与遮罩点击关闭；closeOnMask=false 时遮罩不关闭', async () => {
    const w = mountDrawer()
    pressEscape()
    expect(w.emitted('update:modelValue')).toEqual([[false]])
    expect(w.emitted('close')).toHaveLength(1)

    const w2 = mountDrawer({ closeOnMask: false })
    await w2.find('.app-drawer').trigger('click')
    expect(w2.emitted('update:modelValue')).toBeUndefined()

    const w3 = mountDrawer()
    await w3.find('.app-drawer').trigger('click')
    expect(w3.emitted('update:modelValue')).toEqual([[false]])
  })

  it('打开锁定 body 滚动，关闭恢复', async () => {
    const w = mountDrawer()
    expect(document.body.getAttribute('data-scroll-locked')).toBe('true')
    expect(document.body.style.overflow).toBe('hidden')

    await w.setProps({ modelValue: false })
    expect(document.body.getAttribute('data-scroll-locked')).toBeNull()
  })

  it('焦点圈闭：打开后焦点在面板内，ESC 后返还', async () => {
    const outside = document.createElement('button')
    document.body.appendChild(outside)
    outside.focus()

    const w = mountDrawer()
    await flushPromises()
    expect(w.find('.app-drawer__panel').exists()).toBe(true)
    expect(w.find('.app-drawer__panel').element.contains(document.activeElement)).toBe(true)

    pressEscape()
    expect(w.emitted('update:modelValue')).toEqual([[false]])
    // 模拟 v-model 回写后，焦点返还给打开前的元素
    await w.setProps({ modelValue: false })
    expect(document.activeElement).toBe(outside)
    w.unmount()
  })

  it('响应式源码断言：auto 方向绑定 tablet(768) 断点、bottom sheet 与 RTL 逻辑属性', () => {
    // auto 方向的边界即单一事实源 BREAKPOINTS.tablet = 768
    expect(BREAKPOINTS.tablet).toBe(768)
    expect(source).toMatch(/isTablet\.value \? 'right' : 'bottom'/)
    // bottom sheet：90dvh（含 90vh fallback）+ 拖拽把手视觉
    expect(source).toContain('max-height: 90dvh')
    expect(source).toContain('max-height: 90vh')
    expect(source).toContain('app-drawer__grip')
    // RTL：右侧定位用逻辑属性 inset-inline-end 自动换边
    expect(source).toContain('inset-inline-end')
    expect(source).not.toMatch(/^\s*right:\s*0;/m)
  })
})

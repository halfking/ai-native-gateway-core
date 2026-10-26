import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { h, nextTick } from 'vue'
import { setLocale } from '@/i18n'
import AppSheet from './AppSheet.vue'

const closeOverlay = vi.hoisted(() => vi.fn())
const topOverlay = vi.hoisted(() => ({ value: false }))
const presentOverlay = vi.hoisted(() => vi.fn(() => { topOverlay.value = true }))

vi.mock('@/hyper', () => ({
  useHyperOverlay: () => ({
    isTop: { get value() { return topOverlay.value } },
    present: presentOverlay,
    close: closeOverlay,
    release: vi.fn(),
  }),
}))

describe('AppSheet mobile focus behavior', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.unstubAllGlobals()
    // 必须还原 prototype 上的 spy：否则上一条用例 mock 掉的
    // HTMLElement.prototype.focus 会漏到下一条，导致 background.focus()
    // 什么都没做、document.activeElement 停在 body 上（2026-10-12 实测踩到）。
    vi.restoreAllMocks()
    closeOverlay.mockClear()
    presentOverlay.mockClear()
    topOverlay.value = false
    setLocale('en-US')
  })

  it('registers a sheet that is mounted already open', async () => {
    const wrapper = mount(AppSheet, {
      props: { modelValue: true, title: '供应商详情' },
      attachTo: document.body,
    })

    await nextTick()

    expect(presentOverlay).toHaveBeenCalledTimes(1)
    expect(wrapper.find('.app-sheet--inactive').exists()).toBe(false)
    wrapper.unmount()
  })

  it('does not focus a text/select/action control when opened (close or panel only)', async () => {
    // 2026-10-17：旧逻辑在 pointer:fine 时会 focus 第一个业务 button
    // （供应商详情「解除手动停用」→ window.prompt），iOS 模拟器正好是 fine。
    // 现在一律优先 close，没有本地头时 focus panel。
    setLocale('zh-CN')
    vi.stubGlobal('matchMedia', vi.fn((q: string) => ({
      matches: String(q).includes('pointer: coarse') || String(q).includes('hover: none'),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })))
    const focused: HTMLElement[] = []
    vi.spyOn(HTMLElement.prototype, 'focus').mockImplementation(function (this: HTMLElement) {
      focused.push(this)
    })
    const wrapper = mount(AppSheet, {
      props: { modelValue: false, title: '筛选条件' },
      slots: {
        default: () => h('div', [
          h('button', { id: 'danger-action', type: 'button' }, '解除手动停用'),
          h('input', { id: 'search-choice' }),
          h('select', { id: 'choice' }, [h('option', '全部')]),
        ]),
      },
      attachTo: document.body,
    })

    await wrapper.setProps({ modelValue: true })
    await nextTick()
    await flushPromises()

    expect(document.body.querySelector('#search-choice')).not.toBeNull()
    expect(focused).toHaveLength(1)
    expect(focused[0]).toBe(document.body.querySelector('.app-sheet__close'))
    expect(focused[0]).not.toBe(document.body.querySelector('#danger-action'))

    wrapper.unmount()
  })

  it('compact sheet hides duplicate title but keeps close for a11y', async () => {
    // max-width:599px ⇒ 不画 h2 标题（避免与顶栏双标题），但保留 ✕
    // （iOS Maestro 搜不到顶栏 icon 的 aria-label）。
    vi.stubGlobal('matchMedia', vi.fn((q: string) => ({
      matches: String(q).includes('max-width: 599px'),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    })))
    const focused: HTMLElement[] = []
    vi.spyOn(HTMLElement.prototype, 'focus').mockImplementation(function (this: HTMLElement) {
      focused.push(this)
    })
    const wrapper = mount(AppSheet, {
      props: { modelValue: true, presentation: 'sheet', title: 'Anthropic' },
      slots: {
        default: () => h('div', [h('button', { id: 'danger-action', type: 'button' }, '解除手动停用')]),
      },
      attachTo: document.body,
    })
    await nextTick()
    await flushPromises()

    expect(document.body.querySelector('.app-sheet__header')).not.toBeNull()
    expect(document.body.querySelector('.app-sheet__title')).toBeNull()
    expect(document.body.querySelector('.app-sheet__close')).not.toBeNull()
    expect(focused[0]).toBe(document.body.querySelector('.app-sheet__close'))
    expect(focused[0]).not.toBe(document.body.querySelector('#danger-action'))

    wrapper.unmount()
  })

  it('wires the close icon to the overlay close action', async () => {
    const wrapper = mount(AppSheet, {
      props: { modelValue: false, title: '设置' },
      attachTo: document.body,
    })

    await wrapper.setProps({ modelValue: true })
    await nextTick()
    document.body.querySelector<HTMLButtonElement>('.app-sheet__close')?.click()

    expect(closeOverlay).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('blurs the background input when it opens, so iOS AutoFill cannot swallow taps', async () => {
    // 2026-10-12 实测（iOS 26.3 模拟器，供应商列表 → 点卡片 → 弹详情）：
    // 列表顶部的搜索框保持 focus 时，iOS 的自动填充条一直浮在弹层上方，
    // 且落在页面上的点按会被判成「长按文本」→ 弹出「粘贴 / 自动填充」浮层，
    // ✕ 按钮根本收不到那一下 ⇒ 用户看到的现象是「弹窗关不掉」。
    //
    // 判据直接盯 blur 的接收者，而不是盯 document.activeElement 的最终值 ——
    // 因为弹层自己随后会把焦点移到 ✕ 上，即使没有 blur，activeElement 也变了，
    // 那样这条判据在删掉修复后仍然是绿的 ⇒ 等于没钉住。
    const background = document.createElement('input')
    background.type = 'search'
    background.id = 'provider-search'
    document.body.appendChild(background)
    background.focus()
    expect(document.activeElement).toBe(background)

    const blurSpy = vi.spyOn(HTMLElement.prototype, 'blur')
    const wrapper = mount(AppSheet, {
      props: { modelValue: false, title: '供应商详情' },
      attachTo: document.body,
    })

    await wrapper.setProps({ modelValue: true })
    await nextTick()

    expect(blurSpy.mock.instances[0]).toBe(background)
    expect(blurSpy).toHaveBeenCalled()

    wrapper.unmount()
  })

  it('does not let a lower sheet close when its scrim receives a nested-overlay gesture', async () => {
    const wrapper = mount(AppSheet, {
      props: { modelValue: false, title: '账户设置' },
      attachTo: document.body,
    })

    await wrapper.setProps({ modelValue: true })
    topOverlay.value = false
    document.body.querySelector<HTMLElement>('.app-sheet__scrim')?.dispatchEvent(new MouseEvent('click', { bubbles: true }))

    expect(closeOverlay).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

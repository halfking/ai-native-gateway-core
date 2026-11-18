// CollapsibleFilterSection.test.ts — 折叠筛选容器行为契约（2026-11-18 筛选收纳轮）。
// 形态依据业界口径：控件默认收拢、生效条件 chips 常显、头部一行常驻。
//
// ⚠️ jsdom 口径：不用 wrapper.isVisible() 断言 v-show 面板——它走
// getComputedStyle 级联（含祖先链），jsdom 对 scoped CSS 注入/缓存不可靠，
// 同一状态两次断言结果可能相反。改用两个可靠信号：
// - aria-expanded（直接绑定组件 expanded 状态）；
// - panel.style.display（v-show 的直接作用点，'none'=收拢）。
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import CollapsibleFilterSection from './CollapsibleFilterSection.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { common: { button: { filter: '筛选', clear: '清除' } } } },
})

/** 展开态断言：aria-expanded=true 且面板内联 display 非 none */
function expectPanelState(w: VueWrapper, open: boolean) {
  expect(w.find('.cfs__toggle').attributes('aria-expanded')).toBe(String(open))
  const panel = w.find('.cfs__panel').element as HTMLElement
  expect(panel.style.display === 'none').toBe(!open)
}

function mountSection(props: Record<string, unknown> = {}, slots: Record<string, unknown> = {}) {
  return mount(CollapsibleFilterSection, {
    props,
    slots: { default: '<select class="probe"><option>a</option></select>', ...slots },
    global: { plugins: [i18n] },
  })
}

describe('CollapsibleFilterSection', () => {
  it('默认收拢：aria-expanded=false，面板 display:none，头部开关可见', () => {
    const w = mountSection()
    expect(w.find('.cfs__toggle').exists()).toBe(true)
    expectPanelState(w, false)
  })

  it('单击开关展开/收拢，面板内 slot 控件保留（v-show 不销毁）', async () => {
    const w = mountSection({}, { actions: '<button class="act">查询</button>' })
    await w.find('.cfs__toggle').trigger('click')
    expectPanelState(w, true)
    expect(w.find('.probe').exists()).toBe(true)
    expect(w.find('.act').exists()).toBe(true)
    await w.find('.cfs__toggle').trigger('click')
    expectPanelState(w, false)
    // v-show：控件仍在 DOM（保留未提交输入）
    expect(w.find('.probe').exists()).toBe(true)
  })

  it('生效计数来自 chips.length，badge 显示且头部出现清空按钮', async () => {
    const w = mountSection({
      chips: [
        { key: 'a', label: '状态: 成功', onRemove: () => {} },
        { key: 'b', label: '供应商: OpenAI', onRemove: () => {} },
      ],
    })
    expect(w.find('.cfs__badge').text()).toBe('2')
    expect(w.find('.cfs__reset').exists()).toBe(true)
    await w.find('.cfs__reset').trigger('click')
    expect(w.emitted('reset')).toHaveLength(1)
  })

  it('无生效条件：无 badge、无清空按钮', () => {
    const w = mountSection()
    expect(w.find('.cfs__badge').exists()).toBe(false)
    expect(w.find('.cfs__reset').exists()).toBe(false)
  })

  it('chips 单击触发对应 onRemove（收拢状态下也可移除单项）', async () => {
    const onRemove = vi.fn()
    const w = mountSection({ chips: [{ key: 'a', label: 'X: 1', onRemove }] })
    await w.find('.cfs__chip').trigger('click')
    expect(onRemove).toHaveBeenCalledTimes(1)
  })

  it('activeCount 显式传入时优先于 chips.length', () => {
    const w = mountSection({ activeCount: 5, chips: [{ key: 'a', label: 'X', onRemove: () => {} }] })
    expect(w.find('.cfs__badge').text()).toBe('5')
  })

  it('自动展开：生效条件从 0 → >0 且用户未手动操作过 ⇒ 面板展开一次', async () => {
    const w = mountSection({ chips: [] })
    expectPanelState(w, false)
    await w.setProps({ chips: [{ key: 'a', label: 'X: 1', onRemove: () => {} }] })
    expectPanelState(w, true)
  })

  it('用户手动收拢后，生效条件变化不再自动展开（显式动作优先）', async () => {
    const w = mountSection({ chips: [{ key: 'a', label: 'X: 1', onRemove: () => {} }] })
    // 初始有条件 ⇒ 自动展开
    expectPanelState(w, true)
    await w.find('.cfs__toggle').trigger('click') // 手动收拢
    expectPanelState(w, false)
    // 条件数变化（1 → 2）不再自动展开
    await w.setProps({
      chips: [
        { key: 'a', label: 'X: 1', onRemove: () => {} },
        { key: 'b', label: 'Y: 2', onRemove: () => {} },
      ],
    })
    expectPanelState(w, false)
  })

  it('头部 chips 区单击不触发展开/收拢（.stop 隔离）', async () => {
    const w = mountSection({ chips: [{ key: 'a', label: 'X: 1', onRemove: () => {} }] })
    // 初始自动展开态；点 chip 不应改变展开状态
    await w.find('.cfs__chip').trigger('click')
    expectPanelState(w, true)
  })

  it('label 覆盖默认开关文案', () => {
    const w = mountSection({ label: '过滤条件' })
    expect(w.find('.cfs__toggle').text()).toContain('过滤条件')
  })

  it('toggle 事件随开关发出且载荷为最新状态', async () => {
    const w = mountSection()
    await w.find('.cfs__toggle').trigger('click')
    expect(w.emitted('toggle')).toEqual([[true]])
    await w.find('.cfs__toggle').trigger('click')
    expect(w.emitted('toggle')).toEqual([[true], [false]])
  })
})

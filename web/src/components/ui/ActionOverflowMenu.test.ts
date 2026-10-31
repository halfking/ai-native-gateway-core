// ActionOverflowMenu.test.ts — 溢出菜单的行为契约（2026-10-27 · 第 25 增量）
//
// ★ 这道门守护的是第 25 增量的**核心承诺**：
//   「低频动作不该删（那是丢需求），但不该与高频动作同排同重（那是找不着）」
//   ⇒ 菜单必须**能打开**、**项有标签**、**点了真的会触发**、**禁用的点不动**。
//
// ★ 断言挂的是**真实 zh-CN 语言包**（src/test-utils/i18n.ts）：
//   空 messages 时 `t('common.button.more')` 会返回**键路径本身**
//   ⇒ 「界面上应当出现『更多』」的断言会失配，而结构性断言照样过 ⇒ 半绿不绿。
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import ActionOverflowMenu from './ActionOverflowMenu.vue'
import { testI18n } from '../../test-utils/i18n'

const items = [
  { key: 'a', label: '一键建设' },
  { key: 'b', label: 'OAuth 桥接' },
  { key: 'c', label: '粘贴', disabled: true },
  { key: 'd', label: '清空模型', danger: true, title: '清空当前 Provider 的全部模型' },
]

function mountMenu(list = items) {
  return mount(ActionOverflowMenu, {
    props: { items: list },
    global: { plugins: [testI18n], stubs: { Teleport: true } },
  })
}

describe('ActionOverflowMenu', () => {
  it('触发器文案取自共享键 common.button.more（「更多」只写一份）', () => {
    const w = mountMenu()
    const trigger = w.get('[data-testid="action-overflow-trigger"]')
    expect(trigger.text()).toContain('更多')
    expect(trigger.text()).not.toContain('common.button.more')
  })

  it('初始状态下菜单不渲染（不是一打开就铺满一屏）', () => {
    const w = mountMenu()
    expect(w.find('[data-testid="action-overflow-menu"]').exists()).toBe(false)
    expect(w.get('[data-testid="action-overflow-trigger"]').attributes('aria-expanded')).toBe('false')
  })

  it('点击触发器展开菜单，aria-expanded 同步', async () => {
    const w = mountMenu()
    await w.get('[data-testid="action-overflow-trigger"]').trigger('click')
    expect(w.find('[data-testid="action-overflow-menu"]').exists()).toBe(true)
    expect(w.get('[data-testid="action-overflow-trigger"]').attributes('aria-expanded')).toBe('true')
  })

  it('菜单项逐条渲染自己的标签（折叠后仍看得见是什么）', async () => {
    const w = mountMenu()
    await w.get('[data-testid="action-overflow-trigger"]').trigger('click')
    const texts = w.findAll('[role="menuitem"]').map((n) => n.text())
    expect(texts).toEqual(['一键建设', 'OAuth 桥接', '粘贴', '清空模型'])
  })

  it('点击菜单项抛出 select 并带上它的 key', async () => {
    const w = mountMenu()
    await w.get('[data-testid="action-overflow-trigger"]').trigger('click')
    await w.findAll('[role="menuitem"]')[1]!.trigger('click')
    expect(w.emitted('select')).toEqual([['b']])
  })

  it('禁用的项点不动（不抛 select）', async () => {
    const w = mountMenu()
    await w.get('[data-testid="action-overflow-trigger"]').trigger('click')
    const disabled = w.findAll('[role="menuitem"]')[2]!
    expect(disabled.attributes('aria-disabled')).toBe('true')
    await disabled.trigger('click')
    expect(w.emitted('select')).toBeUndefined()
  })

  it('破坏性动作带危险标记（清空模型不能和普通项长得一样）', async () => {
    const w = mountMenu()
    await w.get('[data-testid="action-overflow-trigger"]').trigger('click')
    const danger = w.findAll('[role="menuitem"]')[3]!
    expect(danger.classes()).toContain('action-overflow__item--danger')
  })

  it('菜单项保留解释性 tooltip（折叠后唯一还能说明白的渠道）', async () => {
    const w = mountMenu()
    await w.get('[data-testid="action-overflow-trigger"]').trigger('click')
    const danger = w.findAll('[role="menuitem"]')[3]!
    expect(danger.attributes('title')).toBe('清空当前 Provider 的全部模型')
  })

  it('选中后菜单收起（一次只处理一件事）', async () => {
    const w = mountMenu()
    await w.get('[data-testid="action-overflow-trigger"]').trigger('click')
    await w.findAll('[role="menuitem"]')[0]!.trigger('click')
    expect(w.find('[data-testid="action-overflow-menu"]').exists()).toBe(false)
  })

  it('Escape 关闭菜单', async () => {
    const w = mountMenu()
    await w.get('[data-testid="action-overflow-trigger"]').trigger('click')
    expect(w.find('[data-testid="action-overflow-menu"]').exists()).toBe(true)
    await w.get('[data-testid="action-overflow-menu"]').trigger('keydown', { key: 'Escape' })
    expect(w.find('[data-testid="action-overflow-menu"]').exists()).toBe(false)
  })

  it('触发器带 aria-haspopup=menu（读屏知道这是个菜单）', () => {
    const w = mountMenu()
    expect(w.get('[data-testid="action-overflow-trigger"]').attributes('aria-haspopup')).toBe('menu')
  })

  it('菜单本体在 Teleport 里（不被父级裁切；判据据此把它算作浮层）', async () => {
    // ★ 这条断言钉住判据与组件之间的一致性：
    //   源码门把「Teleport 内的动作区」判为浮层（逐项弹出，不构成同屏竞争）
    //   ⇒ 组件必须真的把菜单放进 Teleport，否则门与实现对不上。
    // ★ 验证方式是**位置**，不是类名：菜单必须出现在 document.body 上，
    //   且**不**留在组件原位（w.html() 里只剩 teleport 锚点）。
    const w = mount(ActionOverflowMenu, {
      props: { items },
      global: { plugins: [testI18n] },
      attachTo: document.body,
    })
    await w.get('[data-testid="action-overflow-trigger"]').trigger('click')

    expect(document.body.querySelector('.action-overflow__menu')).not.toBeNull()
    // 原位只剩 teleport 锚点，菜单本体不在组件树里
    expect(w.html()).not.toContain('action-overflow__menu')
    expect(w.html()).toContain('teleport start')

    w.unmount()
  })
})

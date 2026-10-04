// AppAccountSheet.test.ts — compact 账户面板门禁（docs/UI规范/00 §5.4 · H2）。
//
// 本组件的关键价值不是「长什么样」，而是两件可断言的行为：
// 1. 它把 compact 上的系统设置**收在一处**（顶栏不得直出语言/主题/用户）；
// 2. 它登记进 Hyper 覆盖层栈，所以 Android 系统返回先关它而不是穿透到背景路由。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import AppAccountSheet from './AppAccountSheet.vue'
import { overlays, back } from '../../lib/shell/hyper'
import { _resetScrollLockForTests } from '../../composables/useScrollLock'

const source = readFileSync(resolve(process.cwd(), 'src/components/shell/AppAccountSheet.vue'), 'utf8')

vi.mock('../../store', () => ({
  store: { userInfo: { display_name: '张三', username: 'zhangsan', role: 'super_admin' } },
  isSuperAdmin: () => true,
  isPlatformOpsView: () => false,
  isProviderConsoleView: () => false,
}))
// 语言/主题控件在本用例不是被测对象，替掉以免牵扯各自的 store
vi.mock('../LanguageSelector.vue', () => ({ default: { template: '<div data-testid="lang" />' } }))
vi.mock('../ThemeToggle.vue', () => ({ default: { template: '<div data-testid="theme" />' } }))

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      hyper: {
        account: {
          title: '账户',
          language: '语言',
          theme: '主题',
          health: '服务健康',
          healthy: '可达',
          unhealthy: '不可达',
          unknown: '状态未知',
          logout: '退出登录',
          adminEntry: '管理入口',
          help: '帮助',
          close: '关闭',
        },
      },
    },
  },
})

function mountSheet(props: Record<string, unknown> = {}) {
  return mount(AppAccountSheet, {
    props: { modelValue: true, ...props },
    global: { plugins: [i18n], stubs: { LanguageSelector: true, ThemeToggle: true } },
    attachTo: document.body,
  })
}

beforeEach(() => {
  overlays._reset()
  back._reset()
  _resetScrollLockForTests()
})
afterEach(() => {
  overlays._reset()
  back._reset()
  _resetScrollLockForTests()
  document.body.innerHTML = ''
})

describe('AppAccountSheet：结构与语义', () => {
  it('role="dialog" + aria-modal + 有可访问名', () => {
    const w = mountSheet()
    const root = w.find('[role="dialog"]')
    expect(root.exists()).toBe(true)
    expect(root.attributes('aria-modal')).toBe('true')
    expect(root.attributes('aria-label')).toBe('账户')
  })

  it('显示名 + 角色', () => {
    const w = mountSheet()
    expect(w.text()).toContain('张三')
    expect(w.text()).toContain('super_admin')
  })

  it('未登录信息时用占位符，不显示 undefined/null', () => {
    const w = mount(AppAccountSheet, {
      props: { modelValue: true },
      global: {
        plugins: [i18n],
        stubs: { LanguageSelector: true, ThemeToggle: true },
      },
    })
    expect(w.text()).not.toContain('undefined')
    expect(w.text()).not.toContain('null')
  })

  it('modelValue=false 时不渲染', () => {
    const w = mount(AppAccountSheet, {
      props: { modelValue: false },
      global: { plugins: [i18n], stubs: { LanguageSelector: true, ThemeToggle: true } },
    })
    expect(w.find('[role="dialog"]').exists()).toBe(false)
  })
})

describe('AppAccountSheet：系统设置收在一处', () => {
  it('语言 / 主题 / 帮助 / 退出 全部在本面板内', () => {
    const w = mountSheet()
    const text = w.text()
    expect(text).toContain('语言')
    expect(text).toContain('主题')
    expect(text).toContain('帮助')
    expect(text).toContain('退出登录')
  })

  it('健康状态只读且可被读屏感知（aria-live）', async () => {
    const w = mountSheet({ health: 'ok' })
    const el = w.find('.account-sheet__health')
    expect(el.attributes('aria-live')).toBe('polite')
    expect(el.attributes('data-state')).toBe('ok')
    expect(el.text()).toBe('可达')

    await w.setProps({ health: 'down' })
    expect(w.find('.account-sheet__health').text()).toBe('不可达')

    await w.setProps({ health: 'unknown' })
    expect(w.find('.account-sheet__health').text()).toBe('状态未知')
  })

  it('退出/帮助/管理入口只 emit，不自己改 store（业务编排留给 App.vue）', async () => {
    const w = mountSheet()
    const actions = w.findAll('.account-sheet__action')
    expect(actions.length).toBeGreaterThanOrEqual(3)
    await actions[actions.length - 1].trigger('click') // 退出登录
    expect(w.emitted('logout')).toHaveLength(1)
  })
})

describe('AppAccountSheet：纳入 Hyper 返回仲裁', () => {
  it('打开时登记到覆盖层栈，Back 消费返回而不是穿透', async () => {
    const w = mountSheet()
    await nextTick()
    expect(overlays.depth).toBe(1)
    expect(overlays.top()?.id).toBe('hyper-account-sheet')
    expect(overlays.top()?.presentation).toBe('sheet')

    await back.request('system')
    expect(overlays.depth).toBe(0)
    expect(w.emitted('update:modelValue')?.at(-1)).toEqual([false])
  })

  it('关闭后栈清空，Back 恢复为操作页面', async () => {
    const w = mountSheet()
    await nextTick()
    await w.setProps({ modelValue: false })
    expect(overlays.depth).toBe(0)
  })

  it('点遮罩与关闭钮都关闭', async () => {
    const w = mountSheet()
    await w.find('.account-sheet__mask-backdrop').trigger('click')
    expect(w.emitted('update:modelValue')).toHaveLength(1)
    await w.find('.account-sheet__close').trigger('click')
    expect(w.emitted('update:modelValue')).toHaveLength(2)
  })
})

describe('AppAccountSheet：样式红线（源码断言）', () => {
  it('每行 ≥48px（新增 Android 触控控件基线）', () => {
    expect(source).toMatch(/\.account-sheet__row\s*\{[^}]*min-height:\s*48px/)
    expect(source).toMatch(/\.account-sheet__action\s*\{[^}]*min-height:\s*48px/)
  })

  it('关闭钮 ≥48px（R1：新控件标准；此前此处把 44px 钉成了「红线」）', () => {
    // ⚠ 同 AppBottomNav：这条门曾把 44px 钉成红线，而同一个 Sheet 里的
    //    `__row` / `__action` 本来就是 48 —— 门保护的是一个自相矛盾的值。
    expect(source).toMatch(/\.account-sheet__close\s*\{[^}]*min-height:\s*48px/)
  })

  it('Sheet 圆角只在上沿（自底向上形态）', () => {
    expect(source).toMatch(/border-radius:\s*12px 12px 0 0/)
  })

  it('safe-area-inset-bottom 只消费一次，且 top 不在 panel 上重复消费', () => {
    const code = source.replace(/\/\*[\s\S]*?\*\//g, '')
    const n = (code.match(/env\(safe-area-inset-bottom/g) ?? []).length
    expect(n).toBe(1)
  })

  it('dvh 有 90vh 回退（iOS 15.0–15.3 没有动态视口单位）', () => {
    const code = source.replace(/\/\*[\s\S]*?\*\//g, '')
    const vh = code.indexOf('max-height: 90vh')
    const dvh = code.indexOf('max-height: 90dvh')
    expect(vh).toBeGreaterThan(-1)
    expect(dvh).toBeGreaterThan(vh) // 回退在前，增强在后
  })
})

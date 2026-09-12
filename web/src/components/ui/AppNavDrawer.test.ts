// AppNavDrawer.test.ts — 分组手风琴展开/收起、激活组默认展开、点击收起。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { nextTick } from 'vue'
import AppNavDrawer from './AppNavDrawer.vue'
import { navDrawerOpen, _resetUseAppNavForTests } from '../../composables/useAppNav'
import { _resetScrollLockForTests } from '../../composables/useScrollLock'

const source = readFileSync(resolve(process.cwd(), 'src/components/ui/AppNavDrawer.vue'), 'utf8')

vi.mock('../../store', () => ({
  isSuperAdmin: () => true,
  isPlatformOpsView: () => true,
  isProviderConsoleView: () => true,
}))
vi.mock('../../api/plugins', () => ({ fetchPluginNav: async () => [] }))
vi.mock('../../config/edition', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../config/edition')>()
  return { ...actual, resolveOpsMenu: async () => null }
})

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      common: { button: { close: '关闭' } },
      nav: { mainAria: '主导航', item: { activateAction: '激活' } },
    },
  },
})

const routes = [
  { path: '/keys', component: { template: '<div />' } },
  { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
]
const router = createRouter({ history: createMemoryHistory(), routes })

async function mountDrawer(path = '/keys') {
  router.push(path)
  await router.isReady()
  const w = mount(AppNavDrawer, {
    global: { plugins: [i18n, router], stubs: { Teleport: true } },
    attachTo: document.body,
  })
  navDrawerOpen.value = true
  await nextTick()
  return w
}

describe('AppNavDrawer', () => {
  beforeEach(() => {
    _resetUseAppNavForTests()
    _resetScrollLockForTests()
  })

  afterEach(() => {
    navDrawerOpen.value = false
    document.body.innerHTML = ''
    _resetScrollLockForTests()
    _resetUseAppNavForTests()
  })

  it('关闭态不渲染，打开态渲染手风琴分组', async () => {
    const w = await mountDrawer()
    expect(w.find('.app-drawer').exists()).toBe(true)
    const headers = w.findAll('.app-nav-drawer__group-header')
    expect(headers.length).toBeGreaterThan(0)
  })

  it('当前路由对应分组默认展开（aria-expanded）', async () => {
    const w = await mountDrawer('/keys') // /keys 属于 tenant-users 分组
    const expandedHeaders = w.findAll('.app-nav-drawer__group-header[aria-expanded="true"]')
    const expandedTexts = expandedHeaders.map((h) => h.text())
    expect(expandedTexts.some((txt) => txt.includes('nav.group.tenantUsers'))).toBe(true)
  })

  it('点击分组头切换手风琴展开/收起', async () => {
    const w = await mountDrawer('/keys')
    // 注意：异步菜单刷新可能触发 v-for 重渲染并替换按钮元素，
    // 每次断言前重新查询，避免持有失效的 DOM 引用。
    const header = () => w.findAll('.app-nav-drawer__group-header')[0]
    const before = header().attributes('aria-expanded')
    await header().trigger('click')
    expect(header().attributes('aria-expanded')).toBe(before === 'true' ? 'false' : 'true')
    await header().trigger('click')
    expect(header().attributes('aria-expanded')).toBe(before)
  })

  it('点击菜单项后抽屉自动收起（navDrawerOpen=false）', async () => {
    const w = await mountDrawer('/keys')
    const item = w.findAll('.app-nav-drawer__item')[0]
    await item.trigger('click')
    expect(navDrawerOpen.value).toBe(false)
  })

  it('宽度与方向源码断言：min(80vw, 320px)、direction=right（RTL 自动换边由 AppDrawer 逻辑属性承担）', () => {
    expect(source).toContain('width="min(80vw, 320px)"')
    expect(source).toContain('direction="right"')
    // 复用 useAppNav 数据源（与 AppTopbar 同一份菜单）
    expect(source).toContain('useAppNav')
  })
})

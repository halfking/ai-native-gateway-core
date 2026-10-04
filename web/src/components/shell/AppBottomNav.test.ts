// AppBottomNav.test.ts — compact 底栏门禁（docs/UI规范/00 §5.4 · H2）。
//
// jsdom 没有真实布局，所以「仅 compact 渲染」「席位数」「触控高度」这类
// 只能通过**窗口档 mock + 源码断言**验证 —— 这是本仓既有的响应式测试模式
// （见 AppTopbar.responsive.test.ts），沿用而不是另造。
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppBottomNav from './AppBottomNav.vue'
import { _resetForTests } from '../../composables/useWindowClass'

const source = readFileSync(resolve(process.cwd(), 'src/components/shell/AppBottomNav.vue'), 'utf8')

// useAppNav 依赖 store / 插件 API / edition；这里给最小替身，让底栏只测自己的职责。
vi.mock('../../store', () => ({
  store: { userInfo: { role: 'admin' } },
  isSuperAdmin: () => true,
  isPlatformOpsView: () => false,
  isProviderConsoleView: () => false,
}))
vi.mock('../../api/plugins', () => ({ fetchPluginNav: async () => [] }))
vi.mock('../../config/edition', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../config/edition')>()
  return { ...actual, resolveOpsMenu: async () => null, onMaintainAvailabilityChange: () => () => {} }
})

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: {
    'zh-CN': {
      hyper: {
        bottomNav: { ariaLabel: '主导航', more: '更多' },
        },
    },
  },
})

const PRIMARY = [
  { path: '/dashboard', label: '总览', labelKey: 'nav.item.overview', icon: '📊', exact: true },
  { path: '/requests', label: '请求', labelKey: 'nav.item.requests', icon: '📈' },
  { path: '/chat', label: '对话', labelKey: 'nav.item.chat', icon: '💬' },
  { path: '/models', label: '模型', labelKey: 'nav.item.models', icon: '🧠' },
  { path: '/keys', label: '密钥', labelKey: 'nav.item.keys', icon: '🔑' },
]

vi.mock('../../composables/useAppNav', () => ({
  useAppNav: () => ({
    navPrimaryResolved: { value: PRIMARY.map((item) => ({ item, resolved: { path: item.path } })) },
    navLabel: (_k: string | undefined, fallback: string) => fallback,
  }),
}))

const routes = [
  { path: '/dashboard', component: { template: '<div />' } },
  { path: '/requests', component: { template: '<div />' } },
  { path: '/chat', component: { template: '<div />' } },
  { path: '/models', component: { template: '<div />' } },
  { path: '/keys', component: { template: '<div />' } },
  { path: '/:pathMatch(.*)*', component: { template: '<div />' } },
]

/** 把 useWindowClass 单例钉在指定档位。 */
function mockWindowClass(cls: 'compact' | 'medium' | 'expanded' | 'large'): void {
  _resetForTests()
  const bounds: Record<string, [number, number]> = {
    compact: [0, 767.98],
    medium: [768, 1023.98],
    expanded: [1024, 1439.98],
    large: [1440, 4000],
  }
  const [lo, hi] = bounds[cls]
  const width = lo
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => {
      const min = query.match(/min-width:\s*([\d.]+)px/)
      const max = query.match(/max-width:\s*([\d.]+)px/)
      const matches = min ? width >= parseFloat(min[1]) : max ? width <= parseFloat(max[1]) : false
      return {
        matches,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      }
    },
  })
  Object.defineProperty(window, 'innerWidth', { writable: true, configurable: true, value: width })
}

async function mountAt(path: string, cls: 'compact' | 'medium' | 'expanded' | 'large') {
  mockWindowClass(cls)
  const router = createRouter({ history: createMemoryHistory(), routes })
  await router.push(path)
  await router.isReady()
  return mount(AppBottomNav, { global: { plugins: [i18n, router] } })
}

afterEach(() => _resetForTests())

describe('AppBottomNav：仅 compact 渲染', () => {
  it('compact 档渲染底栏', async () => {
    const w = await mountAt('/dashboard', 'compact')
    expect(w.find('[data-testid="app-bottom-nav"]').exists()).toBe(true)
  })

  it.each(['medium', 'expanded', 'large'] as const)('%s 档不渲染（桌面零回归）', async (cls) => {
    const w = await mountAt('/dashboard', cls)
    expect(w.find('[data-testid="app-bottom-nav"]').exists()).toBe(false)
  })

  it('用 v-if 而不是 CSS 隐藏，保证桌面 DOM 里根本不存在该节点', () => {
    // v-if 才能让「未渲染」可被 querySelector 直接断言；display:none 断言不到「不存在」
    expect(source).toMatch(/v-if="isCompact"/)
  })
})

describe('AppBottomNav：席位契约', () => {
  it('最多 5 席：3 primary + 「更多」', async () => {
    const w = await mountAt('/dashboard', 'compact')
    const seats = w.findAll('.app-bottom-nav__seat')
    expect(seats).toHaveLength(4) // 3 primary + more
    expect(seats.length).toBeLessThanOrEqual(5)
  })

  it('primary 超过 3 个时截断，不做第二行底栏', async () => {
    const w = await mountAt('/dashboard', 'compact')
    const nav = w.find('[data-testid="app-bottom-nav"]')
    // 源里有 5 个 primary，渲染的 data-nav-path 只有 3 个
    expect(nav.findAll('[data-nav-path]')).toHaveLength(3)
    expect(source).toMatch(/MAX_PRIMARY = 3/)
  })

  it('「更多」不是路由：它 emit more，由父级唤起抽屉，不建立 Tab 栈', async () => {
    const w = await mountAt('/dashboard', 'compact')
    await w.find('[data-testid="app-bottom-nav-more"]').trigger('click')
    expect(w.emitted('more')).toHaveLength(1)
    expect(w.find('[data-testid="app-bottom-nav-more"]').attributes('data-nav-path')).toBeUndefined()
  })

  it('点击席位 emit navigate 并带上 NavItem', async () => {
    const w = await mountAt('/dashboard', 'compact')
    await w.find('[data-nav-path="/chat"]').trigger('click')
    const ev = w.emitted('navigate')
    expect(ev).toBeTruthy()
    expect((ev![0][0] as { path: string }).path).toBe('/chat')
  })
})

describe('AppBottomNav：激活态与无障碍', () => {
  it('当前项 aria-current="page" 且 data-active', async () => {
    const w = await mountAt('/requests', 'compact')
    const active = w.findAll('[aria-current="page"]')
    expect(active).toHaveLength(1)
    expect(active[0].attributes('data-nav-path')).toBe('/requests')
  })

  it('带 exact 的项不做前缀匹配（/dashboard 不应点亮 /dashboard/xxx）', async () => {
    const w = await mountAt('/dashboard', 'compact')
    expect(w.find('[data-nav-path="/dashboard"]').attributes('aria-current')).toBe('page')
    // 非 exact 项在子路径也应点亮
    const w2 = await mountAt('/chat', 'compact')
    expect(w2.find('[data-nav-path="/chat"]').attributes('aria-current')).toBe('page')
  })

  it('有可访问名（aria-label）与 nav 语义', async () => {
    const w = await mountAt('/dashboard', 'compact')
    const nav = w.find('[data-testid="app-bottom-nav"]')
    expect(nav.element.tagName).toBe('NAV')
    expect(nav.attributes('aria-label')).toBe('主导航')
    expect(w.find('[data-testid="app-bottom-nav-more"]').attributes('aria-label')).toBeTruthy()
  })
})

describe('AppBottomNav：样式红线（源码断言）', () => {
  it('每席触控高度 ≥48px（R1：新控件标准；此前此处把 44px 钉成了「红线」）', () => {
    // ⚠ 这条门**曾经把 44px 当红线钉住**——门禁主动保护了一个已被 R1 取代的值。
    //    44px 是**存量控件**的下限，本组件是本专题新建的，所以适用 48px。
    //    总门 `gates.spec.ts` 的 R1 那条会独立复扫，这里是同值的局部断言。
    expect(source).toMatch(/\.app-bottom-nav__seat\s*\{[^}]*min-height:\s*48px/)
  })

  it('z-index 低于抽屉(40)，保证「更多」打开抽屉后仍能点到菜单', () => {
    const z = Number(source.match(/z-index:\s*(\d+)/)?.[1])
    expect(z).toBe(30)
    expect(z).toBeLessThan(40)
  })

  it('消费一次 safe-area-inset-bottom', () => {
    // 先剥注释再统计：注释里会**提到** env()（说明规则），
    // 把它算进声明数会把「1 次消费」误判成「2 次」。
    const code = source.replace(/\/\*[\s\S]*?\*\//g, '')
    const n = (code.match(/env\(safe-area-inset-bottom/g) ?? []).length
    expect(n).toBe(1)
  })

  it('不引入固定 px 断点（档位由 useWindowClass 决定）', () => {
    expect(source).not.toMatch(/@media[^{]*\d{3,}px/)
  })
})

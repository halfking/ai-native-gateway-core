// ResponsiveDataView.spec.ts — 双模板容器门禁（docs/UI规范/00 §5.4 · H3）。
//
// 三条不可省规则的验证：
// 1. compact 强制卡片且**不渲染**切换钮（v-if，不是 CSS 隐藏 —— 否则断言不到「不存在」）
// 2. 切换形态**不重新打接口**（源码级断言：本组件不得出现任何请求调用）
// 3. loading / empty 在两种形态之外统一裁定，不出现「表格有骨架、卡片空白」
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { beforeEach, describe, expect, it } from 'vitest'
import ResponsiveDataView from './ResponsiveDataView.vue'
import { _resetForTests } from '../../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../../composables/useDataViewMode'

const source = readFileSync(resolve(process.cwd(), 'src/components/ui/ResponsiveDataView.vue'), 'utf8')
/**
 * 剥掉**全部**注释后再做源码断言。
 * 只剥块/模板注释是不够的：解释性 `//` 行注释里可能出现被禁的词，
 * 会被误判成违规（本仓既有教训）。行注释正则要避开 `://`。
 */
const codeOnly = source
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/<!--[\s\S]*?-->/g, '')
  .replace(/(^|[^:'"`\\])\/\/[^\n]*/g, '$1')

function mockWindowClass(cls: 'compact' | 'medium' | 'expanded' | 'large'): void {
  _resetForTests()
  const lo = { compact: 0, medium: 768, expanded: 1024, large: 1440 }[cls]
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (query: string) => {
      const min = query.match(/min-width:\s*([\d.]+)px/)
      const max = query.match(/max-width:\s*([\d.]+)px/)
      const matches = min ? lo >= parseFloat(min[1]) : max ? lo <= parseFloat(max[1]) : false
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
}

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  messages: { 'zh-CN': { hyper: { dataView: { table: '表格', cards: '卡片' }, list: { empty: '暂无记录' } } } },
})

const ROWS = [
  { id: 1, name: 'alpha', status: 'ok' },
  { id: 2, name: 'beta', status: 'bad' },
]

function factory(props: Record<string, unknown> = {}) {
  return mount(ResponsiveDataView, {
    props: { rows: ROWS, titleKey: 'name', ...props },
    global: { plugins: [i18n] },
    slots: { table: '<table class="fake-table"><tbody><tr><td>表格</td></tr></tbody></table>' },
  })
}

beforeEach(() => {
  _resetDataViewModeForTests()
  mockWindowClass('expanded')
})

describe('ResponsiveDataView：非 compact 档', () => {
  it('默认渲染表格模板，切换钮存在', () => {
    const w = factory()
    expect(w.find('.fake-table').exists()).toBe(true)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.findAll('.responsive-data-view__switch-btn')).toHaveLength(2)
    expect(w.attributes('data-view')).toBe('table')
  })

  it('点「卡片」切到卡片模板，再点回表格', async () => {
    const w = factory()
    const [tableBtn, cardsBtn] = w.findAll('.responsive-data-view__switch-btn')

    await cardsBtn.trigger('click')
    expect(w.find('[data-testid="card-list"]').exists()).toBe(true)
    expect(w.find('.fake-table').exists()).toBe(false)
    expect(w.attributes('data-view')).toBe('cards')

    await tableBtn.trigger('click')
    expect(w.find('.fake-table').exists()).toBe(true)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('aria-pressed 反映当前形态', async () => {
    const w = factory()
    const [tableBtn, cardsBtn] = w.findAll('.responsive-data-view__switch-btn')
    expect(tableBtn.attributes('aria-pressed')).toBe('true')
    expect(cardsBtn.attributes('aria-pressed')).toBe('false')

    await cardsBtn.trigger('click')
    expect(w.findAll('.responsive-data-view__switch-btn')[0].attributes('aria-pressed')).toBe('false')
    expect(w.findAll('.responsive-data-view__switch-btn')[1].attributes('aria-pressed')).toBe('true')
  })

  it('allowSwitch=false 时桌面也不渲染切换钮', () => {
    const w = factory({ allowSwitch: false })
    expect(w.findAll('.responsive-data-view__switch-btn')).toHaveLength(0)
    expect(w.find('.fake-table').exists()).toBe(true)
  })
})

describe('ResponsiveDataView：compact 档', () => {
  beforeEach(() => mockWindowClass('compact'))

  it('强制卡片，切换钮**不渲染**', () => {
    const w = factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(true)
    expect(w.find('.fake-table').exists()).toBe(false)
    expect(w.attributes('data-view')).toBe('cards')
    // v-if 而非 CSS 隐藏：DOM 里根本不该有这个节点
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
  })

  it('即使存储里预置了 table，compact 仍渲染卡片', () => {
    localStorage.setItem('llmgw_data_view_mode', 'table')
    const w = factory()
    expect(w.find('[data-testid="card-list"]').exists()).toBe(true)
    expect(w.find('.fake-table').exists()).toBe(false)
  })

  it('allowSwitch=true 也救不回 compact 的切换入口', () => {
    const w = factory({ allowSwitch: true })
    expect(w.findAll('.responsive-data-view__switch-btn')).toHaveLength(0)
  })
})

describe('ResponsiveDataView：三态统一', () => {
  it('loading 时两种形态都不渲染，只出骨架', async () => {
    const w = factory({ loading: true })
    expect(w.find('.responsive-data-view__state').exists()).toBe(true)
    expect(w.find('.fake-table').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
  })

  it('loading 优先于 empty', () => {
    const w = factory({ loading: true, empty: true })
    expect(w.find('.responsive-data-view__state').exists()).toBe(true)
  })

  it('empty 态不渲染任何形态模板', () => {
    const w = factory({ empty: true })
    expect(w.find('.fake-table').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.text()).toContain('暂无记录')
  })

  it('emptyText 覆盖默认文案', () => {
    const w = factory({ empty: true, emptyText: '这个租户还没有请求' })
    expect(w.text()).toContain('这个租户还没有请求')
  })

  it('loading 解除后形态模板回来', async () => {
    const w = factory({ loading: true })
    await w.setProps({ loading: false })
    expect(w.find('.fake-table').exists()).toBe(true)
  })
})

describe('ResponsiveDataView：静态约束', () => {
  it('组件内不得出现任何请求调用（切换形态不重新打接口）', () => {
    for (const banned of ['fetch(', 'axios', 'XMLHttpRequest', 'useQuery', 'useFetch', 'api.']) {
      expect(codeOnly, `ResponsiveDataView 不应直接发起请求，却出现了 ${banned}`).not.toContain(banned)
    }
  })

  it('切换钮由 canSwitch 门控，而不是 CSS 隐藏', () => {
    expect(codeOnly).toContain('switchVisible')
    expect(codeOnly).toContain('canSwitch')
    // 隐藏写法会让「compact 下不存在切换入口」这条不可断言
    expect(codeOnly).not.toMatch(/display:\s*none[^}]*switch/i)
  })

  it('rows 是唯一数据来源：两个模板共用同一个 rows prop', () => {
    const tableSlot = codeOnly.match(/<slot name="table"[^>]*>/)
    const cardList = codeOnly.match(/<CardList[\s\S]*?>/)
    expect(tableSlot).not.toBeNull()
    expect(cardList).not.toBeNull()
    expect(tableSlot![0]).toContain(':rows="rows"')
    expect(cardList![0]).toContain(':rows="rows"')
  })
})

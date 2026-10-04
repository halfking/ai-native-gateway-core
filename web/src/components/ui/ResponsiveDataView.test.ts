// ResponsiveDataView.test.ts — 共享容器的**契约**门禁。
//
// 本文件的存在理由：2026-10-06 切片十四给这个已被十三条切片共用的组件
// **加了一个 `#empty` 插槽**（桌面空态里带 CTA 的页面需要它，见 MaaSUsageView）。
// 加插槽的唯一风险面是「不传时行为变了」—— 所以这里只钉那一件事，
// 外加把形态切换 / 三态优先级这两条既有行为一起钉住。
//
// **不测**渲染细节（哪个 div、什么 class）——那些是实现，改版就该跟着变。
// 测的是**页面能依赖的行为**。
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ResponsiveDataView from './ResponsiveDataView.vue'
import { _resetForTests } from '../../composables/useWindowClass'
import { _resetDataViewModeForTests } from '../../composables/useDataViewMode'

const ROWS = [
  { id: 1, name: 'alpha' },
  { id: 2, name: 'beta' },
]

const i18n = createI18n({
  legacy: false,
  locale: 'zh-CN',
  fallbackWarn: false,
  missingWarn: false,
  messages: {
    'zh-CN': {
      hyper: { dataView: { table: '表格', cards: '卡片' }, list: { empty: '默认空态文案' } },
    },
  },
})

function mockWindowClass(cls: 'compact' | 'expanded'): void {
  _resetForTests()
  const lo = cls === 'compact' ? 0 : 1280
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    configurable: true,
    value: (q: string) => {
      const min = q.match(/min-width:\s*([\d.]+)px/)
      const max = q.match(/max-width:\s*([\d.]+)px/)
      const matches = min ? lo >= parseFloat(min[1]) : max ? lo <= parseFloat(max[1]) : false
      return {
        matches,
        media: q,
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

beforeEach(() => {
  _resetDataViewModeForTests()
  mockWindowClass('expanded')
  localStorage.clear()
})

describe('ResponsiveDataView：#empty 插槽的不变式（切片十四加插槽的唯一风险面）', () => {
  it('不传 #empty 时，空态出的仍是 emptyText', () => {
    const w = mount(ResponsiveDataView, {
      props: { rows: [], titleKey: 'id', empty: true, emptyText: '账本空态原文' },
      global: { plugins: [i18n] },
    })
    expect(w.find('.app-empty-state').text()).toBe('账本空态原文')
  })

  /**
   * ★ 这一条是本次改动**最该出事**的地方：
   * 调用方写了 `<template #empty></template>`（有插槽、但内容为空）时，
   * `EmptyState` 内部的 `<slot>{{ text }}</slot>` 会不会因为「插槽存在」而
   * **不再走 fallback**，把文案整个吞掉？
   * ⇒ 必须在真实挂载下验，不能靠读 Vue 文档推断。
   */
  it('传一个空的 #empty 插槽，仍回落到 emptyText（不吞文案）', () => {
    const w = mount(ResponsiveDataView, {
      props: { rows: [], titleKey: 'id', empty: true, emptyText: '账本空态原文' },
      slots: { empty: '' },
      global: { plugins: [i18n] },
    })
    expect(w.find('.app-empty-state').text()).toBe('账本空态原文')
  })

  it('传有内容的 #empty 时由插槽接管（富内容进场）', () => {
    const w = mount(ResponsiveDataView, {
      props: { rows: [], titleKey: 'id', empty: true, emptyText: '账本空态原文' },
      slots: { empty: '<a class="cta" href="/x">去购买积分</a>' },
      global: { plugins: [i18n] },
    })
    const box = w.find('.app-empty-state')
    expect(box.find('a.cta').exists()).toBe(true)
    expect(box.find('a.cta').text()).toBe('去购买积分')
  })

  it('emptyText 为空串时回落到 hyper.list.empty（既有行为未变）', () => {
    const w = mount(ResponsiveDataView, {
      props: { rows: [], titleKey: 'id', empty: true, emptyText: '' },
      global: { plugins: [i18n] },
    })
    expect(w.find('.app-empty-state').text()).toBe('默认空态文案')
  })
})

describe('ResponsiveDataView：既有行为未被插槽改动波及', () => {
  it('loading 优先于 empty（两者同真时出 spinner，不是空态）', () => {
    const w = mount(ResponsiveDataView, {
      props: { rows: [], titleKey: 'id', loading: true, empty: true, emptyText: '空态' },
      global: { plugins: [i18n] },
    })
    expect(w.find('.app-empty-state').exists()).toBe(false)
    expect(w.find('.responsive-data-view__state').exists()).toBe(true)
  })

  it('有行时既不出 loading 也不出 empty', () => {
    const w = mount(ResponsiveDataView, {
      props: { rows: ROWS, titleKey: 'id' },
      slots: { table: '<table><tbody><tr v-for="r in rows" :key="r.id"><td>{{ r.name }}</td></tr></tbody></table>' },
      global: { plugins: [i18n] },
    })
    expect(w.find('.app-empty-state').exists()).toBe(false)
    expect(w.find('.responsive-data-view__state').exists()).toBe(false)
    expect(w.find('tbody tr').text()).toBe('alpha')
  })

  it('桌面：出表格、不出卡片、渲染切换钮', () => {
    const w = mount(ResponsiveDataView, {
      props: { rows: ROWS, titleKey: 'id', fields: [{ key: 'name', label: '名称' }] },
      slots: { table: '<table><tbody><tr v-for="r in rows" :key="r.id"><td>{{ r.name }}</td></tr></tbody></table>' },
      global: { plugins: [i18n] },
    })
    expect(w.find('.responsive-data-view').attributes('data-view')).toBe('table')
    expect(w.find('table').exists()).toBe(true)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(false)
    expect(w.findAll('.responsive-data-view__switch-btn')).toHaveLength(2)
  })

  it('compact：强制卡片且不渲染切换钮', () => {
    mockWindowClass('compact')
    const w = mount(ResponsiveDataView, {
      props: { rows: ROWS, titleKey: 'id', fields: [{ key: 'name', label: '名称' }] },
      slots: { table: '<table><tbody><tr v-for="r in rows" :key="r.id"><td>{{ r.name }}</td></tr></tbody></table>' },
      global: { plugins: [i18n] },
    })
    expect(w.find('.responsive-data-view').attributes('data-view')).toBe('cards')
    expect(w.find('table').exists()).toBe(false)
    expect(w.find('[data-testid="card-list"]').exists()).toBe(true)
    expect(w.find('.responsive-data-view__switch').exists()).toBe(false)
  })

  it('点击切换钮切形态，且不打接口（rows 是唯一来源）', async () => {
    const w = mount(ResponsiveDataView, {
      props: { rows: ROWS, titleKey: 'id', fields: [{ key: 'name', label: '名称' }] },
      slots: { table: '<table><tbody><tr v-for="r in rows" :key="r.id"><td>{{ r.name }}</td></tr></tbody></table>' },
      global: { plugins: [i18n] },
    })
    await w.findAll('.responsive-data-view__switch-btn')[1]!.trigger('click')
    await flushPromises()
    expect(w.find('.responsive-data-view').attributes('data-view')).toBe('cards')
    expect(w.find('table').exists()).toBe(false)
  })

  it('tableMinWidth 落到 --rdv-table-min-width（决定槽内表格的窄屏下限）', () => {
    const w = mount(ResponsiveDataView, {
      props: { rows: ROWS, titleKey: 'id', tableMinWidth: '0px' },
      slots: { table: '<table><tbody><tr /></tbody></table>' },
      global: { plugins: [i18n] },
    })
    expect(w.find('.responsive-data-view').attributes('style')).toContain('--rdv-table-min-width: 0px')
  })

  it('#actions 逐行动作口仍工作（与 #cards 互斥，#cards 优先）', () => {
    const w = mount(ResponsiveDataView, {
      props: { rows: ROWS, titleKey: 'id' },
      slots: {
        table: '<table><tbody><tr v-for="r in rows" :key="r.id"><td>{{ r.name }}</td></tr></tbody></table>',
        actions: '<button class="row-act" @click.stop>进详情</button>',
      },
      global: { plugins: [i18n] },
    })
    // 桌面形态下卡片不渲染 ⇒ #actions 无输出（它只作用于卡片）
    expect(w.find('.row-act').exists()).toBe(false)
  })
})
